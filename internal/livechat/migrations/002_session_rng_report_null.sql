-- ============================================================================
-- Live Chat Migration 002: Dynamic seed + Optional report_text
-- Plan: PLAN_LIVECHAT_DYNAMIC.md
-- Sifat: ADITIF & non-destruktif.
-- ============================================================================

BEGIN;

-- 1. Tambah kolom rng_seed per sesi untuk reproduksi audit variasi kalimat
ALTER TABLE livechat.sessions
    ADD COLUMN IF NOT EXISTS rng_seed BIGINT NOT NULL DEFAULT 0;

-- 2. Longgarkan report_text agar sah jika form penguji dikosongkan
ALTER TABLE livechat.sessions
    ALTER COLUMN report_text DROP NOT NULL;

COMMIT;
