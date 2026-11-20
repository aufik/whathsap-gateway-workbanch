package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"whatshap-gateway/internal/ai"
	"whatshap-gateway/internal/auth"
	"whatshap-gateway/internal/config"
	"whatshap-gateway/internal/database"
	"whatshap-gateway/internal/excel"
	"whatshap-gateway/internal/model"
	"whatshap-gateway/internal/runner"
	"whatshap-gateway/internal/tenants"
	"whatshap-gateway/internal/wa"
)

type AuthWiring struct {
	Handler   *auth.Handler
	Manager   *auth.Manager
	DB        *database.Store
	PublicFS  fs.FS
	Workbooks *tenants.Manager
	WAPool    *wa.Manager
	Runners   *runner.Manager
}

type Server struct {
	cfg       *config.AppConfig
	store     *excel.Store
	aiEngine  *ai.Engine
	waClient  *wa.Client
	runner    *runner.Runner
	runners   *runner.Manager
	broker    *SSEBroker
	publicFS  fs.FS
	auth      AuthWiring
	workbooks *tenants.Manager
	waPool    *wa.Manager
	livechat  *livechatHandlers
}

func NewServer(
	cfg *config.AppConfig,
	store *excel.Store,
	aiEngine *ai.Engine,
	waClient *wa.Client,
	run *runner.Runner,
	broker *SSEBroker,
	publicFS fs.FS,
	authWiring AuthWiring,
) *Server {
	return &Server{
		cfg:       cfg,
		store:     store,
		aiEngine:  aiEngine,
		waClient:  waClient,
		runner:    run,
		runners:   authWiring.Runners,
		broker:    broker,
		publicFS:  publicFS,
		auth:      authWiring,
		workbooks: authWiring.Workbooks,
		waPool:    authWiring.WAPool,
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Public auth endpoints
	if s.auth.Handler != nil {
		mux.HandleFunc("POST /api/auth/login", s.auth.Handler.HandleLogin)
		mux.HandleFunc("POST /api/auth/logout", s.auth.Handler.HandleLogout)
	}

staticFS, err := fs.Sub(s.publicFS, ".")
	if err != nil {
		staticFS = s.publicFS
	}
	staticFiles := http.FileServer(http.FS(staticFS))

	// Public static assets (login page, styles, app.js)
	staticHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/media/") {
			http.NotFound(w, r)
			return
		}
		// Protect dashboard shell: require session, redirect to /login.html when absent.
		if s.auth.Manager != nil {
			isPublicAsset := r.URL.Path == "/login.html" || r.URL.Path == "/app.js" ||
				r.URL.Path == "/styles.css" || r.URL.Path == "/auth.css"
			if !isPublicAsset {
				if _, err := s.auth.Manager.SessionFromRequest(r.Context(), r); err != nil {
					http.Redirect(w, r, "/login.html", http.StatusFound)
					return
				}
			}
		}
		staticFiles.ServeHTTP(w, r)
	})
	mux.Handle("/", staticHandler)


	// Protected API & SSE routes. Every handler resolves the caller's own
	// tenant (workbook, settings, WA pool) from the session.
	apiMux := http.NewServeMux()

	tenant := func(h func(context.Context, int64, http.ResponseWriter, *http.Request)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			sess, ok := auth.FromContext(r.Context())
			if !ok {
				writeErr(w, http.StatusUnauthorized, errors.New("unauthorized"))
				return
			}
			h(r.Context(), sess.UserID, w, r)
		}
	}

	apiMux.HandleFunc("GET /api/state", tenant(s.handleGetStateForUser))
	apiMux.Handle("GET /api/events", s.broker)
	apiMux.HandleFunc("POST /api/settings", tenant(s.handleUpdateSettingsForUser))

	apiMux.HandleFunc("POST /api/wa/connect", tenant(s.handleWAConnectForUser))
	apiMux.HandleFunc("POST /api/wa/pair", tenant(s.handleWAPairForUser))
	apiMux.HandleFunc("POST /api/wa/logout", tenant(s.handleWALogoutForUser))

	apiMux.HandleFunc("POST /api/runner/start", tenant(s.handleRunnerStartForUser))
	apiMux.HandleFunc("POST /api/runner/pause", tenant(s.handleRunnerPauseForUser))
	apiMux.HandleFunc("POST /api/runner/resume", tenant(s.handleRunnerResumeForUser))
	apiMux.HandleFunc("POST /api/runner/stop", tenant(s.handleRunnerStopForUser))

	apiMux.HandleFunc("POST /api/rows/{no}/run", tenant(s.handleRowRunForUser))
	apiMux.HandleFunc("POST /api/rows/{no}/generate", tenant(s.handleRowGenerateForUser))
	apiMux.HandleFunc("POST /api/rows/{no}/evaluate", tenant(s.handleRowReevaluateForUser))
	apiMux.HandleFunc("PATCH /api/rows/{no}", tenant(s.handleRowPatchForUser))

	apiMux.HandleFunc("GET /api/auth/me", s.handleCurrentUser)
	apiMux.HandleFunc("GET /api/admin/users", s.handleAdminListUsers)
	apiMux.HandleFunc("POST /api/admin/users", s.handleAdminCreateUser)
	apiMux.HandleFunc("POST /api/admin/wa-limit", s.handleSetUserWALimit)

	apiMux.HandleFunc("GET /api/wa/devices", s.handleListDevices)
	apiMux.HandleFunc("POST /api/wa/devices", s.handleCreateDevice)
	apiMux.HandleFunc("DELETE /api/wa/devices/{uuid}", s.handleDeleteDevice)

	apiMux.HandleFunc("POST /api/oob/generate", tenant(s.handleOOBGenerateForUser))
	apiMux.HandleFunc("POST /api/oob/rows", tenant(s.handleOOBCreateForUser))
	apiMux.HandleFunc("POST /api/oob/rows/{no}/run", tenant(s.handleOOBRowRunForUser))
	apiMux.HandleFunc("POST /api/oob/rows/{no}/evaluate", tenant(s.handleOOBRowEvaluateForUser))
	apiMux.HandleFunc("PATCH /api/oob/rows/{no}", tenant(s.handleOOBRowPatchForUser))
	apiMux.HandleFunc("DELETE /api/oob/rows/{no}", tenant(s.handleOOBRowDeleteForUser))
	apiMux.HandleFunc("POST /api/oob/delete-batch", tenant(s.handleOOBDeleteBatchForUser))
	apiMux.HandleFunc("POST /api/oob/clear-all", tenant(s.handleOOBClearAllForUser))

	apiMux.HandleFunc("POST /api/stress/generate", tenant(s.handleStressGenerateForUser))
	apiMux.HandleFunc("POST /api/stress/rows", tenant(s.handleStressCreateForUser))
	apiMux.HandleFunc("POST /api/stress/rows/{no}/run", tenant(s.handleStressRowRunForUser))
	apiMux.HandleFunc("POST /api/stress/rows/{no}/evaluate", tenant(s.handleStressRowEvaluateForUser))
	apiMux.HandleFunc("PATCH /api/stress/rows/{no}", tenant(s.handleStressRowPatchForUser))
	apiMux.HandleFunc("DELETE /api/stress/rows/{no}", tenant(s.handleStressRowDeleteForUser))
	apiMux.HandleFunc("POST /api/stress/delete-batch", tenant(s.handleStressDeleteBatchForUser))
	apiMux.HandleFunc("POST /api/stress/clear-all", tenant(s.handleStressClearAllForUser))

	apiMux.HandleFunc("POST /api/llm/test", tenant(s.handleLLMTestForUser))
	apiMux.HandleFunc("POST /api/settings/reload-env", tenant(s.handleReloadEnvForUser))
	apiMux.HandleFunc("GET /api/excel/download", tenant(s.handleExcelDownloadForUser))
	apiMux.HandleFunc("POST /api/excel/upload", tenant(s.handleExcelUploadForUser))
	// Live Chat Emulator (modul terpisah, schema livechat.*). Registered only
	// when the manager is attached so the rest of the app keeps working without it.
	if s.livechat != nil {
		apiMux.HandleFunc("GET /api/livechat/devices", s.livechat.handleDevices)
		apiMux.HandleFunc("GET /api/livechat/preview", s.livechat.handlePreview)
		apiMux.HandleFunc("GET /api/livechat/sessions", s.livechat.handleListSessions)
		apiMux.HandleFunc("POST /api/livechat/sessions", s.livechat.handleStartSession)
		apiMux.HandleFunc("GET /api/livechat/sessions/{id}", s.livechat.handleSessionDetail)
		apiMux.HandleFunc("POST /api/livechat/sessions/{id}/stop", s.livechat.handleStopSession)
		apiMux.HandleFunc("DELETE /api/livechat/sessions/{id}", s.livechat.handleDeleteSession)
		apiMux.HandleFunc("GET /api/livechat/assets", s.livechat.handleListAssets)
		apiMux.HandleFunc("POST /api/livechat/assets", s.livechat.handleUploadAsset)
		apiMux.HandleFunc("DELETE /api/livechat/assets/{id}", s.livechat.handleDeleteAsset)
		apiMux.HandleFunc("GET /api/livechat/assets/{id}/preview", s.livechat.handleAssetPreview)
		apiMux.HandleFunc("GET /api/livechat/events", s.livechat.handleEvents)
	}

	apiMux.Handle("GET /media/", http.HandlerFunc(s.handleMedia))

	if s.auth.Manager != nil {
		mux.Handle("/api/", s.auth.Manager.Middleware(apiMux))
		mux.Handle("/media/", s.auth.Manager.Middleware(apiMux))
	} else {
		mux.Handle("/api/", apiMux)
		mux.Handle("/media/", apiMux)
	}

	return mux
}

