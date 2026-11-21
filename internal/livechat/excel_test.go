package livechat

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"
)

// TestExporterWritesAllStatuses exercises the DoD for step 9: failed, timeout
// and stopped rows must appear too, not just finished. All writes go to temp
// dirs so data/users/* is never touched.
func TestExporterWritesAllStatuses(t *testing.T) {
	root := t.TempDir()
	ex := NewExporterAt(root)

	cases := []struct {
		id     int64
		status Status
		reason string
		ticket string
	}{
		{1, StatusFinished, "laporan anda telah kami terima", "385692"},
		{2, StatusTimeout, "batas giliran tercapai", ""},
		{3, StatusFailed, "koneksi WhatsApp terputus", ""},
		{4, StatusStopped, "dihentikan pengguna", ""},
	}
	for _, c := range cases {
		sess := Session{
			ID:              c.id,
			UserID:          900500,
			WAPhone:         "628000000001",
			BotPhone:        "628975275737",
			Title:           "Jalan rusak " + string(c.status),
			CategoryMode:    ModeBenar,
			Status:          c.status,
			EndReason:       c.reason,
			TicketNo:        c.ticket,
			TotalTurns:      7,
			CreatedAt:       time.Date(2026, 11, 20, 13, 0, 0, 0, time.UTC),
			LastChatExcerpt: "tanya nama\njawab\nselesai",
			AssetIDs:        []int64{10, 11},
		}
		if err := ex.Export(context.Background(), sess.UserID, sess); err != nil {
			t.Fatalf("export id=%d: %v", c.id, err)
		}
	}

	path := filepath.Join(root, "900500", ExcelFileName)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("workbook missing: %v", err)
	}
	f, err := excelize.OpenFile(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()

	rows, err := f.GetRows("Ringkasan Live Chat")
	if err != nil {
		t.Fatalf("read rows: %v", err)
	}
	if len(rows) != 5 { // header + 4 data rows
		t.Fatalf("expected 5 rows, got %d: %v", len(rows), rows)
	}
	// Header A..M
	for i, want := range summaryHeader {
		if rows[0][i] != want {
			t.Fatalf("header col %d = %q, want %q", i, rows[0][i], want)
		}
	}
	// Verify per-status keterangan and last excerpt presence.
	wantKeterangan := map[string]string{
		"finished": "",
		"timeout":  "Batas giliran / tidak ada balasan bot: batas giliran tercapai",
		"failed":   "Gagal: koneksi WhatsApp terputus",
		"stopped":  "Dihentikan manual: dihentikan pengguna",
	}
	for _, r := range rows[1:] {
		status := r[7]
		want := wantKeterangan[status]
		if r[10] != want {
			t.Fatalf("status=%s keterangan=%q, want %q", status, r[10], want)
		}
		if r[11] == "" {
			t.Fatalf("status=%s last_chat_excerpt missing", status)
		}
		if r[12] == "" {
			t.Fatalf("status=%s berkas terkirim missing", status)
		}
	}

	// Idempotency: exporting the same session again must update, not duplicate.
	same := cases[0]
	sess := Session{
		ID: same.id, UserID: 900500, WAPhone: "628000000001", BotPhone: "1",
		Title: "Renamed", CategoryMode: ModeBenar, Status: StatusFinished,
		TotalTurns: 9, CreatedAt: time.Now(),
	}
	if err := ex.Export(context.Background(), 900500, sess); err != nil {
		t.Fatalf("re-export: %v", err)
	}
	rows2, _ := f.GetRows("Ringkasan Live Chat") // f may be stale; reopen
	_ = rows2
	f2, _ := excelize.OpenFile(path)
	defer f2.Close()
	rows2, _ = f2.GetRows("Ringkasan Live Chat")
	if len(rows2) != 5 {
		t.Fatalf("re-export must not duplicate rows, got %d", len(rows2))
	}
	// Locate #1 row and confirm it now says "Renamed".
	found := false
	for _, r := range rows2[1:] {
		if r[0] == "1" && strings.Contains(r[5], "Renamed") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected upserted row #1 title=Renamed, got %v", rows2)
	}
}

// TestExporterLeavesExistingWorkbookUntouched confirms the exporter only writes
// livechat.xlsx and never mutates the sibling workbook.xlsx.
func TestExporterLeavesExistingWorkbookUntouched(t *testing.T) {
	root := t.TempDir()
	userDir := filepath.Join(root, "900501")
	if err := os.MkdirAll(userDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	orig := []byte("THIS_IS_NOT_A_REAL_XLSX_SO_IT_MUST_STAY_UNTOUCHED")
	if err := os.WriteFile(filepath.Join(userDir, "workbook.xlsx"), orig, 0644); err != nil {
		t.Fatalf("seed workbook: %v", err)
	}

	ex := NewExporterAt(root)
	if err := ex.Export(context.Background(), 900501, Session{
		ID: 1, UserID: 900501, WAPhone: "1", BotPhone: "1", Title: "x",
		CategoryMode: ModeBenar, Status: StatusFinished, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("export: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(userDir, "workbook.xlsx"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != string(orig) {
		t.Fatalf("workbook.xlsx was modified by livechat exporter: %q", string(got))
	}
	// livechat.xlsx exists alongside but does not belong to workbook.xlsx.
	if _, err := os.Stat(filepath.Join(userDir, ExcelFileName)); err != nil {
		t.Fatalf("livechat.xlsx missing: %v", err)
	}
}

// TestExporterUsesAssetLabels proves column M carries the label when the
// resolver returns it and falls back to asset#id otherwise.
func TestExporterUsesAssetLabels(t *testing.T) {
	root := t.TempDir()
	ex := NewExporterAt(root).WithAssetLabels(func(_ context.Context, _ int64, ids []int64) (map[int64]string, error) {
		return map[int64]string{11: "Foto Jalan Rusak"}, nil
	})
	if err := ex.Export(context.Background(), 900502, Session{
		ID: 1, UserID: 900502, WAPhone: "1", BotPhone: "1", Title: "x",
		CategoryMode: ModeBenar, Status: StatusFinished, CreatedAt: time.Now(),
		AssetIDs: []int64{11, 12},
	}); err != nil {
		t.Fatalf("export: %v", err)
	}
	f, err := excelize.OpenFile(filepath.Join(root, "900502", ExcelFileName))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	rows, _ := f.GetRows("Ringkasan Live Chat")
	got := rows[1][12] // column M
	if !strings.Contains(got, "Foto Jalan Rusak") || !strings.Contains(got, "asset#12") {
		t.Fatalf("expected labelled + fallback ids, got %q", got)
	}
}
