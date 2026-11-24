-- ============================================================================
-- MASTER DATABASE MIGRATION & SEED FOR WHATSAPP GATEWAY WORKBENCH
-- Meliputi:
--   1. Schema public    (users, sessions, user_workbooks, user_settings, wa_devices)
--   2. Seed Accounts    (superadmin & kamil dengan password: 12345)
--   3. Schema workbench (user_workbooks, bank_rows, out_of_bank_rows, stress_test_rows)
--   4. Schema livechat  (assets, sessions, messages, turns)
-- Idempoten (IF NOT EXISTS & ON CONFLICT) — aman dieksekusi berulang kali.
-- ============================================================================

BEGIN;

-- ============================================================================
-- 1. SCHEMA PUBLIC (Core Multi-User Platform)
-- ============================================================================

CREATE TABLE IF NOT EXISTS public.users (
    id            BIGSERIAL PRIMARY KEY,
    username      VARCHAR(50) NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    full_name     VARCHAR(120) NOT NULL,
    role          VARCHAR(20) NOT NULL CHECK (role IN ('admin','tester')),
    max_wa_devices INT NOT NULL DEFAULT 3,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE public.users ADD COLUMN IF NOT EXISTS max_wa_devices INT NOT NULL DEFAULT 3;

CREATE TABLE IF NOT EXISTS public.sessions (
    id         VARCHAR(128) PRIMARY KEY,
    user_id    BIGINT NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_sessions_user_id ON public.sessions(user_id);

CREATE TABLE IF NOT EXISTS public.user_workbooks (
    user_id      BIGINT PRIMARY KEY REFERENCES public.users(id) ON DELETE CASCADE,
    file_name    TEXT NOT NULL,
    file_data    BYTEA NOT NULL,
    metrics_data JSONB NOT NULL DEFAULT '{}'::jsonb,
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS public.user_settings (
    user_id    BIGINT PRIMARY KEY REFERENCES public.users(id) ON DELETE CASCADE,
    bot_phone  VARCHAR(30) NOT NULL DEFAULT '',
    settings   JSONB NOT NULL DEFAULT '{}'::jsonb,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS public.wa_devices (
    id           BIGSERIAL PRIMARY KEY,
    user_id      BIGINT NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    device_name  VARCHAR(120) NOT NULL,
    phone_number VARCHAR(30) NOT NULL DEFAULT '',
    jid          VARCHAR(120) NOT NULL DEFAULT '',
    is_connected BOOLEAN NOT NULL DEFAULT false,
    device_uuid  VARCHAR(64) NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen    TIMESTAMPTZ
);
ALTER TABLE public.wa_devices ADD COLUMN IF NOT EXISTS device_uuid VARCHAR(64) NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_wa_devices_user_id ON public.wa_devices(user_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_wa_devices_uuid ON public.wa_devices(device_uuid) WHERE device_uuid <> '';
CREATE UNIQUE INDEX IF NOT EXISTS idx_wa_devices_phone ON public.wa_devices(phone_number) WHERE phone_number <> '';

-- ============================================================================
-- 2. SEED ACCOUNTS: superadmin & kamil (Password: 12345)
-- Bcrypt Hash ($2a$10$HDsvZuhlFBYZpJyBP89Pb.O9Bl7wXvUTFKT0bWNl1INT1hLUdis5m)
-- ============================================================================

INSERT INTO public.users (username, password_hash, full_name, role, max_wa_devices)
VALUES 
    ('superadmin', '$2a$10$HDsvZuhlFBYZpJyBP89Pb.O9Bl7wXvUTFKT0bWNl1INT1hLUdis5m', 'Super Admin Kominfo', 'admin', 10),
    ('kamil', '$2a$10$HDsvZuhlFBYZpJyBP89Pb.O9Bl7wXvUTFKT0bWNl1INT1hLUdis5m', 'Taufik Kamil', 'admin', 10)
ON CONFLICT (username) DO UPDATE 
SET password_hash  = EXCLUDED.password_hash,
    full_name      = EXCLUDED.full_name,
    role           = 'admin',
    max_wa_devices = 10;

-- Inisialisasi default settings per user jika belum ada
INSERT INTO public.user_settings (user_id, bot_phone, settings)
SELECT id, '', '{}'::jsonb FROM public.users WHERE username IN ('superadmin', 'kamil')
ON CONFLICT (user_id) DO NOTHING;

-- ============================================================================
-- 3. SCHEMA WORKBENCH (Data Bank Soal, OOB, Stress Test)
-- ============================================================================

CREATE SCHEMA IF NOT EXISTS workbench;

CREATE TABLE IF NOT EXISTS workbench.user_workbooks (
    user_id     BIGINT       PRIMARY KEY REFERENCES public.users(id) ON DELETE CASCADE,
    file_name   VARCHAR(200) NOT NULL DEFAULT 'workbook.xlsx',
    opd_order   TEXT[]       NOT NULL DEFAULT '{}',
    imported_at TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS workbench.bank_rows (
    id                 BIGSERIAL    PRIMARY KEY,
    user_id            BIGINT       NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    no                 INT          NOT NULL,
    excel_row          INT          NOT NULL DEFAULT 0,
    opd                VARCHAR(200) NOT NULL DEFAULT '',
    kategori           VARCHAR(200) NOT NULL DEFAULT '',
    status_bank        VARCHAR(50)  NOT NULL DEFAULT '',
    question_ref       TEXT         NOT NULL DEFAULT '',
    question_input     TEXT         NOT NULL DEFAULT '',
    expected_reply     TEXT         NOT NULL DEFAULT '',
    actual_reply       TEXT         NOT NULL DEFAULT '',
    notes              TEXT         NOT NULL DEFAULT '',
    result_status      VARCHAR(20)  NOT NULL DEFAULT '' CHECK (result_status IN ('','Cocok','Tidak')),
    has_expected_file  BOOLEAN NOT NULL DEFAULT false,
    has_expected_coord BOOLEAN NOT NULL DEFAULT false,
    has_actual_file    BOOLEAN NOT NULL DEFAULT false,
    has_actual_coord   BOOLEAN NOT NULL DEFAULT false,
    media_files        TEXT[]  NOT NULL DEFAULT '{}',
    metrics            JSONB   NOT NULL DEFAULT '{}'::jsonb,
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_bank_user_no UNIQUE (user_id, no)
);

CREATE TABLE IF NOT EXISTS workbench.out_of_bank_rows (
    id                BIGSERIAL    PRIMARY KEY,
    user_id           BIGINT       NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    no                INT          NOT NULL,
    excel_row         INT          NOT NULL DEFAULT 0,
    source_ref        VARCHAR(200) NOT NULL DEFAULT '',
    scenario_type     VARCHAR(200) NOT NULL DEFAULT '',
    base_question     TEXT         NOT NULL DEFAULT '',
    probe_question    TEXT         NOT NULL DEFAULT '',
    expected_behavior TEXT         NOT NULL DEFAULT '',
    actual_reply      TEXT         NOT NULL DEFAULT '',
    human_summary     TEXT         NOT NULL DEFAULT '',
    notes             TEXT         NOT NULL DEFAULT '',
    result_status     VARCHAR(20)  NOT NULL DEFAULT '' CHECK (result_status IN ('','Cocok','Tidak')),
    forbidden_year    VARCHAR(20)  NOT NULL DEFAULT '',
    reference_year    VARCHAR(20)  NOT NULL DEFAULT '',
    media_files       TEXT[]  NOT NULL DEFAULT '{}',
    metrics           JSONB   NOT NULL DEFAULT '{}'::jsonb,
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_oob_user_no UNIQUE (user_id, no)
);

CREATE TABLE IF NOT EXISTS workbench.stress_test_rows (
    id                BIGSERIAL    PRIMARY KEY,
    user_id           BIGINT       NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    no                INT          NOT NULL,
    excel_row         INT          NOT NULL DEFAULT 0,
    stress_category   VARCHAR(200) NOT NULL DEFAULT '',
    user_prompt_q     TEXT         NOT NULL DEFAULT '',
    intent_purpose    TEXT         NOT NULL DEFAULT '',
    expected_behavior TEXT         NOT NULL DEFAULT '',
    actual_reply      TEXT         NOT NULL DEFAULT '',
    human_summary     TEXT         NOT NULL DEFAULT '',
    notes             TEXT         NOT NULL DEFAULT '',
    result_status     VARCHAR(20)  NOT NULL DEFAULT '' CHECK (result_status IN ('','Cocok','Tidak')),
    politeness_score  INT          NOT NULL DEFAULT 0,
    hallucination_lvl VARCHAR(50)  NOT NULL DEFAULT '',
    media_files       TEXT[]  NOT NULL DEFAULT '{}',
    metrics           JSONB   NOT NULL DEFAULT '{}'::jsonb,
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_stress_user_no UNIQUE (user_id, no)
);

CREATE INDEX IF NOT EXISTS idx_wb_bank_user_status   ON workbench.bank_rows (user_id, result_status);
CREATE INDEX IF NOT EXISTS idx_wb_bank_opd           ON workbench.bank_rows (user_id, opd, result_status);
CREATE INDEX IF NOT EXISTS idx_wb_oob_user_status    ON workbench.out_of_bank_rows (user_id, result_status);
CREATE INDEX IF NOT EXISTS idx_wb_stress_user_status ON workbench.stress_test_rows (user_id, result_status);

-- Inisialisasi metadata user_workbooks per user
INSERT INTO workbench.user_workbooks (user_id, file_name, opd_order)
SELECT id, 'Uji-Bank-Pertanyaan-Programmer-4.xlsx', '{}'::text[] FROM public.users WHERE username IN ('superadmin', 'kamil')
ON CONFLICT (user_id) DO NOTHING;

-- ============================================================================
-- 4. SCHEMA LIVECHAT (Live Chat Emulator & Session Inspector)
-- ============================================================================

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
