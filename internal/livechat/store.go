package livechat

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"
)

// Store persists Live Chat state in the `livechat` schema. Every query names
// the schema explicitly (never relies on search_path) and every read/write that
// belongs to a tester is scoped by user_id, so one account can never see
// another's sessions, messages, turns, or assets.
type Store struct {
	db *sql.DB
}

// NewStore wraps the shared PostgreSQL pool.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// SessionInput is the caller-supplied portion of a new session; server-side
// fields (status, timestamps, total_turns) are set by CreateSession.
type SessionInput struct {
	DeviceUUID      string
	WAPhone         string
	BotPhone        string
	Title           string
	ReportText      string
	NamaPengadu     string
	TanggalKejadian string // YYYY-MM-DD
	AlamatKejadian  string
	TitikLokasi     string
	TitikLokasiSrc  TitikLokasiSource
	AssetIDs        []int64
	MaxTurns        int
	CategoryMode    CategoryMode
	RNGSeed         int64
}

// CreateSession inserts a new running session for userID.
func (s *Store) CreateSession(ctx context.Context, userID int64, in SessionInput) (Session, error) {
	if userID <= 0 {
		return Session{}, errors.New("user id tidak valid")
	}
	if in.TanggalKejadian == "" {
		in.TanggalKejadian = time.Now().Format("2006-01-02")
	}
	if in.Title == "" {
		in.Title = "Live Chat " + time.Now().Format("02 Jan 15:04")
	}
	if err := in.validate(); err != nil {
		return Session{}, err
	}
	tanggal, err := time.Parse("2006-01-02", in.TanggalKejadian)
	if err != nil {
		return Session{}, errors.New("tanggal kejadian harus format YYYY-MM-DD")
	}
	ids := in.AssetIDs
	if ids == nil {
		ids = []int64{}
	}
	// Every referenced asset must belong to the caller; otherwise a leaked ID
	// from another tenant would be attached to this session's Excel summary and
	// later sent to the bot as if it were the caller's own file.
	if err := s.VerifyOwnership(ctx, userID, ids); err != nil {
		return Session{}, err
	}
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO livechat.sessions (
			user_id, device_uuid, wa_phone, bot_phone, title, report_text,
			nama_pengadu, tanggal_kejadian, alamat_kejadian, titik_lokasi,
			titik_lokasi_source, asset_ids, max_turns, category_mode, status, rng_seed
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
		RETURNING `+sessionColumns,
		userID, in.DeviceUUID, in.WAPhone, in.BotPhone, in.Title, nullableString(in.ReportText),
		in.NamaPengadu, tanggal, in.AlamatKejadian, nullableString(in.TitikLokasi),
		string(in.TitikLokasiSrc), pq.Array(ids), in.MaxTurns, string(in.CategoryMode), string(StatusRunning), in.RNGSeed,
	)
	return scanSession(row)
}

func (in SessionInput) validate() error {
	if strings.TrimSpace(in.DeviceUUID) == "" {
		return errors.New("device whatsapp wajib dipilih")
	}
	if strings.TrimSpace(in.BotPhone) == "" {
		return errors.New("nomor whatsapp bot target wajib diisi")
	}
	if _, err := time.Parse("2006-01-02", in.TanggalKejadian); err != nil {
		return errors.New("tanggal kejadian harus format YYYY-MM-DD")
	}
	switch in.CategoryMode {
	case ModeBenar, ModeTypo, ModeNgasal, ModeCelah:
	default:
		return fmt.Errorf("kategori mode tidak dikenal: %q", in.CategoryMode)
	}
	switch in.TitikLokasiSrc {
	case SourceRandom, SourceManual, SourceEmpty:
	default:
		return fmt.Errorf("sumber titik lokasi tidak dikenal: %q", in.TitikLokasiSrc)
	}
	return nil
}

const sessionColumns = `
	id, user_id, device_uuid, wa_phone, bot_phone, title, coalesce(report_text,''),
	nama_pengadu, to_char(tanggal_kejadian,'YYYY-MM-DD'), alamat_kejadian,
	coalesce(titik_lokasi,''), titik_lokasi_source, asset_ids, max_turns,
	category_mode, status, coalesce(end_reason,''), coalesce(last_chat_excerpt,''),
	coalesce(ticket_no,''), total_turns, created_at, started_at, ended_at, rng_seed`

type scanner interface {
	Scan(dest ...any) error
}

func scanSession(sc scanner) (Session, error) {
	var (
		s        Session
		titikSrc string
		mode     string
		status   string
		assets   pq.Int64Array
	)
	err := sc.Scan(
		&s.ID, &s.UserID, &s.DeviceUUID, &s.WAPhone, &s.BotPhone, &s.Title, &s.ReportText,
		&s.NamaPengadu, &s.TanggalKejadian, &s.AlamatKejadian, &s.TitikLokasi, &titikSrc,
		&assets, &s.MaxTurns, &mode, &status, &s.EndReason, &s.LastChatExcerpt,
		&s.TicketNo, &s.TotalTurns, &s.CreatedAt, &s.StartedAt, &s.EndedAt, &s.RNGSeed,
	)
	if err != nil {
		return Session{}, err
	}
	s.TitikLokasiSrc = TitikLokasiSource(titikSrc)
	s.CategoryMode = CategoryMode(mode)
	s.Status = Status(status)
	s.AssetIDs = []int64(assets)
	if s.AssetIDs == nil {
		s.AssetIDs = []int64{}
	}
	return s, nil
}

// ListSessions returns session headers for one user, newest first.
func (s *Store) ListSessions(ctx context.Context, userID int64, limit int) ([]Session, error) {
	if userID <= 0 {
		return nil, errors.New("user id tidak valid")
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+sessionColumns+`
		FROM livechat.sessions
		WHERE user_id = $1
		ORDER BY created_at DESC, id DESC
		LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Session, 0, 8)
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

// GetSessionDetail returns the session plus its messages and turns, scoped to
// userID so leaked IDs from another tenant return ErrNotFound.
func (s *Store) GetSessionDetail(ctx context.Context, userID, sessionID int64) (SessionDetail, error) {
	if userID <= 0 || sessionID <= 0 {
		return SessionDetail{}, ErrNotFound
	}
	var detail SessionDetail
	row := s.db.QueryRowContext(ctx, `
		SELECT `+sessionColumns+`
		FROM livechat.sessions
		WHERE id = $1 AND user_id = $2`, sessionID, userID)
	sess, err := scanSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionDetail{}, ErrNotFound
	}
	if err != nil {
		return SessionDetail{}, err
	}
	detail.Session = sess

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, session_id, turn_no, direction, coalesce(body,''),
		       coalesce(media_json::text,''), msg_type, sent_at, latency_ms
		FROM livechat.messages
		WHERE session_id = $1
		ORDER BY id`, sessionID)
	if err != nil {
		return SessionDetail{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var m Message
		var dir, msgType string
		if err := rows.Scan(&m.ID, &m.SessionID, &m.TurnNo, &dir, &m.Body, &m.MediaJSON, &msgType, &m.SentAt, &m.LatencyMs); err != nil {
			return SessionDetail{}, err
		}
		m.Direction = Direction(dir)
		m.MsgType = MsgType(msgType)
		detail.Messages = append(detail.Messages, m)
	}
	if err := rows.Err(); err != nil {
		return SessionDetail{}, err
	}

	trs, err := s.db.QueryContext(ctx, `
		SELECT id, session_id, turn_no, coalesce(bot_question,''),
		       coalesce(question_kind,''), coalesce(our_answer,''),
		       coalesce(sent_media_json::text,''), coalesce(category_mode,''),
		       coalesce(match_status,''), coalesce(note,''), created_at
		FROM livechat.turns
		WHERE session_id = $1
		ORDER BY turn_no`, sessionID)
	if err != nil {
		return SessionDetail{}, err
	}
	defer trs.Close()
	for trs.Next() {
		var t Turn
		if err := trs.Scan(&t.ID, &t.SessionID, &t.TurnNo, &t.BotQuestion, &t.QuestionKind, &t.OurAnswer, &t.SentMediaJSON, &t.CategoryMode, &t.MatchStatus, &t.Note, &t.CreatedAt); err != nil {
			return SessionDetail{}, err
		}
		detail.Turns = append(detail.Turns, t)
	}
	return detail, trs.Err()
}

// UpdateSessionProgress persists a single bot/our turn's contribution to the
// session header (turn count + ticket if detected). It is idempotent enough for
// the engine's writes: turn count is the caller's authoritative value.
func (s *Store) UpdateSessionProgress(ctx context.Context, userID, sessionID int64, totalTurns int, ticketNo string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE livechat.sessions
		SET total_turns = $3, ticket_no = coalesce(nullif($4,''), ticket_no)
		WHERE id = $1 AND user_id = $2 AND status = 'running'`,
		sessionID, userID, totalTurns, ticketNo)
	return err
}