// tenantStore resolves the caller's own workbook. It is the single place that
// guarantees data isolation between accounts.
func (s *Server) tenantStore(ctx context.Context, userID int64) *excel.Store {
	if s.workbooks == nil {
		return s.store
	}
	store, err := s.workbooks.Load(ctx, userID)
	if err != nil || store == nil {
		return excel.NewEmptyStore()
	}
	return store
}

// userDeviceID is the media scope for a tenant (their user id).
func (s *Server) userDeviceID(userID int64) string {
	return strconv.FormatInt(userID, 10)
}

func (s *Server) handleGetStateForUser(ctx context.Context, userID int64, w http.ResponseWriter, r *http.Request) {
	store := s.tenantStore(ctx, userID)
	rows, sum := store.Snapshot()
	oobRows, oobSum := store.OutOfBankSnapshot()
	stressRows, stressSum := store.StressTestSnapshot()
	var waStatus model.WAStatus
	if s.waClient != nil {
		waStatus = s.waClient.Status()
	} else if s.waPool != nil {
		// Summarise device pool for this user
		pool := s.waPool.Pool(userID)
		conn := pool.ConnectedCount()
		devices := pool.Devices()
		if len(devices) > 0 {
			waStatus = model.WAStatus{
				Connected:  conn > 0,
				LoggedIn:   conn > 0,
				AccountJID: fmt.Sprintf("%d perangkat", len(devices)),
			}
		}
	}

	userRunner := s.tenantRunner(ctx, userID)
	snap := model.AppStateSnapshot{
		Settings:          s.userSettings(ctx, userID),
		AI:                s.cfg.AIStatus(),
		WA:                waStatus,
		Runner:            userRunner.Status(),
		Telemetry:         userRunner.Telemetry(),
		ChannelTelemetry:  userRunner.ChannelTelemetry(),
		Summary:           sum,
		Rows:              rows,
		OutOfBankSummary:  oobSum,
		OutOfBankRows:     oobRows,
		StressTestSummary: stressSum,
		StressTestRows:    stressRows,
	}
	if sess, ok := auth.FromContext(r.Context()); ok {
		snap.CurrentUser = &model.CurrentUser{ID: sess.UserID, Username: sess.Username, FullName: sess.FullName, Role: sess.Role}
	}
	writeJSON(w, http.StatusOK, snap)
}

