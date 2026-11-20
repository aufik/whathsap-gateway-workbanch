package runner

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"

	"whatshap-gateway/internal/ai"
	"whatshap-gateway/internal/config"
	"whatshap-gateway/internal/excel"
	"whatshap-gateway/internal/model"
	"whatshap-gateway/internal/wa"
)

var ErrAlreadyRunning = errors.New("pengujian sedang berjalan; silakan jeda atau tunggu selesai terlebih dahulu")

// maxDeviceAttempts is how often a question may be retried after the channel
// drops mid-conversation before it is marked as failed.
const maxDeviceAttempts = 2

type EventSink func(eventType string, payload any)

type Runner struct {
	mu        sync.Mutex
	cfg       *config.AppConfig
	store     *excel.Store
	aiEngine  *ai.Engine
	waClient  *wa.Client
	emit      EventSink
	cancelRun context.CancelFunc

	status    model.RunnerStatus
	telemetry model.HumanTelemetry

	// channels maps deviceUUID to its own real-time live human cadence telemetry,
	// allowing 1 to 3 concurrent WA channels to be monitored independently.
	channels map[string]model.HumanTelemetry

	// overrides holds the active user's behaviour settings; zero value means
	// "use process defaults from cfg".
	overrides model.HumanSettings

	// pool holds every WhatsApp channel of the active tenant. When nil the
	// runner falls back to the single legacy client.
	pool *wa.Pool

	// deviceAttempts counts mid-question disconnects per row so a flaky channel
	// cannot burn the whole batch.
	deviceAttempts map[int]int

	// requeue feeds the running worker with a row that must be retried.
	requeue chan int

	// testedByDevice records which channel produced the current result, keyed by
	// row number, so metrics can attribute each answer to a phone number.
	testedByDevice map[int]map[string]string

	// onRowSaved is invoked after a row result is persisted, so the tenant
	// workbook can be flushed to PostgreSQL asynchronously.
	onRowSaved func()
}

func NewRunner(
	cfg *config.AppConfig,
	store *excel.Store,
	aiEngine *ai.Engine,
	waClient *wa.Client,
	emit EventSink,
) *Runner {
	s := cfg.GetSettings()
	_ = s
	r := &Runner{
		cfg:      cfg,
		store:    store,
		aiEngine: aiEngine,
		waClient: waClient,
		emit:     emit,
		status: model.RunnerStatus{
			State:       "idle",
			TargetSheet: model.SheetTargetBank,
		},
		telemetry: model.HumanTelemetry{
			Phase:              "idle",
			PhaseLabel:         "Menunggu perintah pengujian",
			Presence:           "unavailable",
			NextBreakTarget:    randRange(s.CooldownEveryMin, s.CooldownEveryMax),
			NextLongRestTarget: randRange(s.LongRestEveryMin, s.LongRestEveryMax),
			RecentLogs:         []model.BehaviorLog{},
		},
	}
	return r
}

// SetPool attaches the tenant's device pool. With a pool present, questions are
// distributed round-robin across idle devices instead of a single client.
func (r *Runner) SetPool(pool *wa.Pool) {
	r.mu.Lock()
	r.pool = pool
	r.mu.Unlock()
}

func (r *Runner) currentPool() *wa.Pool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pool
}

// acquireDevice returns an idle device from the pool, or the legacy client.
func (r *Runner) acquireDevice(rowNo int) (*wa.Client, func(), bool) {
	pool := r.currentPool()
	if pool == nil {
		if r.waClient == nil {
			return nil, func() {}, false
		}
		return r.waClient, func() {}, true
	}

	for attempt := 0; attempt < 2; attempt++ {
		device := pool.NextIdle()
		if device == nil {
			return nil, func() {}, false
		}
		if device.TryLockRow(rowNo) {
			release := func() { device.UnlockRow(rowNo) }
			return device, release, true
		}
	}
	return nil, func() {}, false
}

// activeSettings returns the caller's saved settings, falling back to defaults.
func (r *Runner) activeSettings() model.HumanSettings {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.overrides.BotPhone != "" || r.overrides.QuestionMode != "" {
		return r.overrides
	}
	return r.cfg.GetSettings()
}

func (r *Runner) noteDeviceFailure(rowNo int) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.deviceAttempts == nil {
		r.deviceAttempts = map[int]int{}
	}
	r.deviceAttempts[rowNo]++
	return r.deviceAttempts[rowNo]
}

// requeueRow pushes a row back to the queue so another channel can take it.
func (r *Runner) requeueRow(rowNo int) {
	r.mu.Lock()
	ch := r.requeue
	r.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- rowNo:
	default:
	}
}

// persistDeviceFailure writes the final note when attempts are exhausted.
func (r *Runner) persistDeviceFailure(rowNo int, apply func(note, status string) error) {
	note := "Perangkat terputus 2x"
	status := "Tidak"
	if err := apply(note, status); err != nil {
		r.mu.Lock()
		r.status.LastError = err.Error()
		r.mu.Unlock()
	}
}

