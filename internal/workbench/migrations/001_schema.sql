-- ============================================================================
-- Workbench DB migration v1 — schema workbench
-- Plan: PLAN_WORKBENCH_DB.md v4 (Tahap 2)
-- Sifat: ADITIF murni. Tidak menyentuh public.* maupun livechat.*
-- Idempoten (IF NOT EXISTS) dan transaksional.
-- Rollback: DROP SCHEMA workbench CASCADE;
-- ============================================================================

BEGIN;

CREATE SCHEMA IF NOT EXISTS workbench;

-- 3.1.1 Metadata per user (+ urutan OPD untuk sheet Ringkasan)
CREATE TABLE IF NOT EXISTS workbench.user_workbooks (
    user_id     BIGINT       PRIMARY KEY
        REFERENCES public.users(id) ON DELETE CASCADE,
    file_name   VARCHAR(200) NOT NULL DEFAULT 'workbook.xlsx',
    opd_order   TEXT[]       NOT NULL DEFAULT '{}',
    imported_at TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT now()
);

-- 3.1.2 Bank Pertanyaan (210 baris terverifikasi)
CREATE TABLE IF NOT EXISTS workbench.bank_rows (
    id                 BIGSERIAL    PRIMARY KEY,
    user_id            BIGINT       NOT NULL
        REFERENCES public.users(id) ON DELETE CASCADE,
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
    result_status      VARCHAR(20)  NOT NULL DEFAULT ''
        CHECK (result_status IN ('','Cocok','Tidak')),
    has_expected_file  BOOLEAN NOT NULL DEFAULT false,
    has_expected_coord BOOLEAN NOT NULL DEFAULT false,
    has_actual_file    BOOLEAN NOT NULL DEFAULT false,
    has_actual_coord   BOOLEAN NOT NULL DEFAULT false,
    media_files        TEXT[]  NOT NULL DEFAULT '{}',
    metrics            JSONB   NOT NULL DEFAULT '{}'::jsonb,
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_bank_user_no UNIQUE (user_id, no)
);

-- 3.1.3 Uji Diluar Bank (52 baris terverifikasi)
CREATE TABLE IF NOT EXISTS workbench.out_of_bank_rows (
    id                BIGSERIAL    PRIMARY KEY,
    user_id           BIGINT       NOT NULL
        REFERENCES public.users(id) ON DELETE CASCADE,
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
    result_status     VARCHAR(20)  NOT NULL DEFAULT ''
        CHECK (result_status IN ('','Cocok','Tidak')),
    forbidden_year    VARCHAR(20)  NOT NULL DEFAULT '',
    reference_year    VARCHAR(20)  NOT NULL DEFAULT '',
    media_files       TEXT[]  NOT NULL DEFAULT '{}',
    metrics           JSONB   NOT NULL DEFAULT '{}'::jsonb,
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_oob_user_no UNIQUE (user_id, no)
);

-- 3.1.4 Uji Stress AI (15 baris terverifikasi)
CREATE TABLE IF NOT EXISTS workbench.stress_test_rows (
    id                BIGSERIAL    PRIMARY KEY,
    user_id           BIGINT       NOT NULL
        REFERENCES public.users(id) ON DELETE CASCADE,
    no                INT          NOT NULL,
    excel_row         INT          NOT NULL DEFAULT 0,
    stress_category   VARCHAR(200) NOT NULL DEFAULT '',
    user_prompt_q     TEXT         NOT NULL DEFAULT '',
    intent_purpose    TEXT         NOT NULL DEFAULT '',
    expected_behavior TEXT         NOT NULL DEFAULT '',
    actual_reply      TEXT         NOT NULL DEFAULT '',
    human_summary     TEXT         NOT NULL DEFAULT '',
    notes             TEXT         NOT NULL DEFAULT '',
    result_status     VARCHAR(20)  NOT NULL DEFAULT ''
        CHECK (result_status IN ('','Cocok','Tidak')),
    politeness_score  INT          NOT NULL DEFAULT 0,
    hallucination_lvl VARCHAR(50)  NOT NULL DEFAULT '',
    media_files       TEXT[]  NOT NULL DEFAULT '{}',
    metrics           JSONB   NOT NULL DEFAULT '{}'::jsonb,
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_stress_user_no UNIQUE (user_id, no)
);

-- Index performa dashboard & antrean pending
CREATE INDEX IF NOT EXISTS idx_wb_bank_user_status
    ON workbench.bank_rows (user_id, result_status);
CREATE INDEX IF NOT EXISTS idx_wb_bank_opd
    ON workbench.bank_rows (user_id, opd, result_status);
CREATE INDEX IF NOT EXISTS idx_wb_oob_user_status
    ON workbench.out_of_bank_rows (user_id, result_status);
CREATE INDEX IF NOT EXISTS idx_wb_stress_user_status
    ON workbench.stress_test_rows (user_id, result_status);

COMMIT;