func (s *Server) handleWAConnectForUser(ctx context.Context, userID int64, w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeviceUUID string `json:"deviceUuid"`
	}
	_ = decodeJSON(r, &req)
	if req.DeviceUUID == "" {
		writeErr(w, http.StatusBadRequest, errors.New("deviceUuid wajib dikirim; tambahkan nomor WA terlebih dahulu"))
		return
	}

	client, err := s.waPool.AttachDevice(ctx, userID, req.DeviceUUID, func(st model.WAStatus) {
		s.broker.Broadcast(userID, "wa_status", map[string]any{"deviceUuid": req.DeviceUUID, "status": st})
		if s.auth.DB != nil {
			_ = s.auth.DB.SetDeviceConnectedByUUID(context.Background(), userID, req.DeviceUUID, st.Connected && st.LoggedIn, st.AccountJID)
		}
	})
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}

	connectCtx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := client.Connect(connectCtx); err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, client.Status())
}

func (s *Server) handleWAPairForUser(ctx context.Context, userID int64, w http.ResponseWriter, r *http.Request) {
	var req struct {
		Phone      string `json:"phone"`
		DeviceUUID string `json:"deviceUuid"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.DeviceUUID == "" {
		writeErr(w, http.StatusBadRequest, errors.New("deviceUuid wajib dikirim"))
		return
	}

	client, err := s.waPool.AttachDevice(ctx, userID, req.DeviceUUID, func(st model.WAStatus) {
		s.broker.Broadcast(userID, "wa_status", map[string]any{"deviceUuid": req.DeviceUUID, "status": st})
		if s.auth.DB != nil {
			_ = s.auth.DB.SetDeviceConnectedByUUID(context.Background(), userID, req.DeviceUUID, st.Connected && st.LoggedIn, st.AccountJID)
		}
	})
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}

	pairCtx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	code, err := client.RequestPairingCode(pairCtx, req.Phone)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"pairingCode": code,
		"status":      client.Status(),
	})
}

func (s *Server) handleWALogoutForUser(ctx context.Context, userID int64, w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeviceUUID string `json:"deviceUuid"`
	}
	_ = decodeJSON(r, &req)
	if req.DeviceUUID == "" {
		writeErr(w, http.StatusBadRequest, errors.New("deviceUuid wajib dikirim"))
		return
	}

	_ = s.tenantRunner(ctx, userID).Stop()
	client, err := s.waPool.Pool(userID).Resolve(req.DeviceUUID)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	logoutCtx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := client.Logout(logoutCtx); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, client.Status())
}

func (s *Server) handleRunnerStartForUser(ctx context.Context, userID int64, w http.ResponseWriter, r *http.Request) {
	run := s.tenantRunner(ctx, userID)
	var req struct {
		TargetSheet string `json:"targetSheet"`
		ForceAll    bool   `json:"forceAll"`
		Limit       int    `json:"limit"`
		RangeFrom   int    `json:"rangeFrom"`
		RangeTo     int    `json:"rangeTo"`
	}
	_ = decodeJSON(r, &req)
	if err := run.StartBatch(req.TargetSheet, req.ForceAll, req.Limit, req.RangeFrom, req.RangeTo); err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, runner.ErrAlreadyRunning) {
			code = http.StatusConflict
		}
		writeErr(w, code, err)
		return
	}
	writeJSON(w, http.StatusOK, run.Status())
}

func (s *Server) handleRunnerPauseForUser(ctx context.Context, userID int64, w http.ResponseWriter, r *http.Request) {
	run := s.tenantRunner(ctx, userID)
	if err := run.Pause(); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, run.Status())
}

func (s *Server) handleRunnerResumeForUser(ctx context.Context, userID int64, w http.ResponseWriter, r *http.Request) {
	run := s.tenantRunner(ctx, userID)
	var req struct {
		Limit int `json:"limit"`
	}
	_ = decodeJSON(r, &req)
	if err := run.Resume(req.Limit); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, run.Status())
}

func (s *Server) handleRunnerStopForUser(ctx context.Context, userID int64, w http.ResponseWriter, r *http.Request) {
	run := s.tenantRunner(ctx, userID)
	if err := run.Stop(); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, run.Status())
}

func (s *Server) handleRowRunForUser(ctx context.Context, userID int64, w http.ResponseWriter, r *http.Request) {
	run := s.tenantRunner(ctx, userID)
	no, err := parseRowNo(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := run.RunSingleRow(model.SheetTargetBank, no); err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, runner.ErrAlreadyRunning) {
			code = http.StatusConflict
		}
		writeErr(w, code, err)
		return
	}
	writeJSON(w, http.StatusOK, run.Status())
}

func (s *Server) handleRowGenerateForUser(ctx context.Context, userID int64, w http.ResponseWriter, r *http.Request) {
	no, err := parseRowNo(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	row, ok := s.tenantStore(ctx, userID).GetRow(no)
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
		updated, sum, err := s.tenantStore(ctx, userID).UpdateRow(no, model.RowPatch{QuestionInput: &generated})
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		s.broker.Broadcast(userID, "row_updated", updated)
		s.broker.Broadcast(userID, "summary_updated", sum)
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

func (s *Server) handleRowReevaluateForUser(ctx context.Context, userID int64, w http.ResponseWriter, r *http.Request) {
	no, err := parseRowNo(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	row, ok := s.tenantStore(ctx, userID).GetRow(no)
	if !ok {
		writeErr(w, http.StatusNotFound, fmt.Errorf("baris #%d tidak ditemukan", no))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 22*time.Second)
	defer cancel()
	status, notes := s.aiEngine.EvaluateReply(ctx, row, row.ActualReply)

	updated, sum, err := s.tenantStore(ctx, userID).UpdateRow(no, model.RowPatch{
		ResultStatus: &status,
		Notes:        &notes,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.broker.Broadcast(userID, "row_updated", updated)
	s.broker.Broadcast(userID, "summary_updated", sum)
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleRowPatchForUser(ctx context.Context, userID int64, w http.ResponseWriter, r *http.Request) {
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
	updated, sum, err := s.tenantStore(ctx, userID).UpdateRow(no, patch)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.broker.Broadcast(userID, "row_updated", updated)
	s.broker.Broadcast(userID, "summary_updated", sum)
	s.persistAfterWrite(userID)
	writeJSON(w, http.StatusOK, map[string]any{
		"row":     updated,
		"summary": sum,
	})
}

func (s *Server) handleOOBGenerateForUser(ctx context.Context, userID int64, w http.ResponseWriter, r *http.Request) {
	var req ai.OutOfBankGenRequest
	_ = decodeJSON(r, &req)
	if req.Count <= 0 {
		req.Count = 20
	}
	if strings.TrimSpace(req.TargetYear) == "" {
		req.TargetYear = "2026"
	}

	bankRows, _ := s.tenantStore(ctx, userID).Snapshot()
	ctx, cancel := context.WithTimeout(r.Context(), 18*time.Second)
	defer cancel()

	candidates := s.aiEngine.GenerateOutOfBankProbes(ctx, bankRows, req)
	rows, sum, added, err := s.tenantStore(ctx, userID).AppendOutOfBankRows(candidates)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if added == 0 {
		writeErr(w, http.StatusConflict, errors.New("semua variasi yang dihasilkan sudah ada di sheet Uji Diluar Bank; coba ubah prompt/kategori atau gunakan teks lain"))
		return
	}
	s.broker.Broadcast(userID, "oob_reloaded", map[string]any{
		"rows":    rows,
		"summary": sum,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"rows":    rows,
		"summary": sum,
		"added":   added,
	})
}

func (s *Server) handleOOBCreateForUser(ctx context.Context, userID int64, w http.ResponseWriter, r *http.Request) {
	var item model.OutOfBankRow
	if err := decodeJSON(r, &item); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(item.ProbeQuestion) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("kalimat pertanyaan uji diluar bank wajib diisi"))
		return
	}
	if strings.TrimSpace(item.SourceRef) == "" {
		item.SourceRef = "Input Manual Penguji"
	}
	if strings.TrimSpace(item.ScenarioType) == "" {
		item.ScenarioType = "Pertanyaan Kustom Diluar Bank"
	}
	if strings.TrimSpace(item.ExpectedBehavior) == "" {
		item.ExpectedBehavior = "Bot wajib menjawab jujur sesuai cakupan data (tidak boleh berhalusinasi / mengarang fakta)."
	}

	rows, sum, added, err := s.tenantStore(ctx, userID).AppendOutOfBankRows([]model.OutOfBankRow{item})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if added == 0 {
		writeErr(w, http.StatusConflict, errors.New("pertanyaan tersebut sudah ada di sheet Uji Diluar Bank"))
		return
	}
	s.broker.Broadcast(userID, "oob_reloaded", map[string]any{
		"rows":    rows,
		"summary": sum,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"rows":    rows,
		"summary": sum,
		"added":   added,
	})
}

func (s *Server) handleOOBRowRunForUser(ctx context.Context, userID int64, w http.ResponseWriter, r *http.Request) {
	run := s.tenantRunner(ctx, userID)
	no, err := parseRowNo(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := run.RunSingleRow(model.SheetTargetOutOfBank, no); err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, runner.ErrAlreadyRunning) {
			code = http.StatusConflict
		}
		writeErr(w, code, err)
		return
	}
	writeJSON(w, http.StatusOK, run.Status())
}

func (s *Server) handleOOBRowEvaluateForUser(ctx context.Context, userID int64, w http.ResponseWriter, r *http.Request) {
	no, err := parseRowNo(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	row, ok := s.tenantStore(ctx, userID).GetOutOfBankRow(no)
	if !ok {
		writeErr(w, http.StatusNotFound, fmt.Errorf("baris Uji Diluar Bank #%d tidak ditemukan", no))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 22*time.Second)
	defer cancel()
	status, notes := s.aiEngine.EvaluateOutOfBankReply(ctx, row, row.ActualReply)

	updated, sum, err := s.tenantStore(ctx, userID).UpdateOutOfBankRow(no, model.OutOfBankPatch{
		ResultStatus: &status,
		Notes:        &notes,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.broker.Broadcast(userID, "oob_row_updated", updated)
	s.broker.Broadcast(userID, "oob_summary_updated", sum)
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleOOBRowPatchForUser(ctx context.Context, userID int64, w http.ResponseWriter, r *http.Request) {
	no, err := parseRowNo(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	var patch model.OutOfBankPatch
	if err := decodeJSON(r, &patch); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	updated, sum, err := s.tenantStore(ctx, userID).UpdateOutOfBankRow(no, patch)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.broker.Broadcast(userID, "oob_row_updated", updated)
	s.broker.Broadcast(userID, "oob_summary_updated", sum)
	writeJSON(w, http.StatusOK, map[string]any{
		"row":     updated,
		"summary": sum,
	})
}

func (s *Server) handleOOBRowDeleteForUser(ctx context.Context, userID int64, w http.ResponseWriter, r *http.Request) {
	if s.tenantRunner(ctx, userID).Status().State == "running" {
		writeErr(w, http.StatusConflict, errors.New("jeda atau hentikan pengujian terlebih dahulu sebelum menghapus baris"))
		return
	}
	no, err := parseRowNo(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	rows, sum, err := s.tenantStore(ctx, userID).DeleteOutOfBankRow(no)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.broker.Broadcast(userID, "oob_reloaded", map[string]any{
		"rows":    rows,
		"summary": sum,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"rows":    rows,
		"summary": sum,
	})
}

func (s *Server) handleOOBDeleteBatchForUser(ctx context.Context, userID int64, w http.ResponseWriter, r *http.Request) {
	if s.tenantRunner(ctx, userID).Status().State == "running" {
		writeErr(w, http.StatusConflict, errors.New("jeda atau hentikan pengujian terlebih dahulu sebelum menghapus baris"))
		return
	}
	var req struct {
		Numbers []int `json:"numbers"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	rows, sum, err := s.tenantStore(ctx, userID).DeleteOutOfBankBatch(req.Numbers)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.broker.Broadcast(userID, "oob_reloaded", map[string]any{
		"rows":    rows,
		"summary": sum,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"rows":    rows,
		"summary": sum,
	})
}

func (s *Server) handleOOBClearAllForUser(ctx context.Context, userID int64, w http.ResponseWriter, r *http.Request) {
	if s.tenantRunner(ctx, userID).Status().State == "running" {
		writeErr(w, http.StatusConflict, errors.New("jeda atau hentikan pengujian terlebih dahulu sebelum mengosongkan baris"))
		return
	}
	rows, sum, err := s.tenantStore(ctx, userID).ClearOutOfBank()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.broker.Broadcast(userID, "oob_reloaded", map[string]any{
		"rows":    rows,
		"summary": sum,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"rows":    rows,
		"summary": sum,
	})
}

func (s *Server) handleStressGenerateForUser(ctx context.Context, userID int64, w http.ResponseWriter, r *http.Request) {
	var req ai.StressGenRequest
	_ = decodeJSON(r, &req)
	if req.Count <= 0 {
		req.Count = 15
	}

	bankRows, _ := s.tenantStore(ctx, userID).Snapshot()
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	candidates := s.aiEngine.GenerateStressProbes(ctx, bankRows, req)
	rows, sum, added, err := s.tenantStore(ctx, userID).AppendStressTestRows(candidates)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if added == 0 {
		writeErr(w, http.StatusConflict, errors.New("semua skenario yang dihasilkan sudah ada di sheet Uji Stress AI; coba ubah prompt/kategori"))
		return
	}
	s.broker.Broadcast(userID, "stress_reloaded", map[string]any{
		"rows":    rows,
		"summary": sum,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"rows":    rows,
		"summary": sum,
		"added":   added,
	})
}

func (s *Server) handleStressCreateForUser(ctx context.Context, userID int64, w http.ResponseWriter, r *http.Request) {
	var item model.StressTestRow
	if err := decodeJSON(r, &item); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(item.UserPromptQ) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("kalimat pertanyaan uji stress/curhat wajib diisi"))
		return
	}
	if strings.TrimSpace(item.StressCategory) == "" {
		item.StressCategory = "Stress Test Manual"
	}
	if strings.TrimSpace(item.ExpectedBehavior) == "" {
		item.ExpectedBehavior = "Bot menolak sopan / meluruskan batasan layanan / tidak larut dalam curhat."
	}

	rows, sum, added, err := s.tenantStore(ctx, userID).AppendStressTestRows([]model.StressTestRow{item})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if added == 0 {
		writeErr(w, http.StatusConflict, errors.New("pertanyaan stress tersebut sudah ada di sheet Uji Stress AI"))
		return
	}
	s.broker.Broadcast(userID, "stress_reloaded", map[string]any{
		"rows":    rows,
		"summary": sum,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"rows":    rows,
		"summary": sum,
		"added":   added,
	})
}