// FinishSession closes a session with a final status and records the excerpt.
// When already finished it is a no-op (idempotent).
func (s *Store) FinishSession(ctx context.Context, userID, sessionID int64, status Status, endReason, ticketNo, excerpt string) error {
	switch status {
	case StatusFinished, StatusFailed, StatusStopped, StatusTimeout:
	default:
		return fmt.Errorf("status akhir tidak dikenal: %q", status)
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE livechat.sessions
		SET status = $3,
		    end_reason = coalesce(nullif($4,''), end_reason),
		    ticket_no = coalesce(nullif($5,''), ticket_no),
		    last_chat_excerpt = coalesce(nullif($6,''), last_chat_excerpt),
		    ended_at = now()
		WHERE id = $1 AND user_id = $2 AND status IN ('running','stopped')`,
		sessionID, userID, string(status), endReason, ticketNo, excerpt)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrAlreadyClosed
	}
	return nil
}

// MarkStarted stamps the moment the opening message starts.
func (s *Store) MarkStarted(ctx context.Context, userID, sessionID int64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE livechat.sessions
		SET started_at = coalesce(started_at, now())
		WHERE id = $1 AND user_id = $2 AND status = 'running'`,
		sessionID, userID)
	return err
}

// InsertMessage appends a chat bubble and returns its row id.
func (s *Store) InsertMessage(ctx context.Context, userID int64, m Message) (int64, error) {
	if userID <= 0 || m.SessionID <= 0 {
		return 0, ErrNotFound
	}
	// Ownership gate so a leaked session id cannot write into another tenant.
	if !s.ownsSession(ctx, userID, m.SessionID) {
		return 0, ErrNotFound
	}
	if err := validateMessage(&m); err != nil {
		return 0, err
	}
	var media sql.NullString
	if strings.TrimSpace(m.MediaJSON) != "" {
		if !json.Valid([]byte(m.MediaJSON)) {
			return 0, errors.New("media json tidak valid")
		}
		media = sql.NullString{String: m.MediaJSON, Valid: true}
	}
	var id int64
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO livechat.messages (session_id, turn_no, direction, body, media_json, msg_type, latency_ms)
		VALUES ($1,$2,$3,$4,$5::jsonb,$6,$7)
		RETURNING id`,
		m.SessionID, m.TurnNo, string(m.Direction), nullableString(m.Body), media, string(m.MsgType), m.LatencyMs,
	).Scan(&id)
	return id, err
}

func validateMessage(m *Message) error {
	switch m.Direction {
	case DirectionSent, DirectionReceived:
	default:
		return fmt.Errorf("direction tidak dikenal: %q", m.Direction)
	}
	if m.MsgType == "" {
		m.MsgType = MsgText
	}
	switch m.MsgType {
	case MsgText, MsgImage, MsgDocument, MsgLocation:
	default:
		return fmt.Errorf("msg type tidak dikenal: %q", m.MsgType)
	}
	return nil
}

// InsertTurn appends an audit row for one bot turn. UNIQUE(session_id, turn_no)
// prevents duplicate insertions from a retried engine loop.
func (s *Store) InsertTurn(ctx context.Context, userID int64, t Turn) (int64, error) {
	if userID <= 0 || t.SessionID <= 0 {
		return 0, ErrNotFound
	}
	if !s.ownsSession(ctx, userID, t.SessionID) {
		return 0, ErrNotFound
	}
	var mq sql.NullString
	if strings.TrimSpace(t.SentMediaJSON) != "" {
		if !json.Valid([]byte(t.SentMediaJSON)) {
			return 0, errors.New("sent media json tidak valid")
		}
		mq = sql.NullString{String: t.SentMediaJSON, Valid: true}
	}
	var id int64
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO livechat.turns (
			session_id, turn_no, bot_question, question_kind, our_answer,
			sent_media_json, category_mode, match_status, note
		) VALUES ($1,$2,$3,$4,$5,$6::jsonb,$7,$8,$9)
		RETURNING id`,
		t.SessionID, t.TurnNo, nullableString(t.BotQuestion), nullableString(t.QuestionKind),
		nullableString(t.OurAnswer), mq, nullableString(t.CategoryMode),
		nullableString(t.MatchStatus), nullableString(t.Note),
	).Scan(&id)
	return id, err
}