// SetDeviceIdentity stores the channel identity (uuid + phone) for the row
// currently being processed, so the persisted metrics can record it.
func (r *Runner) SetDeviceIdentity(rowNo int, deviceUUID, phone string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status.ActiveRows == nil {
		r.status.ActiveRows = map[string]int{}
	}
	if deviceUUID != "" {
		r.status.ActiveRows[deviceUUID] = rowNo
	}
	if r.testedByDevice == nil {
		r.testedByDevice = map[int]map[string]string{}
	}
	r.testedByDevice[rowNo] = map[string]string{"device_uuid": deviceUUID, "phone_number": phone}
}

func (r *Runner) takeDeviceIdentity(rowNo int) (string, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	info := r.testedByDevice[rowNo]
	delete(r.testedByDevice, rowNo)
	for id, no := range r.status.ActiveRows {
		if no == rowNo {
			delete(r.status.ActiveRows, id)
		}
	}
	if info == nil {
		return "", ""
	}
	return info["device_uuid"], info["phone_number"]
}

// OnRowSaved registers a callback fired after each persisted result.
func (r *Runner) OnRowSaved(fn func()) {
	r.mu.Lock()
	r.onRowSaved = fn
	r.mu.Unlock()
}

// SetStore swaps the workbook this runner writes to (used after a per-user
// Excel upload). Callers must ensure the runner is idle.
func (r *Runner) SetStore(store *excel.Store) {
	r.mu.Lock()
	r.store = store
	r.mu.Unlock()
}

func (r *Runner) Status() model.RunnerStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}

func (r *Runner) Telemetry() model.HumanTelemetry {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.copyTelemetryLocked()
}

// ChannelTelemetry returns a snapshot of active per-channel telemetries.
func (r *Runner) ChannelTelemetry() map[string]model.HumanTelemetry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]model.HumanTelemetry, len(r.channels))
	for id, t := range r.channels {
		cp := t
		cp.RecentLogs = append([]model.BehaviorLog(nil), t.RecentLogs...)
		out[id] = cp
	}
	return out
}

func normalizeTargetSheet(raw string) string {
	switch strings.TrimSpace(strings.ToLower(raw)) {
	case model.SheetTargetOutOfBank:
		return model.SheetTargetOutOfBank
	case model.SheetTargetStress:
		return model.SheetTargetStress
	default:
		return model.SheetTargetBank
	}
}

