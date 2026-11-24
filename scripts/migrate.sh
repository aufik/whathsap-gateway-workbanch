#!/usr/bin/env bash
# ==============================================================================
# Script Migrasi Database & Seeding Akun WhatsApp Gateway Workbench
# Mendukung eksekusi via psql lokal atau container Docker postgres
# ==============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(dirname "$SCRIPT_DIR")"
SQL_FILE="${ROOT_DIR}/migration_master.sql"

if [ ! -f "$SQL_FILE" ]; then
    echo "❌ Error: Berkas SQL tidak ditemukan di $SQL_FILE"
    exit 1
fi

echo "======================================================================"
echo "🚀 Memulai Migrasi Database: WhatsApp Gateway Workbench"
echo "======================================================================"

# Parameter default VPS (dapat di-override via environment variable)
DB_USER="${DB_USER:-webchat_user}"
DB_PASSWORD="${DB_PASSWORD:-iXZrvEt8zMlnn8PgS6}"
DB_HOST="${DB_HOST:-127.0.0.1}"
DB_PORT="${DB_PORT:-5432}"
DB_NAME="${DB_NAME:-whatshap-workbanch}"

# Cek apakah psql tersedia di host
if command -v psql >/dev/null 2>&1; then
    echo "📡 Menghubungi PostgreSQL host via psql (${DB_USER}@${DB_HOST}:${DB_PORT}/${DB_NAME})..."
    PGPASSWORD="${DB_PASSWORD}" psql -h "${DB_HOST}" -p "${DB_PORT}" -U "${DB_USER}" -d "${DB_NAME}" -f "${SQL_FILE}"
    echo "✅ Migrasi dan seeding via psql host berhasil!"
else
    # Fallback: cari container docker postgres
    PG_CONTAINER="$(docker ps --filter "name=postgres" --format "{{.Names}}" | head -n 1 || true)"
    if [ -n "$PG_CONTAINER" ]; then
        echo "🐳 psql tidak ditemukan di host. Menggunakan container Docker: ${PG_CONTAINER}..."
        docker exec -i "${PG_CONTAINER}" psql -U "${DB_USER}" -d "${DB_NAME}" < "${SQL_FILE}"
        echo "✅ Migrasi dan seeding via container ${PG_CONTAINER} berhasil!"
    else
        echo "❌ Error: Tidak dapat menemukan 'psql' di host maupun container Docker postgres."
        echo "Silakan install postgresql-client atau jalankan melalui docker exec."
        exit 1
    fi
fi

echo "======================================================================"
echo "🎉 SEMUA TABEL & 2 AKUN BERHASIL DIMIGRASI:"
echo "   1. superadmin (password: 12345)"
echo "   2. kamil      (password: 12345)"
echo "   Database target: ${DB_NAME}"
echo "======================================================================"
