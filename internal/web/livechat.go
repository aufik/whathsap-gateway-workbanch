package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"whatshap-gateway/internal/auth"
	"whatshap-gateway/internal/livechat"
	"whatshap-gateway/internal/model"
)

// livechatHandlers bundles the routes the server exposes under /api/livechat.
// Every method receives the caller's user id from the session middleware; no
// handler ever accepts a user id from the request body or query string.
type livechatHandlers struct {
	server   *Server
	store    *livechat.Store
	assets   *livechat.AssetService
	manager  *livechat.Manager
	exporter *livechat.Exporter
}

// hasDB tells handlers to skip DB-dependent helpers when the server was built
// without a real database store (common in unit tests).
func (h *livechatHandlers) hasDB() bool {
	return h != nil && h.server != nil && h.server.auth.DB != nil && h.server.auth.DB.DB() != nil
}

func (h *livechatHandlers) requireUser(w http.ResponseWriter, r *http.Request) (int64, bool) {
	sess, ok := auth.FromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, errors.New("unauthorized"))
		return 0, false
	}
	if h == nil || h.store == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("live chat belum tersedia"))
		return 0, false
	}
	return sess.UserID, true
}

// GET /api/livechat/devices → connected devices only (for the form picker).
func (h *livechatHandlers) handleDevices(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	if !h.hasDB() {
		writeErr(w, http.StatusServiceUnavailable, errors.New("database belum terhubung"))
		return
	}
	devices, err := h.server.auth.DB.ListWADevices(r.Context(), userID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	type item struct {
		DeviceUUID  string `json:"deviceUuid"`
		PhoneNumber string `json:"phoneNumber"`
		Connected   bool   `json:"connected"`
	}
	out := make([]item, 0, len(devices))
	for _, d := range devices {
		connected := d.IsConnected
		if h.server.waPool != nil {
			if c, ok := h.server.waPool.Pool(userID).Get(d.DeviceUUID); ok && c != nil {
				st := c.Status()
				connected = st.Connected && st.LoggedIn
			}
		}
		out = append(out, item{DeviceUUID: d.DeviceUUID, PhoneNumber: d.PhoneNumber, Connected: connected})
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": out})
}

// GET /api/livechat/preview → random preview (nama/tgl/alamat/maps) before the
// form opens. The values are *suggestions*; the tester may edit them.
func (h *livechatHandlers) handlePreview(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireUser(w, r); !ok {
		return
	}
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	writeJSON(w, http.StatusOK, livechat.GenerateRandomPreview(rng, time.Now()))
}

// GET /api/livechat/sessions
func (h *livechatHandlers) handleListSessions(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	pageSize, _ := strconv.Atoi(r.URL.Query().Get("pageSize"))
	if pageSize <= 0 {
		pageSize, _ = strconv.Atoi(r.URL.Query().Get("limit"))
	}
	filter := livechat.SessionFilter{
		Page:     page,
		PageSize: pageSize,
		Status:   r.URL.Query().Get("status"),
		Mode:     r.URL.Query().Get("mode"),
		Query:    r.URL.Query().Get("q"),
		SortBy:   r.URL.Query().Get("sort"),
	}

	result, err := h.store.ListSessionsPaged(r.Context(), userID, filter)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// GET /api/livechat/sessions/{id}
func (h *livechatHandlers) handleSessionDetail(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, errors.New("id sesi tidak valid"))
		return
	}
	detail, err := h.store.GetSessionDetail(r.Context(), userID, id)
	if errors.Is(err, livechat.ErrNotFound) {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

// startSessionRequest matches the form in plan §11.1. Every field is required
// except AssetIDs (default empty) and max_turns (default 40).
type startSessionRequest struct {
	DeviceUUID      string  `json:"deviceUuid"`
	BotPhone        string  `json:"botPhone"`
	Title           string  `json:"title"`
	ReportText      string  `json:"reportText"`
	NamaPengadu     string  `json:"namaPengadu"`
	TanggalKejadian string  `json:"tanggalKejadian"`
	AlamatKejadian  string  `json:"alamatKejadian"`
	TitikLokasi     string  `json:"titikLokasi"`
	TitikLokasiSrc  string  `json:"titikLokasiSource"`
	AssetIDs        []int64 `json:"assetIds"`
	MaxTurns        int     `json:"maxTurns"`
	CategoryMode    string  `json:"categoryMode"`
	RepeatCount     int     `json:"repeatCount"`
}

// POST /api/livechat/sessions
func (h *livechatHandlers) handleStartSession(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	if h.manager == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("manager live chat belum siap"))
		return
	}
	// Mutual exclusion: the runner uses the same WhatsApp device. Starting a
	// live chat during a batch would interleave bot replies and confuse both
	// audit trails, so refuse up front instead of racing.
	if h.server.runners != nil {
		if st := h.server.tenantRunner(r.Context(), userID).Status(); st.State == "running" || st.State == "paused" {
			writeErr(w, http.StatusConflict, errors.New("uji Bank/OOB/Stress sedang berjalan; hentikan dulu sebelum memulai live chat"))
			return
		}
	}
	var req startSessionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	// Bot phone prioritization:
	// 1. Manually specified in request
	// 2. user_settings.bot_phone (pengaturan target bot akun user kita - pemenang utama)
	// 3. .env BOT_PHONE (fallback global)
	botPhone := strings.TrimSpace(req.BotPhone)
	if botPhone == "" {
		userCfg := h.server.userSettings(r.Context(), userID)
		botPhone = strings.TrimSpace(userCfg.BotPhone)
	}
	if botPhone == "" && h.server.cfg != nil {
		botPhone = strings.TrimSpace(h.server.cfg.Settings.BotPhone)
	}

	// Auto-random fallback for empty fields so filling the form is entirely optional:
	seed := time.Now().UnixNano()
	rng := rand.New(rand.NewSource(seed))
	preview := livechat.GenerateRandomPreview(rng, time.Now())

	nama := strings.TrimSpace(req.NamaPengadu)
	if nama == "" {
		nama = preview.NamaPengadu
	}
	tgl := strings.TrimSpace(req.TanggalKejadian)
	if tgl == "" {
		tgl = preview.TanggalKejadian
	}
	alamat := strings.TrimSpace(req.AlamatKejadian)
	if alamat == "" {
		alamat = preview.AlamatKejadian
	}
	titik := strings.TrimSpace(req.TitikLokasi)
	src := livechat.TitikLokasiSource(strings.TrimSpace(req.TitikLokasiSrc))
	if titik == "" && (src == "" || src == livechat.SourceRandom) {
		titik = preview.TitikLokasi
		src = livechat.SourceRandom
	} else if src == "" {
		if titik == "" {
			src = livechat.SourceEmpty
		} else {
			src = livechat.SourceManual
		}
	}

	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = "Live Chat " + time.Now().Format("02 Jan 15:04")
	}

	reportText := strings.TrimSpace(req.ReportText)
	if reportText == "" {
		reportText = preview.ReportText
	}

	mode := livechat.CategoryMode(strings.TrimSpace(req.CategoryMode))
	if mode == "" {
		mode = livechat.ModeBenar
	}
	maxTurns := req.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 40
	}
	if maxTurns > 200 {
		writeErr(w, http.StatusBadRequest, errors.New("batas giliran maksimal 200"))
		return
	}
	repeatCount := req.RepeatCount
	if repeatCount <= 0 {
		repeatCount = 1
	}
	if repeatCount > 20 {
		repeatCount = 20
	}

	sess, err := h.manager.StartBatch(r.Context(), userID, livechat.SessionInput{
		DeviceUUID:      req.DeviceUUID,
		WAPhone:         devicePhone(r.Context(), h.server, userID, req.DeviceUUID),
		BotPhone:        botPhone,
		Title:           title,
		ReportText:      reportText,
		NamaPengadu:     nama,
		TanggalKejadian: tgl,
		AlamatKejadian:  alamat,
		TitikLokasi:     titik,
		TitikLokasiSrc:  src,
		AssetIDs:        req.AssetIDs,
		MaxTurns:        maxTurns,
		CategoryMode:    mode,
		RNGSeed:         seed,
	}, repeatCount)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"session": sess})
}

