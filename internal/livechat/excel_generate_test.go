package livechat

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

// seedFourStatuses membuat 4 sesi (finished/timeout/failed/stopped) untuk user
// sintetis agar kolom Keterangan bisa diverifikasi untuk setiap status.
func seedFourStatuses(t *testing.T, st *Store, db *sql.DB, userID int64) []int64 {
	t.Helper()
	ctx := context.Background()
	cleanupUser(t, db, userID)

	cases := []struct {
		status Status
		reason string
		ticket string
	}{
		{StatusFinished, "laporan anda telah kami terima", "385692"},
		{StatusTimeout, "batas giliran tercapai", ""},
		{StatusFailed, "koneksi WhatsApp terputus", ""},
		{StatusStopped, "dihentikan pengguna", ""},
	}
	var ids []int64
	for i, c := range cases {
		in := sampleInput()
		in.Title = "Sesi " + string(c.status)
		sess, err := st.CreateSession(ctx, userID, in)
		if err != nil {
			t.Fatalf("create #%d: %v", i, err)
		}
		if err := st.FinishSession(ctx, userID, sess.ID, c.status, c.reason, c.ticket, "bubble1\nbubble2\nbubble3"); err != nil {
			t.Fatalf("finish #%d: %v", i, err)
		}
		ids = append(ids, sess.ID)
	}
	return ids
}

// TestGenerateExcelAllStatuses memastikan seluruh status tertulis beserta
// Keterangan yang sesuai, dan kolom A..M terisi.
func TestGenerateExcelAllStatuses(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	st := NewStore(db)
	ctx := context.Background()
	const userID int64 = 900950
	defer cleanupUser(t, db, userID)

	seedFourStatuses(t, st, db, userID)

	raw, name, err := GenerateExcel(ctx, st, userID)
	if err != nil {
		t.Fatalf("GenerateExcel: %v", err)
	}
	if name != ExcelFileName {
		t.Fatalf("nama file: %q", name)
	}
	if len(raw) < 1000 {
		t.Fatalf("workbook terlalu kecil: %d byte", len(raw))
	}

	f, err := excelize.OpenReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("hasil bukan xlsx valid: %v", err)
	}
	defer f.Close()

	rows, err := f.GetRows(SummarySheet)
	if err != nil {
		t.Fatalf("baca sheet: %v", err)
	}
	if len(rows) != 5 { // 1 header + 4 sesi
		t.Fatalf("jumlah baris: %d, want 5", len(rows))
	}
	for i, want := range summaryHeader {
		if rows[0][i] != want {
			t.Fatalf("header kolom %d = %q, want %q", i, rows[0][i], want)
		}
	}
	wantKeterangan := map[string]string{
		"finished": "",
		"timeout":  "Batas giliran / tidak ada balasan bot: batas giliran tercapai",
		"failed":   "Gagal: koneksi WhatsApp terputus",
		"stopped":  "Dihentikan manual: dihentikan pengguna",
	}
	for _, r := range rows[1:] {
		status := r[8]
		if got := r[1]; got != wantKeterangan[status] {
			t.Fatalf("status=%s keterangan=%q, want %q", status, got, wantKeterangan[status])
		}
		if r[0] == "" || r[2] == "" || r[3] == "" || r[4] == "" || r[6] == "" {
			t.Fatalf("kolom wajib kosong pada status=%s: %v", status, r)
		}
		if r[12] == "" {
			t.Fatalf("kolom M (chat terakhir) kosong untuk status=%s", status)
		}
	}
	// Verify custom cell style (colors) is applied to status cells (column I)
	for rowIdx := 2; rowIdx <= 5; rowIdx++ {
		cell := fmt.Sprintf("I%d", rowIdx)
		styleID, err := f.GetCellStyle(SummarySheet, cell)
		if err != nil || styleID == 0 {
			t.Fatalf("cell %s must have a colored status style, got styleID=%d, err=%v", cell, styleID, err)
		}
	}
}