// DeleteSession removes one session (and cascades messages + turns). Scoped by
// user_id so the operation is rejected for other tenants.
func (s *Store) DeleteSession(ctx context.Context, userID, sessionID int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM livechat.sessions WHERE id = $1 AND user_id = $2`, sessionID, userID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SessionExists confirms ownership without loading the full header.
func (s *Store) ownsSession(ctx context.Context, userID, sessionID int64) bool {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM livechat.sessions WHERE id = $1 AND user_id = $2`, sessionID, userID).Scan(&one)
	return err == nil && one == 1
}

// AssetInput is the caller-provided metadata for a new asset; filesystem work
// (content addressing) lives in assets.go.
type AssetInput struct {
	Kind       AssetKind
	Label      string
	FileName   string
	StoredPath string
	Mimetype   string
	SizeBytes  int64
	SHA256     string
}

// CreateAsset inserts a new asset row. Duplicate (user_id, sha256) returns
// ErrDuplicate; callers should look up the existing row instead.
func (s *Store) CreateAsset(ctx context.Context, userID int64, in AssetInput) (Asset, error) {
	if userID <= 0 {
		return Asset{}, errors.New("user id tidak valid")
	}
	if in.Kind != AssetImage && in.Kind != AssetDocument {
		return Asset{}, errors.New("jenis aset tidak dikenal")
	}
	if strings.TrimSpace(in.StoredPath) == "" || len(in.SHA256) != 64 {
		return Asset{}, errors.New("aset tidak lengkap")
	}
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO livechat.assets (user_id, kind, label, file_name, stored_path, mimetype, size_bytes, sha256)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		RETURNING id, user_id, kind, label, file_name, stored_path, mimetype, size_bytes, sha256, created_at`,
		userID, string(in.Kind), in.Label, in.FileName, in.StoredPath, in.Mimetype, in.SizeBytes, in.SHA256)
	var a Asset
	var kind string
	if err := row.Scan(&a.ID, &a.UserID, &kind, &a.Label, &a.FileName, &a.StoredPath, &a.Mimetype, &a.SizeBytes, &a.SHA256, &a.CreatedAt); err != nil {
		if isUniqueViolation(err) {
			return Asset{}, ErrDuplicate
		}
		return Asset{}, err
	}
	a.Kind = AssetKind(kind)
	return a, nil
}

// ListAssets returns the caller's assets, newest first.
func (s *Store) ListAssets(ctx context.Context, userID int64) ([]Asset, error) {
	if userID <= 0 {
		return nil, errors.New("user id tidak valid")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, user_id, kind, label, file_name, stored_path, mimetype, size_bytes, sha256, created_at
		FROM livechat.assets
		WHERE user_id = $1
		ORDER BY created_at DESC, id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Asset, 0, 8)
	for rows.Next() {
		var a Asset
		var kind string
		if err := rows.Scan(&a.ID, &a.UserID, &kind, &a.Label, &a.FileName, &a.StoredPath, &a.Mimetype, &a.SizeBytes, &a.SHA256, &a.CreatedAt); err != nil {
			return nil, err
		}
		a.Kind = AssetKind(kind)
		out = append(out, a)
	}
	return out, rows.Err()
}

// GetAsset returns one asset scoped by userID.
func (s *Store) GetAsset(ctx context.Context, userID, assetID int64) (Asset, error) {
	if userID <= 0 || assetID <= 0 {
		return Asset{}, ErrNotFound
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT id, user_id, kind, label, file_name, stored_path, mimetype, size_bytes, sha256, created_at
		FROM livechat.assets
		WHERE id = $1 AND user_id = $2`, assetID, userID)
	var a Asset
	var kind string
	if err := row.Scan(&a.ID, &a.UserID, &kind, &a.Label, &a.FileName, &a.StoredPath, &a.Mimetype, &a.SizeBytes, &a.SHA256, &a.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Asset{}, ErrNotFound
		}
		return Asset{}, err
	}
	a.Kind = AssetKind(kind)
	return a, nil
}

