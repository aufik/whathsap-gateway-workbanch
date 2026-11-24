-- ============================================================================
-- Live Chat Migration 003: Soft Delete & Session Remarks (Keterangan)
-- Sifat: ADITIF & non-destruktif.
-- ============================================================================

BEGIN;

-- 1. Tambah kolom deleted_at untuk soft delete
ALTER TABLE livechat.sessions
    ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ DEFAULT NULL;

-- 2. Tambah kolom keterangan untuk catatan/keterangan setiap sesi
ALTER TABLE livechat.sessions
    ADD COLUMN IF NOT EXISTS keterangan TEXT NOT NULL DEFAULT '';

-- 3. Partial index agar query sesi aktif (belum dihapus) cepat
CREATE INDEX IF NOT EXISTS idx_sessions_user_active
    ON livechat.sessions (user_id, created_at DESC)
    WHERE deleted_at IS NULL;

COMMIT;