// TestGenerateExcelUsesAssetLabels memastikan kolom M memakai label aset.
func TestGenerateExcelUsesAssetLabels(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	st := NewStore(db)
	ctx := context.Background()
	const userID int64 = 900951
	defer cleanupUser(t, db, userID)

	raw, err := st.CreateAsset(ctx, userID, AssetInput{
		Kind: AssetImage, Label: "Foto Jalan Rusak", FileName: "foto.png",
		StoredPath: "ab/ab.png", Mimetype: "image/png", SizeBytes: 10,
		SHA256: "abababababababababababababababababababababababababababababababab",
	})
	if err != nil {
		t.Fatalf("create asset: %v", err)
	}
	in := sampleInput()
	in.AssetIDs = []int64{raw.ID}
	sess, err := st.CreateSession(ctx, userID, in)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := st.FinishSession(ctx, userID, sess.ID, StatusFinished, "diterima", "123456", "x"); err != nil {
		t.Fatalf("finish: %v", err)
	}

	data, _, err := GenerateExcel(ctx, st, userID)
	if err != nil {
		t.Fatalf("GenerateExcel: %v", err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	cell, _ := f.GetCellValue(SummarySheet, "N2")
	if cell != "Foto Jalan Rusak" {
		t.Fatalf("kolom N = %q, want %q", cell, "Foto Jalan Rusak")
	}
}

// TestGenerateExcelIncludesConfirmationMessage verifies that bot confirmation recap appears in column L.
func TestGenerateExcelIncludesConfirmationMessage(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	st := NewStore(db)
	const userID int64 = 900953
	cleanupUser(t, db, userID)
	defer cleanupUser(t, db, userID)

	ctx := context.Background()
	sess, err := st.CreateSession(ctx, userID, sampleInput())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	confQuestion := "*Baik*, *Lampiran Berkas* dilewati.\n\nBerikut ringkasan aduan Anda:\n• *Nama Pengadu*: Nurul Aini\n• *Judul Laporan*: Saya Nurul Aini pak\nApakah sudah sesuai untuk dikirim?\n# 171158"
	if _, err := st.InsertTurn(ctx, userID, Turn{
		SessionID:    sess.ID,
		TurnNo:       1,
		BotQuestion:  confQuestion,
		QuestionKind: "konfirmasi",
		OurAnswer:    "Ya, sudah sesuai",
		CategoryMode: "benar",
		MatchStatus:  "cocok",
	}); err != nil {
		t.Fatalf("insert turn: %v", err)
	}
	if err := st.FinishSession(ctx, userID, sess.ID, StatusFinished, "tiket terbit", "171158", "Selesai"); err != nil {
		t.Fatalf("finish: %v", err)
	}

	data, _, err := GenerateExcel(ctx, st, userID)
	if err != nil {
		t.Fatalf("GenerateExcel: %v", err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()

	colLHeader, _ := f.GetCellValue(SummarySheet, "L1")
	if colLHeader != "Ringkasan Konfirmasi Bot" {
		t.Fatalf("L1 header = %q, want %q", colLHeader, "Ringkasan Konfirmasi Bot")
	}

	colLVal, _ := f.GetCellValue(SummarySheet, "L2")
	if !strings.Contains(colLVal, "Berikut ringkasan aduan Anda") || !strings.Contains(colLVal, "Nurul Aini") {
		t.Fatalf("L2 confirmation text mismatch: got %q", colLVal)
	}
}

// TestGenerateExcelEmptyUser memastikan user tanpa sesi tetap menghasilkan
// workbook valid berisi header saja (dipakai handler sebelum cek 404).
func TestGenerateExcelEmptyUser(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	st := NewStore(db)
	const userID int64 = 900952
	cleanupUser(t, db, userID)
	defer cleanupUser(t, db, userID)

	raw, _, err := GenerateExcel(context.Background(), st, userID)
	if err != nil {
		t.Fatalf("GenerateExcel: %v", err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	rows, _ := f.GetRows(SummarySheet)
	if len(rows) != 1 {
		t.Fatalf("user kosong harus 1 baris header, dapat %d", len(rows))
	}
}

// TestGenerateExcelDoesNotTouchDisk memastikan generator tidak menulis file
// livechat.xlsx (persyaratan Tahap 11).
func TestGenerateExcelDoesNotTouchDisk(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	st := NewStore(db)
	const userID int64 = 900953
	cleanupUser(t, db, userID)
	defer cleanupUser(t, db, userID)
	seedFourStatuses(t, st, db, userID)

	dir := t.TempDir()
	before, _ := os.ReadDir(dir)
	if _, _, err := GenerateExcel(context.Background(), st, userID); err != nil {
		t.Fatalf("GenerateExcel: %v", err)
	}
	after, _ := os.ReadDir(dir)
	if len(after) != len(before) {
		t.Fatalf("generator menulis file: before=%d after=%d", len(before), len(after))
	}
	// Dan file livechat.xlsx tidak diciptakan di path standar.
	if _, err := os.Stat(filepath.Join("data", "users", "900953", ExcelFileName)); err == nil {
		t.Fatalf("file livechat.xlsx tidak boleh dibuat")
	}
}