// VerifyOwnership reports whether every asset id belongs to userID. It is the
// gate used before a session's asset_ids (and again before sending) so another
// tenant's asset can never be attached or transmitted on this session.
func (s *Store) VerifyOwnership(ctx context.Context, userID int64, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	if userID <= 0 {
		return ErrNotFound
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id FROM livechat.assets WHERE user_id = $1 AND id = ANY($2)`,
		userID, pq.Array(ids))
	if err != nil {
		return err
	}
	defer rows.Close()
	found := make(map[int64]struct{}, len(ids))
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		found[id] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if _, ok := found[id]; !ok {
			return fmt.Errorf("aset id %d bukan milik pengguna", id)
		}
	}
	return nil
}

// findAssetBySHA looks up an existing row by (user_id, sha256). Used by the
// upload path for dedup before touching disk.
func (s *Store) findAssetBySHA(ctx context.Context, userID int64, sha string) (Asset, error) {
	if userID <= 0 || len(sha) != 64 {
		return Asset{}, ErrNotFound
	}
	var id int64
	err := s.db.QueryRowContext(ctx, `
		SELECT id FROM livechat.assets WHERE user_id = $1 AND sha256 = $2`,
		userID, sha).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return Asset{}, ErrNotFound
	}
	if err != nil {
		return Asset{}, err
	}
	return s.GetAsset(ctx, userID, id)
}

// AssetInUse reports whether any session still references assetID. Selection is
// checked against sessions.asset_ids; deletion of an in-use asset must be
// rejected to avoid breaking stored sessions.
func (s *Store) AssetInUse(ctx context.Context, userID, assetID int64) (bool, error) {
	if userID <= 0 || assetID <= 0 {
		return false, errors.New("parameter tidak valid")
	}
	var exists bool
	err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM livechat.sessions WHERE user_id = $1 AND $2 = ANY(asset_ids)
		)`, userID, assetID).Scan(&exists)
	return exists, err
}

// DeleteAsset removes an asset row scoped by userID. Refuses when any session is
// still pointing at the asset, so we never leave a dangling reference.
func (s *Store) DeleteAsset(ctx context.Context, userID, assetID int64) error {
	inUse, err := s.AssetInUse(ctx, userID, assetID)
	if err != nil {
		return err
	}
	if inUse {
		return ErrAssetInUse
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM livechat.assets WHERE id = $1 AND user_id = $2`, assetID, userID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Errors exposed by the store. They are stable so the HTTP layer can map them
// to the right status codes without inspecting driver messages.
var (
	ErrNotFound      = errors.New("data live chat tidak ditemukan")
	ErrAlreadyClosed = errors.New("sesi live chat sudah berakhir")
	ErrDuplicate     = errors.New("aset ini sudah pernah diunggah")
	ErrAssetInUse    = errors.New("aset masih dipakai sesi, tidak bisa dihapus")
)

func nullableString(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

func isUniqueViolation(err error) bool {
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		return pqErr.Code == "23505"
	}
	return false
}
