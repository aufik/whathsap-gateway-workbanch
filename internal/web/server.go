package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"

	"whatshap-gateway/internal/ai"
	"whatshap-gateway/internal/config"
	"whatshap-gateway/internal/excel"
	"whatshap-gateway/internal/model"
	"whatshap-gateway/internal/runner"
	"whatshap-gateway/internal/wa"
)

type Server struct {
	cfg      *config.AppConfig
	store    *excel.Store
	aiEngine *ai.Engine
	waClient *wa.Client
	runner   *runner.Runner
	broker   *SSEBroker
	publicFS fs.FS
}

func NewServer(
	cfg *config.AppConfig,
	store *excel.Store,
	aiEngine *ai.Engine,
	waClient *wa.Client,
	run *runner.Runner,
	broker *SSEBroker,
	publicFS fs.FS,
) *Server {
	return &Server{
		cfg:      cfg,
		store:    store,
		aiEngine: aiEngine,
		waClient: waClient,
		runner:   run,
		broker:   broker,
		publicFS: publicFS,
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/state", s.handleGetState)
	mux.Handle("GET /api/events", s.broker)
	mux.HandleFunc("POST /api/settings", s.handleUpdateSettings)

	mux.HandleFunc("POST /api/wa/connect", s.handleWAConnect)
	mux.HandleFunc("POST /api/wa/pair", s.handleWAPair)
	mux.HandleFunc("POST /api/wa/logout", s.handleWALogout)

	mux.HandleFunc("POST /api/runner/start", s.handleRunnerStart)
	mux.HandleFunc("POST /api/runner/pause", s.handleRunnerPause)
	mux.HandleFunc("POST /api/runner/resume", s.handleRunnerResume)
	mux.HandleFunc("POST /api/runner/stop", s.handleRunnerStop)

	mux.HandleFunc("POST /api/rows/{no}/run", s.handleRowRun)
	mux.HandleFunc("POST /api/rows/{no}/generate", s.handleRowGenerate)
	mux.HandleFunc("POST /api/rows/{no}/evaluate", s.handleRowReevaluate)
	mux.HandleFunc("PATCH /api/rows/{no}", s.handleRowPatch)

	mux.HandleFunc("GET /api/excel/download", s.handleExcelDownload)
	mux.HandleFunc("POST /api/excel/upload", s.handleExcelUpload)

	mux.Handle("/", http.FileServer(http.FS(s.publicFS)))
	return mux
}

func (s *Server) handleGetState(w http.ResponseWriter, r *http.Request) {
	rows, sum := s.store.Snapshot()
	snap := model.AppStateSnapshot{
		Settings:  s.cfg.GetSettings(),
		AI:        s.cfg.AIStatus(),
		WA:        s.waClient.Status(),
		Runner:    s.runner.Status(),
		Telemetry: s.runner.Telemetry(),
		Summary:   sum,
		Rows:      rows,
	}
	writeJSON(w, http.StatusOK, snap)
}

func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var next model.HumanSettings
	if err := decodeJSON(r, &next); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	saved, err := s.cfg.UpdateSettings(next)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.broker.Broadcast("settings_updated", saved)
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) handleWAConnect(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := s.waClient.Connect(ctx); err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, s.waClient.Status())
}