func (r *Runner) StartBatch(targetSheet string, forceAll bool, limit int, rangeFrom, rangeTo int) error {
	sheet := normalizeTargetSheet(targetSheet)
	settings := r.activeSettings()
	if strings.TrimSpace(settings.BotPhone) == "" {
		return errors.New("nomor WhatsApp Bot target wajib diisi terlebih dahulu")
	}
	if pool := r.currentPool(); pool != nil {
		if pool.ConnectedCount() == 0 {
			return errors.New("belum ada nomor WhatsApp penguji yang terhubung untuk akun ini")
		}
	} else if r.waClient == nil || !r.waClient.Status().LoggedIn || !r.waClient.Status().Connected {
		return errors.New("WhatsApp penguji belum terhubung; silakan scan QR atau gunakan Pairing Code")
	}

	var queue []int
	isRangeMode := rangeFrom > 0 && rangeTo >= rangeFrom

	if sheet == model.SheetTargetOutOfBank {
		if isRangeMode {
			queue = r.store.PendingOutOfBankRange(rangeFrom, rangeTo, forceAll)
		} else {
			queue = r.store.PendingOutOfBankNumbers(forceAll, limit)
		}
		if len(queue) == 0 {
			if isRangeMode {
				return fmt.Errorf("tidak ada soal Uji Diluar Bank dalam rentang #%d s/d #%d yang dapat diuji", rangeFrom, rangeTo)
			}
			return errors.New("tidak ada soal Uji Diluar Bank yang belum diuji (tambahkan/generate soal terlebih dahulu)")
		}
	} else if sheet == model.SheetTargetStress {
		if isRangeMode {
			queue = r.store.PendingStressRange(rangeFrom, rangeTo, forceAll)
		} else {
			queue = r.store.PendingStressNumbers(forceAll, limit)
		}
		if len(queue) == 0 {
			if isRangeMode {
				return fmt.Errorf("tidak ada soal Uji Stress AI dalam rentang #%d s/d #%d yang dapat diuji", rangeFrom, rangeTo)
			}
			return errors.New("tidak ada soal Uji Stress AI yang belum diuji (bangkitkan soal terlebih dahulu)")
		}
	} else {
		if isRangeMode {
			queue = r.store.PendingRowRange(rangeFrom, rangeTo, forceAll)
		} else {
			queue = r.store.PendingRowNumbers(forceAll, limit)
		}
		if len(queue) == 0 {
			if isRangeMode {
				return fmt.Errorf("tidak ada soal Bank Pertanyaan dalam rentang #%d s/d #%d yang dapat diuji", rangeFrom, rangeTo)
			}
			return errors.New("seluruh baris pada Bank Pertanyaan sudah selesai diuji")
		}
	}

	r.mu.Lock()
	if r.status.State == "running" {
		r.mu.Unlock()
		return ErrAlreadyRunning
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancelRun = cancel
	r.requeue = make(chan int, len(queue)+8)
	r.deviceAttempts = map[int]int{}
	r.status = model.RunnerStatus{
		State:          "running",
		Mode:           "batch",
		TargetSheet:    sheet,
		CurrentRowNo:   queue[0],
		ProcessedInRun: 0,
		TargetInRun:    len(queue),
		StartedAt:      time.Now().UnixMilli(),
		LastError:      "",
	}
	r.telemetry.CircuitBreakerTripped = false
	r.telemetry.CircuitBreakerReason = ""
	if r.telemetry.NextBreakTarget <= 0 {
		r.telemetry.NextBreakTarget = randRange(settings.CooldownEveryMin, settings.CooldownEveryMax)
	}
	if r.telemetry.NextLongRestTarget <= 0 {
		r.telemetry.NextLongRestTarget = randRange(settings.LongRestEveryMin, settings.LongRestEveryMax)
	}

	sheetLabel := "Bank Pertanyaan"
	if sheet == model.SheetTargetOutOfBank {
		sheetLabel = "Sheet Uji Diluar Bank"
	} else if sheet == model.SheetTargetStress {
		sheetLabel = "Sheet Uji Stress AI"
	}
	desc := fmt.Sprintf("%d soal dipilih", len(queue))
	if isRangeMode {
		desc = fmt.Sprintf("rentang #%d s/d #%d (%d soal)", rangeFrom, rangeTo, len(queue))
	}
	r.appendLogLocked("batch_start", 0, fmt.Sprintf("Memulai batch %s [%s, mode kalimat: %s]", sheetLabel, desc, settings.QuestionMode))
	stSnap := r.status
	telSnap := r.copyTelemetryLocked()
	r.mu.Unlock()

	r.broadcast("runner_status", stSnap)
	r.broadcast("human_behavior", telSnap)

	go r.runQueueWorker(ctx, sheet, queue, true)
	return nil
}

func (r *Runner) RunSingleRow(targetSheet string, rowNo int) error {
	sheet := normalizeTargetSheet(targetSheet)
	settings := r.activeSettings()
	if strings.TrimSpace(settings.BotPhone) == "" {
		return errors.New("nomor WhatsApp Bot target wajib diisi terlebih dahulu")
	}
	if pool := r.currentPool(); pool != nil {
		if pool.ConnectedCount() == 0 {
			return errors.New("belum ada nomor WhatsApp penguji yang terhubung untuk akun ini")
		}
	} else if r.waClient == nil || !r.waClient.Status().LoggedIn || !r.waClient.Status().Connected {
		return errors.New("WhatsApp penguji belum terhubung; silakan scan QR atau gunakan Pairing Code")
	}

	if sheet == model.SheetTargetOutOfBank {
		if _, ok := r.store.GetOutOfBankRow(rowNo); !ok {
			return fmt.Errorf("baris Uji Diluar Bank #%d tidak ditemukan", rowNo)
		}
	} else if sheet == model.SheetTargetStress {
		if _, ok := r.store.GetStressTestRow(rowNo); !ok {
			return fmt.Errorf("baris Uji Stress AI #%d tidak ditemukan", rowNo)
		}
	} else {
		if _, ok := r.store.GetRow(rowNo); !ok {
			return fmt.Errorf("baris Bank Pertanyaan #%d tidak ditemukan", rowNo)
		}
	}

	r.mu.Lock()
	if r.status.State == "running" {
		r.mu.Unlock()
		return ErrAlreadyRunning
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancelRun = cancel
	r.status = model.RunnerStatus{
		State:          "running",
		Mode:           "single",
		TargetSheet:    sheet,
		CurrentRowNo:   rowNo,
		ProcessedInRun: 0,
		TargetInRun:    1,
		StartedAt:      time.Now().UnixMilli(),
		LastError:      "",
	}
	r.telemetry.CircuitBreakerTripped = false
	r.telemetry.CircuitBreakerReason = ""
	r.appendLogLocked("single_start", rowNo, fmt.Sprintf("Menguji tunggal [%s] Baris #%d", sheet, rowNo))
	stSnap := r.status
	telSnap := r.copyTelemetryLocked()
	r.mu.Unlock()

	r.broadcast("runner_status", stSnap)
	r.broadcast("human_behavior", telSnap)

	go r.runQueueWorker(ctx, sheet, []int{rowNo}, false)
	return nil
}

func (r *Runner) Pause() error {
	r.mu.Lock()
	if r.status.State != "running" {
		r.mu.Unlock()
		return nil
	}
	if r.cancelRun != nil {
		r.cancelRun()
		r.cancelRun = nil
	}
	r.status.State = "paused"
	now := time.Now().UnixMilli()
	r.telemetry.Phase = "paused"
	r.telemetry.PhaseLabel = "Dijeda oleh penguji — aman dilanjutkan kapan saja"
	r.telemetry.Presence = "unavailable"
	r.telemetry.PhaseStartedAt = now
	r.telemetry.PhaseEndsAt = now
	r.telemetry.PhaseDurationMs = 0
	r.appendLogLocked("paused", r.status.CurrentRowNo, "Pengujian dijeda oleh penguji")
	stSnap := r.status
	telSnap := r.copyTelemetryLocked()
	r.mu.Unlock()

	r.setPresenceAll(context.Background(), false)
	r.broadcast("runner_status", stSnap)
	r.broadcast("human_behavior", telSnap)
	return nil
}

func (r *Runner) Resume(limit int) error {
	r.mu.Lock()
	sheet := r.status.TargetSheet
	r.mu.Unlock()
	return r.StartBatch(sheet, false, limit, 0, 0)
}

func (r *Runner) Stop() error {
	r.mu.Lock()
	if r.cancelRun != nil {
		r.cancelRun()
		r.cancelRun = nil
	}
	r.status.State = "idle"
	r.status.CurrentRowNo = 0
	now := time.Now().UnixMilli()
	r.telemetry.Phase = "idle"
	r.telemetry.PhaseLabel = "Pengujian dihentikan (Idle)"
	r.telemetry.Presence = "unavailable"
	r.telemetry.ActiveRowNo = 0
	r.telemetry.PhaseStartedAt = now
	r.telemetry.PhaseEndsAt = now
	r.telemetry.PhaseDurationMs = 0
	r.appendLogLocked("stopped", 0, "Antrean pengujian dihentikan")
	stSnap := r.status
	telSnap := r.copyTelemetryLocked()
	r.mu.Unlock()

	r.setPresenceAll(context.Background(), false)
	r.broadcast("runner_status", stSnap)
	r.broadcast("human_behavior", telSnap)
	return nil
}

// maxParallelWorkers caps how many device channels may send questions at the
// same second. Set to 3 per user requirement.
const maxParallelWorkers = 3

func (r *Runner) runQueueWorker(ctx context.Context, sheet string, queue []int, isBatch bool) {
	// Active worker count is min(connected devices, maxParallelWorkers, queue length)
	workerCount := 1
	if pool := r.currentPool(); pool != nil {
		conn := pool.ConnectedCount()
		if conn > 1 {
			workerCount = conn
			if workerCount > maxParallelWorkers {
				workerCount = maxParallelWorkers
			}
		}
	}
	if !isBatch || len(queue) < workerCount {
		workerCount = 1
	}

	work := make(chan int, len(queue)+64)
	for _, no := range queue {
		work <- no
	}

	r.mu.Lock()
	r.status.State = "running"
	r.status.TargetInRun = len(queue)
	r.status.ProcessedInRun = 0
	r.status.ActiveRows = map[string]int{}
	r.mu.Unlock()

	var wg sync.WaitGroup
	for wID := 0; wID < workerCount; wID++ {
		wg.Add(1)
		go func(workerIndex int) {
			defer wg.Done()
			prevQueueDelayMs := 0
			prevBreakKind := ""

			for {
				if ctx.Err() != nil {
					return
				}

				var rowNo int
				select {
				case <-ctx.Done():
					return
				case no := <-work:
					rowNo = no
				case <-time.After(200 * time.Millisecond):
					if ch := r.dequeueRequeued(); ch != nil {
						select {
						case extra := <-ch:
							work <- extra
							continue
						default:
						}
					}
					// If the work queue is empty and no extra rows remain, this worker finishes
					if len(work) == 0 {
						return
					}
					continue
				}

				settings := r.activeSettings()
				r.mu.Lock()
				r.status.CurrentRowNo = rowNo
				stSnap := r.status
				r.mu.Unlock()
				r.broadcast("runner_status", stSnap)

				var okStep bool
				if sheet == model.SheetTargetOutOfBank {
					okStep = r.executeOutOfBankRow(ctx, rowNo, settings, prevQueueDelayMs, prevBreakKind)
				} else if sheet == model.SheetTargetStress {
					okStep = r.executeStressTestRow(ctx, rowNo, settings, prevQueueDelayMs, prevBreakKind)
				} else {
					okStep = r.executeBankRow(ctx, rowNo, isBatch, settings, prevQueueDelayMs, prevBreakKind)
				}
				if !okStep {
					return
				}

				if isBatch {
					prevQueueDelayMs, prevBreakKind = r.pauseBetweenQuestions(ctx, rowNo)
					if ctx.Err() != nil {
						return
					}
				}
			}
		}(wID)
	}

	wg.Wait()

	r.mu.Lock()
	if r.status.State == "running" {
		r.status.State = "idle"
		r.status.CurrentRowNo = 0
		r.status.ActiveRows = map[string]int{}
		now := time.Now().UnixMilli()
		r.telemetry.Phase = "idle"
		r.telemetry.PhaseLabel = "Sesi pengujian selesai — siap menunggu perintah berikutnya"
		r.telemetry.Presence = "unavailable"
		r.telemetry.ActiveRowNo = 0
		r.telemetry.PhaseStartedAt = now
		r.telemetry.PhaseEndsAt = now
		r.telemetry.PhaseDurationMs = 0
		r.appendLogLocked("completed", 0, fmt.Sprintf("Selesai memproses %d soal dan tersimpan otomatis ke Excel", r.status.ProcessedInRun))
	}
	stSnap := r.status
	telSnap := r.copyTelemetryLocked()
	r.mu.Unlock()

	r.setPresenceAll(context.Background(), false)
	r.broadcast("runner_status", stSnap)
	r.broadcast("human_behavior", telSnap)
}

func (r *Runner) pauseBetweenQuestions(ctx context.Context, rowNo int) (int, string) {
	r.mu.Lock()
	sinceBreak := r.telemetry.SinceLastBreak
	breakTarget := r.telemetry.NextBreakTarget
	sinceLong := r.telemetry.SinceLastLongRest
	longTarget := r.telemetry.NextLongRestTarget
	r.mu.Unlock()

	latest := r.activeSettings()

	if sinceLong >= longTarget {
		restMs := randRange(latest.LongRestMinMs, latest.LongRestMaxMs)
		nextLong := randRange(latest.LongRestEveryMin, latest.LongRestEveryMax)
		nextMicro := randRange(latest.CooldownEveryMin, latest.CooldownEveryMax)
		r.setPresenceAll(ctx, false)
		r.onTelemetryHook(
			"long_rest",
			fmt.Sprintf("Istirahat Panjang (Long-Rest) setelah %d soal selama %.1f menit agar akun aman...", sinceLong, float64(restMs)/60000),
			"unavailable",
			restMs,
			rowNo,
			func(t *model.HumanTelemetry) {
				t.TotalLongRestsTaken++
				t.LastCooldownMs = restMs
				t.SinceLastLongRest = 0
				t.NextLongRestTarget = nextLong
				t.SinceLastBreak = 0
				t.NextBreakTarget = nextMicro
			},
		)
		sleepCancelable(ctx, time.Duration(restMs)*time.Millisecond)
		return restMs, "long_rest"
	}

	if sinceBreak >= breakTarget {
		cooldownMs := randRange(latest.CooldownMinMs, latest.CooldownMaxMs)
		nextTarget := randRange(latest.CooldownEveryMin, latest.CooldownEveryMax)
		r.setPresenceAll(ctx, false)
		r.onTelemetryHook(
			"micro_break",
			fmt.Sprintf("Istirahat Manusiawi (Micro-Break) setelah %d soal selama %.0f dtk...", sinceBreak, float64(cooldownMs)/1000),
			"unavailable",
			cooldownMs,
			rowNo,
			func(t *model.HumanTelemetry) {
				t.TotalBreaksTaken++
				t.LastCooldownMs = cooldownMs
				t.SinceLastBreak = 0
				t.NextBreakTarget = nextTarget
			},
		)
		sleepCancelable(ctx, time.Duration(cooldownMs)*time.Millisecond)
		return cooldownMs, "micro_break"
	}

	isSpike := latest.DistractionSpikePct > 0 && rand.Intn(100) < latest.DistractionSpikePct
	interDelayMs := randRange(latest.DelayMinMs, latest.DelayMaxMs)
	kind := "normal"
	label := fmt.Sprintf("Jeda santai antar soal selama %.1f dtk...", float64(interDelayMs)/1000)
	if isSpike {
		interDelayMs = randRange(latest.DistractionMinMs, latest.DistractionMaxMs)
		kind = "spike"
		label = fmt.Sprintf("Jeda alami agak panjang (cek catatan Excel) selama %.1f dtk...", float64(interDelayMs)/1000)
	}
	r.onTelemetryHook(
		"inter_question_delay",
		label,
		"available",
		interDelayMs,
		rowNo,
		func(t *model.HumanTelemetry) { t.LastInterDelayMs = interDelayMs },
	)
	sleepCancelable(ctx, time.Duration(interDelayMs)*time.Millisecond)
	return interDelayMs, kind
}

// dequeueRequeued exposes the requeue channel for the worker select.
func (r *Runner) dequeueRequeued() chan int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.requeue
}

// setPresenceAll toggles presence for every channel of the tenant.
func (r *Runner) setPresenceAll(ctx context.Context, available bool) {
	if pool := r.currentPool(); pool != nil {
		for _, d := range pool.Devices() {
			d.SetPresenceAvailable(ctx, available)
		}
		return
	}
	if r.waClient != nil {
		r.waClient.SetPresenceAvailable(ctx, available)
	}
}

func (r *Runner) executeBankRow(
	ctx context.Context,
	rowNo int,
	isBatch bool,
	settings model.HumanSettings,
	prevQueueDelayMs int,
	prevBreakKind string,
) bool {
	row, ok := r.store.GetRow(rowNo)
	if !ok {
		return true
	}

	questionToSend := strings.TrimSpace(row.QuestionInput)
	if questionToSend == "" || settings.QuestionMode != model.ModeManual {
		if !isBatch && questionToSend != "" {
			// Keep previewed/edited QuestionInput on single-row run
		} else {
			gen, err := r.aiEngine.GenerateQuestion(ctx, row, settings.QuestionMode, time.Now().UnixNano())
			if err != nil {
				questionToSend = row.QuestionRef
			} else {
				questionToSend = gen
			}
		}
	}

	if updatedRow, sum, err := r.store.UpdateRow(rowNo, model.RowPatch{QuestionInput: &questionToSend}); err == nil {
		r.broadcast("row_updated", updatedRow)
		r.broadcast("summary_updated", sum)
	}

	rowLabel := fmt.Sprintf("Bank #%d", rowNo)
	device, release, okDevice := r.acquireDevice(rowNo)
	if !okDevice {
		r.appendLogLocked("device_wait", rowNo, "Menunggu perangkat WA yang tersedia...")
		return true
	}
	defer release()
	st := device.Status()
	r.SetDeviceIdentity(rowNo, device.DeviceID(), st.AccountJID)

	actualReply, mediaFiles, metrics, askErr := device.AskBotHumanized(
		ctx,
		rowLabel,
		rowNo,
		settings.BotPhone,
		questionToSend,
		settings,
		r.onTelemetryHook,
	)

	if askErr != nil {
		if errors.Is(askErr, context.Canceled) {
			return false
		}
		if errors.Is(askErr, wa.ErrCircuitBreaker) {
			return r.handleDeviceDrop(rowNo, rowLabel, askErr)
		}
		r.mu.Lock()
		r.status.LastError = askErr.Error()
		r.appendLogLocked("error", rowNo, fmt.Sprintf("Kendala pada %s: %v", rowLabel, askErr))
		r.mu.Unlock()
		return true
	}

	metrics.QueueDelayMs = prevQueueDelayMs
	metrics.BreakKind = prevBreakKind
	metrics.BotTargetPhone = strings.TrimSpace(settings.BotPhone)
	if uuid, phone := r.takeDeviceIdentity(rowNo); uuid != "" || phone != "" {
		metrics.TestedByDevice = &model.DeviceIdentity{DeviceUUID: uuid, PhoneNumber: phone}
		metrics.SenderPhone = phone
	}

	evalCtx, evalCancel := context.WithTimeout(ctx, 25*time.Second)
	resStatus, notes := r.aiEngine.EvaluateReply(evalCtx, row, actualReply)
	evalCancel()

	patch := model.RowPatch{
		QuestionInput: &questionToSend,
		ActualReply:   &actualReply,
		Notes:         &notes,
		ResultStatus:  &resStatus,
		Metrics:       &metrics,
	}
	if len(mediaFiles) > 0 {
		patch.MediaFiles = &mediaFiles
	}

	updatedRow, sum, saveErr := r.store.UpdateRow(rowNo, patch)
	if saveErr != nil {
		r.tripCircuitBreaker(rowNo, fmt.Sprintf("Gagal menyimpan ke Excel: %v", saveErr))
		return false
	}

	r.recordRowCompletion(rowLabel, rowNo, resStatus, len([]rune(actualReply)))
	r.broadcast("row_updated", updatedRow)
	r.broadcast("summary_updated", sum)
	return true
}

func (r *Runner) executeOutOfBankRow(
	ctx context.Context,
	rowNo int,
	settings model.HumanSettings,
	prevQueueDelayMs int,
	prevBreakKind string,
) bool {
	row, ok := r.store.GetOutOfBankRow(rowNo)
	if !ok {
		return true
	}

	questionToSend := strings.TrimSpace(row.ProbeQuestion)
	if questionToSend == "" {
		return true
	}

	rowLabel := fmt.Sprintf("DiluarBank #%d", rowNo)
	device, release, okDevice := r.acquireDevice(rowNo)
	if !okDevice {
		r.appendLogLocked("device_wait", rowNo, "Menunggu perangkat WA yang tersedia...")
		return true
	}
	defer release()
	st := device.Status()
	r.SetDeviceIdentity(rowNo, device.DeviceID(), st.AccountJID)

	actualReply, mediaFiles, metrics, askErr := device.AskBotHumanized(
		ctx,
		rowLabel,
		rowNo,
		settings.BotPhone,
		questionToSend,
		settings,
		r.onTelemetryHook,
	)

	if askErr != nil {
		if errors.Is(askErr, context.Canceled) {
			return false
		}
		if errors.Is(askErr, wa.ErrCircuitBreaker) {
			return r.handleDeviceDrop(rowNo, rowLabel, askErr)
		}
		r.mu.Lock()
		r.status.LastError = askErr.Error()
		r.appendLogLocked("error", rowNo, fmt.Sprintf("Kendala pada %s: %v", rowLabel, askErr))
		r.mu.Unlock()
		return true
	}

	metrics.QueueDelayMs = prevQueueDelayMs
	metrics.BreakKind = prevBreakKind
	metrics.BotTargetPhone = strings.TrimSpace(settings.BotPhone)
	if uuid, phone := r.takeDeviceIdentity(rowNo); uuid != "" || phone != "" {
		metrics.TestedByDevice = &model.DeviceIdentity{DeviceUUID: uuid, PhoneNumber: phone}
		metrics.SenderPhone = phone
	}

	evalCtx, evalCancel := context.WithTimeout(ctx, 25*time.Second)
	resStatus, notes := r.aiEngine.EvaluateOutOfBankReply(evalCtx, row, actualReply)
	evalCancel()

	patch := model.OutOfBankPatch{
		ActualReply:  &actualReply,
		Notes:        &notes,
		ResultStatus: &resStatus,
		Metrics:      &metrics,
	}
	if len(mediaFiles) > 0 {
		patch.MediaFiles = &mediaFiles
	}

	updatedOOB, oobSum, saveErr := r.store.UpdateOutOfBankRow(rowNo, patch)
	if saveErr != nil {
		r.tripCircuitBreaker(rowNo, fmt.Sprintf("Gagal menyimpan sheet Uji Diluar Bank: %v", saveErr))
		return false
	}

	r.recordRowCompletion(rowLabel, rowNo, resStatus, len([]rune(actualReply)))
	r.broadcast("oob_row_updated", updatedOOB)
	r.broadcast("oob_summary_updated", oobSum)
	return true
}

func (r *Runner) executeStressTestRow(
	ctx context.Context,
	rowNo int,
	settings model.HumanSettings,
	prevQueueDelayMs int,
	prevBreakKind string,
) bool {
	row, ok := r.store.GetStressTestRow(rowNo)
	if !ok {
		return true
	}

	questionToSend := strings.TrimSpace(row.UserPromptQ)
	if questionToSend == "" {
		return true
	}

	rowLabel := fmt.Sprintf("Stress #%d", rowNo)
	device, release, okDevice := r.acquireDevice(rowNo)
	if !okDevice {
		r.appendLogLocked("device_wait", rowNo, "Menunggu perangkat WA yang tersedia...")
		return true
	}
	defer release()
	st := device.Status()
	r.SetDeviceIdentity(rowNo, device.DeviceID(), st.AccountJID)

	actualReply, mediaFiles, metrics, askErr := device.AskBotHumanized(
		ctx,
		rowLabel,
		rowNo,
		settings.BotPhone,
		questionToSend,
		settings,
		r.onTelemetryHook,
	)

	if askErr != nil {
		if errors.Is(askErr, context.Canceled) {
			return false
		}
		if errors.Is(askErr, wa.ErrCircuitBreaker) {
			return r.handleDeviceDrop(rowNo, rowLabel, askErr)
		}
		r.mu.Lock()
		r.status.LastError = askErr.Error()
		r.appendLogLocked("error", rowNo, fmt.Sprintf("Kendala pada %s: %v", rowLabel, askErr))
		r.mu.Unlock()
		return true
	}

	metrics.QueueDelayMs = prevQueueDelayMs
	metrics.BreakKind = prevBreakKind
	metrics.BotTargetPhone = strings.TrimSpace(settings.BotPhone)
	if uuid, phone := r.takeDeviceIdentity(rowNo); uuid != "" || phone != "" {
		metrics.TestedByDevice = &model.DeviceIdentity{DeviceUUID: uuid, PhoneNumber: phone}
		metrics.SenderPhone = phone
	}

	evalCtx, evalCancel := context.WithTimeout(ctx, 25*time.Second)
	resStatus, notes, score, hal := r.aiEngine.EvaluateStressReply(evalCtx, row, actualReply)
	evalCancel()

	patch := model.StressTestPatch{
		ActualReply:      &actualReply,
		Notes:            &notes,
		ResultStatus:     &resStatus,
		PolitenessScore:  &score,
		HallucinationLvl: &hal,
		Metrics:          &metrics,
	}
	if len(mediaFiles) > 0 {
		patch.MediaFiles = &mediaFiles
	}

	updatedStress, stressSum, saveErr := r.store.UpdateStressTestRow(rowNo, patch)
	if saveErr != nil {
		r.tripCircuitBreaker(rowNo, fmt.Sprintf("Gagal menyimpan sheet Uji Stress AI: %v", saveErr))
		return false
	}

	r.recordRowCompletion(rowLabel, rowNo, resStatus, len([]rune(actualReply)))
	r.broadcast("stress_row_updated", updatedStress)
	r.broadcast("stress_summary_updated", stressSum)
	return true
}

func (r *Runner) recordRowCompletion(rowLabel string, rowNo int, resStatus string, replyChars int) {
	r.mu.Lock()
	r.status.ProcessedInRun++
	r.telemetry.SinceLastBreak++
	r.telemetry.SinceLastLongRest++
	r.appendLogLocked("evaluated", rowNo, fmt.Sprintf("%s selesai -> Status: %s (%d huruf balasan)", rowLabel, resStatus, replyChars))
	stSnap := r.status
	telSnap := r.copyTelemetryLocked()
	r.mu.Unlock()
	r.broadcast("runner_status", stSnap)
	r.broadcast("human_behavior", telSnap)
}

// handleDeviceDrop re-queues the question when a channel disconnects mid-chat.
// After maxDeviceAttempts failures the row is closed with a clear note so the
// batch can finish instead of looping forever.
func (r *Runner) handleDeviceDrop(rowNo int, rowLabel string, cause error) bool {
	attempts := r.noteDeviceFailure(rowNo)
	if attempts >= maxDeviceAttempts {
		note := "Perangkat terputus 2x"
		status := "Tidak"
		if _, _, err := r.store.UpdateRow(rowNo, model.RowPatch{Notes: &note, ResultStatus: &status}); err == nil {
			r.notifyRowSaved()
		}
		r.mu.Lock()
		r.appendLogLocked("device_failed", rowNo, fmt.Sprintf("%s: perangkat terputus %d kali, ditandai Tidak", rowLabel, attempts))
		r.mu.Unlock()
		return true
	}
	r.mu.Lock()
	r.appendLogLocked("device_retry", rowNo, fmt.Sprintf("%s: perangkat terputus (%v), dikembalikan ke antrean [percobaan %d]", rowLabel, cause, attempts))
	r.mu.Unlock()
	r.requeueRow(rowNo)
	return true
}

func (r *Runner) onTelemetryHook(
	phase string,
	label string,
	presence string,
	durationMs int,
	rowNo int,
	extra func(*model.HumanTelemetry),
) {
	now := time.Now().UnixMilli()
	r.mu.Lock()
	r.telemetry.Phase = phase
	r.telemetry.PhaseLabel = label
	r.telemetry.Presence = presence
	r.telemetry.PhaseStartedAt = now
	r.telemetry.PhaseEndsAt = now + int64(durationMs)
	r.telemetry.PhaseDurationMs = durationMs
	r.telemetry.ActiveRowNo = rowNo
	if extra != nil {
		extra(&r.telemetry)
	}
	r.appendLogLocked(phase, rowNo, label)
	snap := r.copyTelemetryLocked()

	// Update channel-specific telemetry map
	var devUUID string
	if info := r.testedByDevice[rowNo]; info != nil {
		devUUID = info["device_uuid"]
	}
	if devUUID != "" {
		if r.channels == nil {
			r.channels = map[string]model.HumanTelemetry{}
		}
		chTel := snap
		chTel.DeviceUUID = devUUID
		r.channels[devUUID] = chTel
	}
	allChannels := make(map[string]model.HumanTelemetry, len(r.channels))
	for id, t := range r.channels {
		allChannels[id] = t
	}
	r.mu.Unlock()

	r.broadcast("human_behavior", snap)
	if devUUID != "" {
		r.broadcast("channel_behavior", map[string]any{
			"deviceUuid": devUUID,
			"telemetry":  snap,
			"channels":   allChannels,
		})
	}
}

func (r *Runner) tripCircuitBreaker(rowNo int, reason string) {
	now := time.Now().UnixMilli()
	r.mu.Lock()
	if r.cancelRun != nil {
		r.cancelRun()
		r.cancelRun = nil
	}
	r.status.State = "paused"
	r.status.LastError = reason
	r.telemetry.Phase = "circuit_breaker"
	r.telemetry.PhaseLabel = "Circuit Breaker Aktif: Pengujian otomatis dijeda demi keamanan akun WA"
	r.telemetry.Presence = "unavailable"
	r.telemetry.PhaseStartedAt = now
	r.telemetry.PhaseEndsAt = now
	r.telemetry.PhaseDurationMs = 0
	r.telemetry.CircuitBreakerTripped = true
	r.telemetry.CircuitBreakerReason = reason
	r.appendLogLocked("circuit_breaker", rowNo, "OTOMATIS PAUSE: "+reason)
	stSnap := r.status
	telSnap := r.copyTelemetryLocked()
	r.mu.Unlock()

	r.setPresenceAll(context.Background(), false)
	r.broadcast("runner_status", stSnap)
	r.broadcast("human_behavior", telSnap)
}

func (r *Runner) appendLogLocked(phase string, rowNo int, detail string) {
	entry := model.BehaviorLog{
		Timestamp: time.Now().UnixMilli(),
		Phase:     phase,
		RowNo:     rowNo,
		Detail:    detail,
	}
	r.telemetry.RecentLogs = append([]model.BehaviorLog{entry}, r.telemetry.RecentLogs...)
	if len(r.telemetry.RecentLogs) > 50 {
		r.telemetry.RecentLogs = r.telemetry.RecentLogs[:50]
	}
}

func (r *Runner) copyTelemetryLocked() model.HumanTelemetry {
	cp := r.telemetry
	cp.RecentLogs = append([]model.BehaviorLog(nil), r.telemetry.RecentLogs...)
	return cp
}

func (r *Runner) broadcast(eventType string, payload any) {
	if r.emit != nil {
		r.emit(eventType, payload)
	}
}

// notifyRowSaved tells the tenant layer that a result reached disk.
func (r *Runner) notifyRowSaved() {
	r.mu.Lock()
	fn := r.onRowSaved
	r.mu.Unlock()
	if fn != nil {
		fn()
	}
}

func randRange(minVal, maxVal int) int {
	if maxVal <= minVal {
		return minVal
	}
	return minVal + rand.Intn(maxVal-minVal+1)
}

func sleepCancelable(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// SetSettings replaces the behaviour settings this runner uses. The web layer
// calls it after a user saves their own preferences.
func (r *Runner) SetSettings(userID int64, settings model.HumanSettings) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.overrides = settings
}
