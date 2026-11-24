-- ============================================================================
-- LIVE CHAT SCHEMA MIGRATION v1
-- Tabel: livechat.assets, livechat.sessions, livechat.messages, livechat.turns
-- Sifat: Idempoten (IF NOT EXISTS), aman dijalankan berulang kali.
-- ============================================================================

BEGIN;

CREATE SCHEMA IF NOT EXISTS livechat;

CREATE TABLE IF NOT EXISTS livechat.assets (
    id          BIGSERIAL PRIMARY KEY,
    user_id     BIGINT NOT NULL,
    kind        VARCHAR(10) NOT NULL CHECK (kind IN ('image', 'document')),
    label       VARCHAR(120) NOT NULL,
    file_name   VARCHAR(200) NOT NULL,
    stored_path TEXT NOT NULL,
    mimetype    VARCHAR(100) NOT NULL,
    size_bytes  BIGINT NOT NULL,
    sha256      VARCHAR(64) NOT NULL,
    created_at  TIMESTAMPTZ DEFAULT now() NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_live_assets_user_created ON livechat.assets (user_id, created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS uq_live_assets_user_sha ON livechat.assets (user_id, sha256);

CREATE TABLE IF NOT EXISTS livechat.sessions (
    id                 BIGSERIAL PRIMARY KEY,
    user_id            BIGINT NOT NULL,
    device_uuid        VARCHAR(64) NOT NULL,
    wa_phone           VARCHAR(30) NOT NULL,
    bot_phone          VARCHAR(30) NOT NULL,
    title              VARCHAR(200) NOT NULL,
    report_text        TEXT,
    nama_pengadu       VARCHAR(120) NOT NULL,
    tanggal_kejadian   DATE NOT NULL,
    alamat_kejadian    TEXT NOT NULL,
    titik_lokasi       VARCHAR(200),
    titik_lokasi_source VARCHAR(10) DEFAULT 'random' NOT NULL CHECK (titik_lokasi_source IN ('random', 'manual', 'empty')),
    asset_ids          BIGINT[] DEFAULT '{}'::BIGINT[] NOT NULL,
    max_turns          INT DEFAULT 40 NOT NULL,
    category_mode      VARCHAR(20) NOT NULL CHECK (category_mode IN ('benar', 'typo', 'ngasal', 'celah')),
    status             VARCHAR(20) NOT NULL CHECK (status IN ('running', 'finished', 'failed', 'stopped', 'timeout')),
    end_reason         TEXT,
    last_chat_excerpt  TEXT,
    ticket_no          VARCHAR(40),
    total_turns        INT DEFAULT 0 NOT NULL,
    created_at         TIMESTAMPTZ DEFAULT now() NOT NULL,
    started_at         TIMESTAMPTZ,
    ended_at           TIMESTAMPTZ,
    rng_seed           BIGINT DEFAULT 0 NOT NULL,
    deleted_at         TIMESTAMPTZ,
    keterangan         TEXT DEFAULT '' NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_live_sessions_user_created ON livechat.sessions (user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_live_sessions_status ON livechat.sessions (status);
CREATE INDEX IF NOT EXISTS idx_sessions_user_active ON livechat.sessions (user_id, created_at DESC) WHERE (deleted_at IS NULL);

CREATE TABLE IF NOT EXISTS livechat.messages (
    id          BIGSERIAL PRIMARY KEY,
    session_id  BIGINT NOT NULL REFERENCES livechat.sessions(id) ON DELETE CASCADE,
    turn_no     INT NOT NULL,
    direction   VARCHAR(10) NOT NULL CHECK (direction IN ('sent', 'received')),
    body        TEXT,
    media_json  JSONB,
    msg_type    VARCHAR(12) DEFAULT 'text' NOT NULL CHECK (msg_type IN ('text', 'image', 'document', 'location')),
    sent_at     TIMESTAMPTZ DEFAULT now() NOT NULL,
    latency_ms  INT
);
CREATE INDEX IF NOT EXISTS idx_live_messages_session ON livechat.messages (session_id, id);

CREATE TABLE IF NOT EXISTS livechat.turns (
    id              BIGSERIAL PRIMARY KEY,
    session_id      BIGINT NOT NULL REFERENCES livechat.sessions(id) ON DELETE CASCADE,
    turn_no         INT NOT NULL,
    bot_question    TEXT,
    question_kind   VARCHAR(30),
    our_answer      TEXT,
    sent_media_json JSONB,
    category_mode   VARCHAR(20),
    match_status    VARCHAR(20),
    note            TEXT,
    created_at      TIMESTAMPTZ DEFAULT now() NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_live_turns_session_turn ON livechat.turns (session_id, turn_no);

COMMIT;