func (s *Server) handleWAPair(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Phone string `json:"phone"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	code, err := s.waClient.RequestPairingCode(ctx, req.Phone)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"pairingCode": code,
		"status":      s.waClient.Status(),
	})
}

func (s *Server) handleWALogout(w http.ResponseWriter, r *http.Request) {
	_ = s.runner.Stop()
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := s.waClient.Logout(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, s.waClient.Status())
}

func (s *Server) handleRunnerStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ForceAll bool `json:"forceAll"`
	}
	_ = decodeJSON(r, &req)
	if err := s.runner.StartBatch(req.ForceAll); err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, runner.ErrAlreadyRunning) {
			code = http.StatusConflict
		}
		writeErr(w, code, err)
		return
	}
	writeJSON(w, http.StatusOK, s.runner.Status())
}

func (s *Server) handleRunnerPause(w http.ResponseWriter, r *http.Request) {
	if err := s.runner.Pause(); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, s.runner.Status())
}

func (s *Server) handleRunnerResume(w http.ResponseWriter, r *http.Request) {
	if err := s.runner.Resume(); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, s.runner.Status())
}

func (s *Server) handleRunnerStop(w http.ResponseWriter, r *http.Request) {
	if err := s.runner.Stop(); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, s.runner.Status())
}

func (s *Server) handleRowRun(w http.ResponseWriter, r *http.Request) {
	no, err := parseRowNo(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.runner.RunSingleRow(no); err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, runner.ErrAlreadyRunning) {
			code = http.StatusConflict
		}
		writeErr(w, code, err)
		return
	}
	writeJSON(w, http.StatusOK, s.runner.Status())
}

func (s *Server) handleRowGenerate(w http.ResponseWriter, r *http.Request) {
	no, err := parseRowNo(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	row, ok := s.store.GetRow(no)
	if !ok {
		writeErr(w, http.StatusNotFound, fmt.Errorf("baris #%d tidak ditemukan", no))
		return
	}

	var req struct {
		Mode string `json:"mode"`
		Save bool   `json:"save"`
	}
	_ = decodeJSON(r, &req)

	mode := s.cfg.GetSettings().QuestionMode
	if strings.TrimSpace(req.Mode) != "" {
		mode = model.QuestionMode(strings.ToLower(strings.TrimSpace(req.Mode)))
	}

	ctx, cancel := context.WithTimeout(r.Context(), 22*time.Second)
	defer cancel()

	generated, err := s.aiEngine.GenerateQuestion(ctx, row, mode, time.Now().UnixNano())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	if req.Save {
		updated, sum, err := s.store.UpdateRow(no, model.RowPatch{QuestionInput: &generated})
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		s.broker.Broadcast("row_updated", updated)
		s.broker.Broadcast("summary_updated", sum)
		writeJSON(w, http.StatusOK, map[string]any{
			"generated": generated,
			"row":       updated,
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"generated": generated,
		"row":       row,
	})
}

func (s *Server) handleRowReevaluate(w http.ResponseWriter, r *http.Request) {
	no, err := parseRowNo(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	row, ok := s.store.GetRow(no)
	if !ok {
		writeErr(w, http.StatusNotFound, fmt.Errorf("baris #%d tidak ditemukan", no))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 22*time.Second)
	defer cancel()
	status, notes := s.aiEngine.EvaluateReply(ctx, row, row.ActualReply)

	updated, sum, err := s.store.UpdateRow(no, model.RowPatch{
		ResultStatus: &status,
		Notes:        &notes,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.broker.Broadcast("row_updated", updated)
	s.broker.Broadcast("summary_updated", sum)
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleRowPatch(w http.ResponseWriter, r *http.Request) {
	no, err := parseRowNo(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	var patch model.RowPatch
	if err := decodeJSON(r, &patch); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	updated, sum, err := s.store.UpdateRow(no, patch)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.broker.Broadcast("row_updated", updated)
	s.broker.Broadcast("summary_updated", sum)
	writeJSON(w, http.StatusOK, map[string]any{
		"row":     updated,
		"summary": sum,
	})
}

func (s *Server) handleExcelDownload(w http.ResponseWriter, r *http.Request) {
	data, fileName, err := s.store.ExportBytes()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", fileName))
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

func (s *Server) handleExcelUpload(w http.ResponseWriter, r *http.Request) {
	if s.runner.Status().State == "running" {
		writeErr(w, http.StatusConflict, errors.New("hentikan atau jeda pengujian terlebih dahulu sebelum mengganti file Excel"))
		return
	}
	if err := r.ParseMultipartForm(25 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("ukuran file melebihi batas atau request tidak valid: %v", err))
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("berkas file Excel tidak ditemukan pada form upload"))
		return
	}
	defer file.Close()

	raw, err := io.ReadAll(io.LimitReader(file, 25<<20))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	sum, err := s.store.ReplaceWorkbook(raw, header.Filename)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.cfg.SetExcelFile(s.store.FilePath())
	rows, _ := s.store.Snapshot()
	s.broker.Broadcast("workbook_reloaded", map[string]any{
		"summary": sum,
		"rows":    rows,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"summary": sum,
		"rows":    rows,
	})
}

func parseRowNo(r *http.Request) (int, error) {
	raw := strings.TrimSpace(r.PathValue("no"))
	no, err := strconv.Atoi(raw)
	if err != nil || no <= 0 {
		return 0, errors.New("nomor baris tidak valid")
	}
	return no, nil
}

func decodeJSON(r *http.Request, dst any) error {
	if r.Body == nil {
		return nil
	}
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 2<<20))
	if err := dec.Decode(dst); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func writeErr(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{
		"error": err.Error(),
	})
}
