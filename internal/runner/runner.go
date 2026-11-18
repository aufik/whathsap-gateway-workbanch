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
}

func NewRunner(
	cfg *config.AppConfig,
	store *excel.Store,
	aiEngine *ai.Engine,
	waClient *wa.Client,
	emit EventSink,
) *Runner {
	s := cfg.GetSettings()
	r := &Runner{
		cfg:      cfg,
		store:    store,
		aiEngine: aiEngine,
		waClient: waClient,
		emit:     emit,
		status: model.RunnerStatus{
			State: "idle",
		},
		telemetry: model.HumanTelemetry{
			Phase:           "idle",
			PhaseLabel:      "Menunggu perintah pengujian",
			Presence:        "unavailable",
			NextBreakTarget: randRange(s.CooldownEveryMin, s.CooldownEveryMax),
			RecentLogs:      []model.BehaviorLog{},
		},
	}
	return r
}

func (r *Runner) Status() model.RunnerStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}

func (r *Runner) Telemetry() model.HumanTelemetry {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := r.telemetry
	cp.RecentLogs = append([]model.BehaviorLog(nil), r.telemetry.RecentLogs...)
	return cp
}

func (r *Runner) StartBatch(forceAll bool) error {
	settings := r.cfg.GetSettings()
	if strings.TrimSpace(settings.BotPhone) == "" {
		return errors.New("nomor WhatsApp Bot target wajib diisi terlebih dahulu")
	}
	waSt := r.waClient.Status()
	if !waSt.LoggedIn || !waSt.Connected {
		return errors.New("WhatsApp penguji belum terhubung; silakan scan QR atau gunakan Pairing Code")
	}

	queue := r.store.PendingRowNumbers(forceAll)
	if len(queue) == 0 {
		return errors.New("seluruh baris pada Excel sudah selesai diuji")
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
		Mode:           "batch",
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
	r.appendLogLocked("batch_start", 0, fmt.Sprintf("Memulai batch sekuensial (%d soal antrean, mode kalimat: %s)", len(queue), settings.QuestionMode))
	stSnap := r.status
	telSnap := r.copyTelemetryLocked()
	r.mu.Unlock()

	r.broadcast("runner_status", stSnap)
	r.broadcast("human_behavior", telSnap)

	go r.runQueueWorker(ctx, queue, true)
	return nil
}

func (r *Runner) RunSingleRow(rowNo int) error {
	settings := r.cfg.GetSettings()
	if strings.TrimSpace(settings.BotPhone) == "" {
		return errors.New("nomor WhatsApp Bot target wajib diisi terlebih dahulu")
	}
	waSt := r.waClient.Status()
	if !waSt.LoggedIn || !waSt.Connected {
		return errors.New("WhatsApp penguji belum terhubung; silakan scan QR atau gunakan Pairing Code")
	}
	if _, ok := r.store.GetRow(rowNo); !ok {
		return fmt.Errorf("baris No %d tidak ditemukan", rowNo)
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
		CurrentRowNo:   rowNo,
		ProcessedInRun: 0,
		TargetInRun:    1,
		StartedAt:      time.Now().UnixMilli(),
		LastError:      "",
	}
	r.telemetry.CircuitBreakerTripped = false
	r.telemetry.CircuitBreakerReason = ""
	r.appendLogLocked("single_start", rowNo, fmt.Sprintf("Menguji tunggal Baris #%d (mode: %s)", rowNo, settings.QuestionMode))
	stSnap := r.status
	telSnap := r.copyTelemetryLocked()
	r.mu.Unlock()

	r.broadcast("runner_status", stSnap)
	r.broadcast("human_behavior", telSnap)

	go r.runQueueWorker(ctx, []int{rowNo}, false)
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

	r.waClient.SetPresenceAvailable(context.Background(), false)
	r.broadcast("runner_status", stSnap)
	r.broadcast("human_behavior", telSnap)
	return nil
}

func (r *Runner) Resume() error {
	return r.StartBatch(false)
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

	r.waClient.SetPresenceAvailable(context.Background(), false)
	r.broadcast("runner_status", stSnap)
	r.broadcast("human_behavior", telSnap)
	return nil
}

func (r *Runner) runQueueWorker(ctx context.Context, queue []int, isBatch bool) {
	for idx, rowNo := range queue {
		if ctx.Err() != nil {
			return
		}

		row, ok := r.store.GetRow(rowNo)
		if !ok {
			continue
		}

		settings := r.cfg.GetSettings()

		r.mu.Lock()
		r.status.CurrentRowNo = rowNo
		stSnap := r.status
		r.mu.Unlock()
		r.broadcast("runner_status", stSnap)

		// Prepare question text: if user already typed custom QuestionInput and mode == manual, keep it;
		// otherwise generate using selected mode (unless row already had manual edit when running single row?
		// If QuestionInput is already populated and mode == manual, use it; else generate fresh if empty or non-manual).
		questionToSend := strings.TrimSpace(row.QuestionInput)
		if questionToSend == "" || settings.QuestionMode != model.ModeManual {
			// If single-row test & user already pre-generated/edited QuestionInput, preserve it!
			if !isBatch && questionToSend != "" {
				// Keep user's previewed/edited QuestionInput on single-row run
			} else {
				gen, err := r.aiEngine.GenerateQuestion(ctx, row, settings.QuestionMode, time.Now().UnixNano())
				if err != nil {
					questionToSend = row.QuestionRef
				} else {
					questionToSend = gen
				}
			}
		}

		// Save QuestionInput immediately so UI reflects the exact outgoing question
		if updatedRow, sum, err := r.store.UpdateRow(rowNo, model.RowPatch{QuestionInput: &questionToSend}); err == nil {
			r.broadcast("row_updated", updatedRow)
			r.broadcast("summary_updated", sum)
		}

		// Execute humanized WhatsApp interaction (Presence -> Dynamic Typing -> Send -> Quiet-Window Collect -> MarkRead & Reading Delay)
		actualReply, askErr := r.waClient.AskBotHumanized(
			ctx,
			rowNo,
			settings.BotPhone,
			questionToSend,
			settings,
			r.onTelemetryHook,
		)

		if askErr != nil {
			if errors.Is(askErr, context.Canceled) {
				return
			}
			if errors.Is(askErr, wa.ErrCircuitBreaker) {
				r.tripCircuitBreaker(rowNo, askErr.Error())
				return
			}
			r.mu.Lock()
			r.status.LastError = askErr.Error()
			r.appendLogLocked("error", rowNo, fmt.Sprintf("Kendala pada Baris #%d: %v", rowNo, askErr))
			r.mu.Unlock()
			continue
		}

		// Evaluate answer & persist atomically to Excel
		evalCtx, evalCancel := context.WithTimeout(ctx, 25*time.Second)
		resStatus, notes := r.aiEngine.EvaluateReply(evalCtx, row, actualReply)
		evalCancel()

		updatedRow, sum, saveErr := r.store.UpdateRow(rowNo, model.RowPatch{
			QuestionInput: &questionToSend,
			ActualReply:   &actualReply,
			Notes:         &notes,
			ResultStatus:  &resStatus,
		})
		if saveErr != nil {
			r.tripCircuitBreaker(rowNo, fmt.Sprintf("Gagal menyimpan ke Excel: %v", saveErr))
			return
		}

		r.mu.Lock()
		r.status.ProcessedInRun++
		r.telemetry.SinceLastBreak++
		sinceBreak := r.telemetry.SinceLastBreak
		breakTarget := r.telemetry.NextBreakTarget
		r.appendLogLocked("evaluated", rowNo, fmt.Sprintf("Baris #%d selesai -> Status: %s (%d huruf balasan)", rowNo, resStatus, len([]rune(actualReply))))
		stSnap = r.status
		telSnap := r.copyTelemetryLocked()
		r.mu.Unlock()

		r.broadcast("row_updated", updatedRow)
		r.broadcast("summary_updated", sum)
		r.broadcast("runner_status", stSnap)
		r.broadcast("human_behavior", telSnap)

		// If there are remaining items in batch, perform Periodic Micro-Break or Randomized Inter-Question Jitter
		hasMore := isBatch && idx < len(queue)-1
		if !hasMore {
			break
		}

		latestSettings := r.cfg.GetSettings()
		if sinceBreak >= breakTarget {
			cooldownMs := randRange(latestSettings.CooldownMinMs, latestSettings.CooldownMaxMs)
			nextTarget := randRange(latestSettings.CooldownEveryMin, latestSettings.CooldownEveryMax)

			r.waClient.SetPresenceAvailable(ctx, false)
			r.onTelemetryHook(
				"micro_break",
				fmt.Sprintf("Istirahat manusiawi (Micro-Break) setelah %d soal selama %.0f dtk agar irama alami...", sinceBreak, float64(cooldownMs)/1000),
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
			if !sleepCancelable(ctx, time.Duration(cooldownMs)*time.Millisecond) {
				return
			}
		} else {
			interDelayMs := randRange(latestSettings.DelayMinMs, latestSettings.DelayMaxMs)
			r.onTelemetryHook(
				"inter_question_delay",
				fmt.Sprintf("Jeda santai antar soal selama %.1f dtk sebelum lanjut Baris #%d...", float64(interDelayMs)/1000, queue[idx+1]),
				"available",
				interDelayMs,
				queue[idx+1],
				func(t *model.HumanTelemetry) {
					t.LastInterDelayMs = interDelayMs
				},
			)
			if !sleepCancelable(ctx, time.Duration(interDelayMs)*time.Millisecond) {
				return
			}
		}
	}

	// Clean finish
	r.mu.Lock()
	if r.status.State == "running" {
		r.status.State = "idle"
		r.status.CurrentRowNo = 0
		now := time.Now().UnixMilli()
		r.telemetry.Phase = "idle"
		r.telemetry.PhaseLabel = "Pengujian selesai — siap menunggu perintah berikutnya"
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

	r.waClient.SetPresenceAvailable(context.Background(), false)
	r.broadcast("runner_status", stSnap)
	r.broadcast("human_behavior", telSnap)
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
	r.mu.Unlock()

	r.broadcast("human_behavior", snap)
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

	r.waClient.SetPresenceAvailable(context.Background(), false)
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
	if len(r.telemetry.RecentLogs) > 45 {
		r.telemetry.RecentLogs = r.telemetry.RecentLogs[:45]
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