// POST /api/livechat/sessions/{id}/stop
func (h *livechatHandlers) handleStopSession(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, errors.New("id sesi tidak valid"))
		return
	}
	// Verify ownership FIRST so a stranger can't use Stop as an existence
	// oracle, then ask the manager.
	if _, err := h.store.GetSessionDetail(r.Context(), userID, id); errors.Is(err, livechat.ErrNotFound) {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	stopped := false
	if h.manager != nil {
		stopped = h.manager.Stop(userID, id)
	}
	writeJSON(w, http.StatusOK, map[string]any{"stopped": stopped, "sessionId": id})
}

// DELETE /api/livechat/sessions/{id}
func (h *livechatHandlers) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, errors.New("id sesi tidak valid"))
		return
	}
	if h.manager != nil && h.manager.ActiveSessionID(userID) == id {
		h.manager.Stop(userID, id)
	}
	if err := h.store.DeleteSession(r.Context(), userID, id); err != nil {
		if errors.Is(err, livechat.ErrNotFound) {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
}

// PATCH /api/livechat/sessions/{id}/keterangan
func (h *livechatHandlers) handleUpdateSessionKeterangan(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, errors.New("id sesi tidak valid"))
		return
	}
	var body struct {
		Keterangan string `json:"keterangan"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := h.store.UpdateSessionKeterangan(r.Context(), userID, id, body.Keterangan); err != nil {
		if errors.Is(err, livechat.ErrNotFound) {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessionId": id, "keterangan": body.Keterangan})
}

// GET /api/livechat/assets
func (h *livechatHandlers) handleListAssets(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	assets, err := h.store.ListAssets(r.Context(), userID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"assets": assets})
}

// POST /api/livechat/assets (multipart: file, label)
func (h *livechatHandlers) handleUploadAsset(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	if h.assets == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("layanan aset belum siap"))
		return
	}
	if err := r.ParseMultipartForm(livechat.MaxAssetBytes); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("berkas tidak valid atau melebihi batas"))
		return
	}
	defer func() { _ = r.MultipartForm.RemoveAll() }()
	file, hdr, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("berkas tidak ditemukan pada form upload"))
		return
	}
	defer file.Close()
	asset, created, err := h.assets.Upload(r.Context(), userID, livechat.UploadRequest{
		Label:    r.FormValue("label"),
		FileName: hdr.Filename,
		File:     file,
		Size:     hdr.Size,
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"asset": asset, "created": created})
}

// DELETE /api/livechat/assets/{id}
func (h *livechatHandlers) handleDeleteAsset(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, errors.New("id aset tidak valid"))
		return
	}
	if err := h.store.DeleteAsset(r.Context(), userID, id); err != nil {
		switch {
		case errors.Is(err, livechat.ErrNotFound):
			writeErr(w, http.StatusNotFound, err)
		case errors.Is(err, livechat.ErrAssetInUse):
			writeErr(w, http.StatusConflict, err)
		default:
			writeErr(w, http.StatusInternalServerError, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
}

// GET /api/livechat/assets/{id}/preview → streams the stored file. The service
// re-checks ownership inside Open, so a leaked id can't fetch another tenant's
// bytes.
func (h *livechatHandlers) handleAssetPreview(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, errors.New("id aset tidak valid"))
		return
	}
	rc, asset, err := h.assets.Open(r.Context(), userID, id)
	if err != nil {
		if errors.Is(err, livechat.ErrNotFound) {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	defer rc.Close()
	if asset.Mimetype != "" {
		w.Header().Set("Content-Type", asset.Mimetype)
	}
	w.Header().Set("Content-Disposition", "inline; filename=\""+asset.FileName+"\"")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, rc)
}

// GET /api/livechat/excel/download → stream data/users/{id}/livechat.xlsx when
// it exists. Never generates the file on demand (plan §9); trigger via the
// engine finishing a session.
func (h *livechatHandlers) handleExcelDownload(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	// Ringkasan dibangun langsung dari schema livechat (tanpa file di disk).
	// Bila belum ada sesi sama sekali, endpoint menjawab 404 seperti sebelumnya
	// agar UI dapat menampilkan pesan yang sama.
	sessions, err := h.store.ListSessions(r.Context(), userID, 1)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if len(sessions) == 0 {
		writeErr(w, http.StatusNotFound, errors.New("belum ada ringkasan live chat; jalankan minimal satu sesi"))
		return
	}

	q := r.URL.Query()
	var lcFilter livechat.LiveChatFilter
	lcFilter.CategoryMode = strings.TrimSpace(q.Get("mode"))
	if lcFilter.CategoryMode == "" {
		lcFilter.CategoryMode = strings.TrimSpace(q.Get("category"))
	}
	lcFilter.Status = strings.TrimSpace(q.Get("status"))
	if t, ok := parseFilterDateTime(q.Get("date_from"), q.Get("time_from"), false); ok {
		lcFilter.DateFrom = t
	}
	if t, ok := parseFilterDateTime(q.Get("date_to"), q.Get("time_to"), true); ok {
		lcFilter.DateTo = t
	}

	data, fileName, err := livechat.GenerateExcel(r.Context(), h.store, userID, lcFilter)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", fileName))
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// GET /api/livechat/media/{sessionId}/{file} serves a bot attachment that the
// engine persisted under data/users/{id}/livechat_media (plan §6.5). The
// sessionId is verified against the caller, so a leaked file name from another
// tenant returns 404 rather than disclosing bytes.
func (h *livechatHandlers) handleIncomingMedia(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	sid, err := strconv.ParseInt(r.PathValue("sessionId"), 10, 64)
	if err != nil || sid <= 0 {
		writeErr(w, http.StatusBadRequest, errors.New("id sesi tidak valid"))
		return
	}
	if _, err := h.store.GetSessionDetail(r.Context(), userID, sid); errors.Is(err, livechat.ErrNotFound) {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	name := filepath.Base(strings.TrimSpace(r.PathValue("file")))
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		writeErr(w, http.StatusBadRequest, errors.New("nama berkas tidak valid"))
		return
	}
	path := filepath.Join(livechat.UserMediaRoot(userID), name)
	if fi, err := os.Stat(path); err != nil || fi.IsDir() {
		writeErr(w, http.StatusNotFound, errors.New("berkas media tidak ditemukan"))
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeFile(w, r, path)
}

// GET /api/livechat/events → SSE stream of engine updates for this user. The
// engine already emits to the broker (engineEmitter adapter); we expose a
// dedicated endpoint so the UI can filter to livechat events without scanning
// the shared stream. Implementation delegates to broker.Subscribe.
func (h *livechatHandlers) handleEvents(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireUser(w, r); !ok {
		return
	}
	if h.server.broker == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("SSE broker belum siap"))
		return
	}
	h.server.broker.ServeHTTP(w, r)
}

// devicePhone looks up the phone associated with a deviceUUID for the header
// shown in the UI. Returns "" when unknown; the session still works because the
// runtime uses the WA pool client, not the phone string.
func devicePhone(ctx context.Context, s *Server, userID int64, uuid string) string {
	if s == nil || s.auth.DB == nil || s.auth.DB.DB() == nil || strings.TrimSpace(uuid) == "" {
		return ""
	}
	devices, err := s.auth.DB.ListWADevices(ctx, userID)
	if err != nil {
		return ""
	}
	for _, d := range devices {
		if d.DeviceUUID == uuid {
			return d.PhoneNumber
		}
	}
	return ""
}

// livechatEmitter forwards engine events to the shared SSE broker. The engine
// passes the owning user id inside the payload; the adapter reads it there so
// no handler-level mapping is needed.
type livechatEmitter struct {
	broker *SSEBroker
}

func (e livechatEmitter) Emit(_ int64, eventType string, payload any) {
	if e.broker == nil {
		return
	}
	uid := peekUserID(payload)
	if uid <= 0 {
		return
	}
	e.broker.Broadcast(uid, eventType, payload)
}

func peekUserID(payload any) int64 {
	m, ok := payload.(map[string]any)
	if !ok {
		return 0
	}
	switch v := m["userId"].(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	}
	return 0
}

// AttachLiveChat wires the livechat manager + asset service into the server.
// It is called from main.go after NewServer so the default server (used by
// tests) keeps compiling unchanged.
func (s *Server) AttachLiveChat(store *livechat.Store, assets *livechat.AssetService, manager *livechat.Manager, exporter *livechat.Exporter) {
	if store == nil || manager == nil {
		return
	}
	s.livechat = &livechatHandlers{
		server:   s,
		store:    store,
		assets:   assets,
		manager:  manager,
		exporter: exporter,
	}
}

// LiveChatEmitter returns an engineEmitter that routes into the server's SSE
// broker. main.go hands this to livechat.NewManager.
func LiveChatEmitter(broker *SSEBroker) livechat.EngineEmitter {
	return livechatEmitter{broker: broker}
}

// SettingsForUser exposes the caller's effective HumanSettings to main.go so
// the livechat manager can build its humanizer with the same values the runner
// uses.
func (s *Server) SettingsForUser(ctx context.Context, userID int64) model.HumanSettings {
	return s.userSettings(ctx, userID)
}

// ShutdownLiveChat cancels every engine started by this server. Safe to call
// when no livechat manager was attached.
func (s *Server) ShutdownLiveChat(ctx context.Context) error {
	if s.livechat == nil || s.livechat.manager == nil {
		return nil
	}
	return s.livechat.manager.Shutdown(ctx)
}
