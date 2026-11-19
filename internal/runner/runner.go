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
	settings := r.cfg.GetSettings()
	if strings.TrimSpace(settings.BotPhone) == "" {
		return errors.New("nomor WhatsApp Bot target wajib diisi terlebih dahulu")
	}
	waSt := r.waClient.Status()
	if !waSt.LoggedIn || !waSt.Connected {
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
	settings := r.cfg.GetSettings()
	if strings.TrimSpace(settings.BotPhone) == "" {
		return errors.New("nomor WhatsApp Bot target wajib diisi terlebih dahulu")
	}
	waSt := r.waClient.Status()
	if !waSt.LoggedIn || !waSt.Connected {
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

	r.waClient.SetPresenceAvailable(context.Background(), false)
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

	r.waClient.SetPresenceAvailable(context.Background(), false)
	r.broadcast("runner_status", stSnap)
	r.broadcast("human_behavior", telSnap)
	return nil
}

func (r *Runner) runQueueWorker(ctx context.Context, sheet string, queue []int, isBatch bool) {
	prevQueueDelayMs := 0
	prevBreakKind := ""

	for idx, rowNo := range queue {
		if ctx.Err() != nil {
			return
		}

		settings := r.cfg.GetSettings()

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

		hasMore := isBatch && idx < len(queue)-1
		if !hasMore {
			break
		}

		// Determine post-question delay: Long-Rest > Micro-Break > Distraction Spike > Normal Jitter
		r.mu.Lock()
		sinceBreak := r.telemetry.SinceLastBreak
		breakTarget := r.telemetry.NextBreakTarget
		sinceLong := r.telemetry.SinceLastLongRest
		longTarget := r.telemetry.NextLongRestTarget
		r.mu.Unlock()

		latest := r.cfg.GetSettings()
		nextRowNo := queue[idx+1]

		if sinceLong >= longTarget {
			restMs := randRange(latest.LongRestMinMs, latest.LongRestMaxMs)
			nextLong := randRange(latest.LongRestEveryMin, latest.LongRestEveryMax)
			nextMicro := randRange(latest.CooldownEveryMin, latest.CooldownEveryMax)

			r.waClient.SetPresenceAvailable(ctx, false)
			r.onTelemetryHook(
				"long_rest",
				fmt.Sprintf("Istirahat Panjang (Long-Rest) setelah %d soal selama %.1f menit agar akun super aman...", sinceLong, float64(restMs)/60000),
				"unavailable",
				restMs,
				nextRowNo,
				func(t *model.HumanTelemetry) {
					t.TotalLongRestsTaken++
					t.LastCooldownMs = restMs
					t.SinceLastLongRest = 0
					t.NextLongRestTarget = nextLong
					t.SinceLastBreak = 0
					t.NextBreakTarget = nextMicro
				},
			)
			prevQueueDelayMs = restMs
			prevBreakKind = "long_rest"
			if !sleepCancelable(ctx, time.Duration(restMs)*time.Millisecond) {
				return
			}
		} else if sinceBreak >= breakTarget {
			cooldownMs := randRange(latest.CooldownMinMs, latest.CooldownMaxMs)
			nextTarget := randRange(latest.CooldownEveryMin, latest.CooldownEveryMax)

			r.waClient.SetPresenceAvailable(ctx, false)
			r.onTelemetryHook(
				"micro_break",
				fmt.Sprintf("Istirahat Manusiawi (Micro-Break) setelah %d soal selama %.0f dtk...", sinceBreak, float64(cooldownMs)/1000),
				"unavailable",
				cooldownMs,
				nextRowNo,
				func(t *model.HumanTelemetry) {
					t.TotalBreaksTaken++
					t.LastCooldownMs = cooldownMs
					t.SinceLastBreak = 0
					t.NextBreakTarget = nextTarget
				},
			)
			prevQueueDelayMs = cooldownMs
			prevBreakKind = "micro_break"
			if !sleepCancelable(ctx, time.Duration(cooldownMs)*time.Millisecond) {
				return
			}
		} else {
			isSpike := latest.DistractionSpikePct > 0 && rand.Intn(100) < latest.DistractionSpikePct
			var interDelayMs int
			var label string
			if isSpike {
				interDelayMs = randRange(latest.DistractionMinMs, latest.DistractionMaxMs)
				prevBreakKind = "spike"
				label = fmt.Sprintf("Jeda alami agak panjang (cek catatan Excel) selama %.1f dtk sebelum Baris #%d...", float64(interDelayMs)/1000, nextRowNo)
			} else {
				interDelayMs = randRange(latest.DelayMinMs, latest.DelayMaxMs)
				prevBreakKind = "normal"
				label = fmt.Sprintf("Jeda santai antar soal selama %.1f dtk sebelum lanjut Baris #%d...", float64(interDelayMs)/1000, nextRowNo)
			}
			prevQueueDelayMs = interDelayMs

			r.onTelemetryHook(
				"inter_question_delay",
				label,
				"available",
				interDelayMs,
				nextRowNo,
				func(t *model.HumanTelemetry) {
					t.LastInterDelayMs = interDelayMs
				},
			)
			if !sleepCancelable(ctx, time.Duration(interDelayMs)*time.Millisecond) {
				return
			}
		}
	}

	r.mu.Lock()
	if r.status.State == "running" {
		r.status.State = "idle"
		r.status.CurrentRowNo = 0
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

	r.waClient.SetPresenceAvailable(context.Background(), false)
	r.broadcast("runner_status", stSnap)
	r.broadcast("human_behavior", telSnap)
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
	actualReply, mediaFiles, metrics, askErr := r.waClient.AskBotHumanized(
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
			r.tripCircuitBreaker(rowNo, askErr.Error())
			return false
		}
		r.mu.Lock()
		r.status.LastError = askErr.Error()
		r.appendLogLocked("error", rowNo, fmt.Sprintf("Kendala pada %s: %v", rowLabel, askErr))
		r.mu.Unlock()
		return true
	}

	metrics.QueueDelayMs = prevQueueDelayMs
	metrics.BreakKind = prevBreakKind

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
	actualReply, mediaFiles, metrics, askErr := r.waClient.AskBotHumanized(
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
			r.tripCircuitBreaker(rowNo, askErr.Error())
			return false
		}
		r.mu.Lock()
		r.status.LastError = askErr.Error()
		r.appendLogLocked("error", rowNo, fmt.Sprintf("Kendala pada %s: %v", rowLabel, askErr))
		r.mu.Unlock()
		return true
	}

	metrics.QueueDelayMs = prevQueueDelayMs
	metrics.BreakKind = prevBreakKind

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
	actualReply, mediaFiles, metrics, askErr := r.waClient.AskBotHumanized(
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
			r.tripCircuitBreaker(rowNo, askErr.Error())
			return false
		}
		r.mu.Lock()
		r.status.LastError = askErr.Error()
		r.appendLogLocked("error", rowNo, fmt.Sprintf("Kendala pada %s: %v", rowLabel, askErr))
		r.mu.Unlock()
		return true
	}

	metrics.QueueDelayMs = prevQueueDelayMs
	metrics.BreakKind = prevBreakKind

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