func (s *Server) handleStressRowRunForUser(ctx context.Context, userID int64, w http.ResponseWriter, r *http.Request) {
	run := s.tenantRunner(ctx, userID)
	no, err := parseRowNo(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := run.RunSingleRow(model.SheetTargetStress, no); err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, runner.ErrAlreadyRunning) {
			code = http.StatusConflict
		}
		writeErr(w, code, err)
		return
	}
	writeJSON(w, http.StatusOK, run.Status())
}

func (s *Server) handleStressRowEvaluateForUser(ctx context.Context, userID int64, w http.ResponseWriter, r *http.Request) {
	no, err := parseRowNo(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	row, ok := s.tenantStore(ctx, userID).GetStressTestRow(no)
	if !ok {
		writeErr(w, http.StatusNotFound, fmt.Errorf("baris Uji Stress AI #%d tidak ditemukan", no))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 22*time.Second)
	defer cancel()
	status, notes, score, hal := s.aiEngine.EvaluateStressReply(ctx, row, row.ActualReply)

	updated, sum, err := s.tenantStore(ctx, userID).UpdateStressTestRow(no, model.StressTestPatch{
		ResultStatus:     &status,
		Notes:            &notes,
		PolitenessScore:  &score,
		HallucinationLvl: &hal,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.broker.Broadcast(userID, "stress_row_updated", updated)
	s.broker.Broadcast(userID, "stress_summary_updated", sum)
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleStressRowPatchForUser(ctx context.Context, userID int64, w http.ResponseWriter, r *http.Request) {
	no, err := parseRowNo(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	var patch model.StressTestPatch
	if err := decodeJSON(r, &patch); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	updated, sum, err := s.tenantStore(ctx, userID).UpdateStressTestRow(no, patch)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.broker.Broadcast(userID, "stress_row_updated", updated)
	s.broker.Broadcast(userID, "stress_summary_updated", sum)
	writeJSON(w, http.StatusOK, map[string]any{
		"row":     updated,
		"summary": sum,
	})
}

func (s *Server) handleStressRowDeleteForUser(ctx context.Context, userID int64, w http.ResponseWriter, r *http.Request) {
	if s.tenantRunner(ctx, userID).Status().State == "running" {
		writeErr(w, http.StatusConflict, errors.New("jeda atau hentikan pengujian terlebih dahulu sebelum menghapus baris"))
		return
	}
	no, err := parseRowNo(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	rows, sum, err := s.tenantStore(ctx, userID).DeleteStressTestRow(no)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.broker.Broadcast(userID, "stress_reloaded", map[string]any{
		"rows":    rows,
		"summary": sum,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"rows":    rows,
		"summary": sum,
	})
}

func (s *Server) handleStressDeleteBatchForUser(ctx context.Context, userID int64, w http.ResponseWriter, r *http.Request) {
	if s.tenantRunner(ctx, userID).Status().State == "running" {
		writeErr(w, http.StatusConflict, errors.New("jeda atau hentikan pengujian terlebih dahulu sebelum menghapus baris"))
		return
	}
	var req struct {
		Numbers []int `json:"numbers"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	rows, sum, err := s.tenantStore(ctx, userID).DeleteStressTestBatch(req.Numbers)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.broker.Broadcast(userID, "stress_reloaded", map[string]any{
		"rows":    rows,
		"summary": sum,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"rows":    rows,
		"summary": sum,
	})
}

func (s *Server) handleStressClearAllForUser(ctx context.Context, userID int64, w http.ResponseWriter, r *http.Request) {
	if s.tenantRunner(ctx, userID).Status().State == "running" {
		writeErr(w, http.StatusConflict, errors.New("jeda atau hentikan pengujian terlebih dahulu sebelum mengosongkan baris"))
		return
	}
	rows, sum, err := s.tenantStore(ctx, userID).ClearStressTest()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.broker.Broadcast(userID, "stress_reloaded", map[string]any{
		"rows":    rows,
		"summary": sum,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"rows":    rows,
		"summary": sum,
	})
}

func (s *Server) handleLLMTestForUser(ctx context.Context, userID int64, w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()

	result, err := s.aiEngine.TestConnection(ctx)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"message": result,
		"ai":      s.cfg.AIStatus(),
	})
}

func (s *Server) handleReloadEnvForUser(ctx context.Context, userID int64, w http.ResponseWriter, r *http.Request) {
	if err := s.cfg.ReloadFromDisk(); err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("gagal membaca ulang .env: %w", err))
		return
	}
	aiStatus := s.cfg.AIStatus()
	settings := s.userSettings(ctx, userID)
	s.broker.Broadcast(userID, "ai_status_updated", aiStatus)
	s.broker.Broadcast(userID, "settings_updated", settings)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"ai":       aiStatus,
		"settings": settings,
	})
}
func (s *Server) handleExcelDownloadForUser(ctx context.Context, userID int64, w http.ResponseWriter, r *http.Request) {
	data, fileName, err := s.tenantStore(ctx, userID).ExportBytes()
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

func (s *Server) handleExcelUploadForUser(ctx context.Context, userID int64, w http.ResponseWriter, r *http.Request) {
	userRunner := s.tenantRunner(ctx, userID)
	if userRunner.Status().State == "running" {
		writeErr(w, http.StatusConflict, errors.New("hentikan atau jeda pengujian terlebih dahulu sebelum mengganti file Excel"))
		return
	}
	if err := r.ParseMultipartForm(25 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("ukuran file melebihi batas atau request tidak valid: %v", err))
		return
	}
	file, _, err := r.FormFile("file")
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

	store := s.tenantStore(ctx, userID)
	if _, err := store.ReplaceFromBytes(raw); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	userRunner.SetStore(store)
	s.persistAfterWrite(userID)

	sum, _ := store.Snapshot()
	rows, _ := store.Snapshot()
	oobRows, oobSum := store.OutOfBankSnapshot()
	stressRows, stressSum := store.StressTestSnapshot()
	s.broker.Broadcast(userID, "workbook_reloaded", map[string]any{
		"summary":           sum,
		"rows":              rows,
		"outOfBankSummary":  oobSum,
		"outOfBankRows":     oobRows,
		"stressTestSummary": stressSum,
		"stressTestRows":    stressRows,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"summary":           sum,
		"rows":              rows,
		"outOfBankSummary":  oobSum,
		"outOfBankRows":     oobRows,
		"stressTestSummary": stressSum,
		"stressTestRows":    stressRows,
	})
}

// handleMedia serves a user's downloaded attachments.
// Supports both:
// 1. Scoped URL: /media/{userID}/{file}
// 2. Legacy/flat URL: /media/{file} (served from the user's media dir or global data/downloads fallback)
func (s *Server) handleMedia(w http.ResponseWriter, r *http.Request) {
	sess, ok := auth.FromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/media/")
	parts := strings.SplitN(rest, "/", 2)

	var name string
	if len(parts) == 2 && parts[0] == strconv.FormatInt(sess.UserID, 10) {
		name = filepath.Base(parts[1])
	} else if len(parts) == 1 {
		name = filepath.Base(parts[0])
	} else {
		http.NotFound(w, r)
		return
	}

	if name == "." || name == string(filepath.Separator) || strings.Contains(name, "..") {
		http.NotFound(w, r)
		return
	}

	// 1. Try tenant's own download directory first
	userMediaPath := filepath.Join(s.workbooks.MediaDir(sess.UserID), name)
	if fi, err := os.Stat(userMediaPath); err == nil && !fi.IsDir() {
		http.ServeFile(w, r, userMediaPath)
		return
	}

	// 2. Try global data/downloads fallback (for historical images created before multi-tenant)
	globalMediaPath := filepath.Join("data", "downloads", name)
	if fi, err := os.Stat(globalMediaPath); err == nil && !fi.IsDir() {
		http.ServeFile(w, r, globalMediaPath)
		return
	}

	http.NotFound(w, r)
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
