package livechat

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/xuri/excelize/v2"
)

// ExcelFileName is the per-user summary file. It is intentionally different
// from the existing workbook.xlsx to keep the test sheets untouched (plan §9).
const ExcelFileName = "livechat.xlsx"

// Workbook columns (plan §9). The order is fixed because the UI download and
// the tests both assume it.
var summaryHeader = []string{
	"No", "Tanggal Sesi", "Nomor WA Penguji", "Nomor WA Bot", "Kategori Mode",
	"Judul Pengaduan", "Jumlah Giliran", "Status Akhir", "Alasan Selesai",
	"Nomor Tiket", "Keterangan", "Chat Terakhir", "Berkas Terkirim",
}

// Exporter writes one row per session to data/users/{id}/livechat.xlsx. Writes
// are atomic (.tmp + rename) and serialised per file so a burst of FinishSession
// calls cannot corrupt the file.
type Exporter struct {
	mu   sync.Mutex
	root string // optional override for tests; empty = data/users
}

// NewExporter returns a fresh exporter. Every Export reads the file from disk
// first, so we never carry an in-memory workbook that could overwrite manual
// edits or run out of date across restarts/processes.
func NewExporter() *Exporter { return &Exporter{} }

// NewExporterAt is the test-friendly constructor that anchors writes to a custom
// root directory (e.g. t.TempDir()). It never touches data/users in tests.
func NewExporterAt(root string) *Exporter { return &Exporter{root: root} }

func (e *Exporter) pathFor(userID int64) string {
	if e.root != "" {
		return filepath.Join(e.root, fmt.Sprintf("%d", userID), ExcelFileName)
	}
	return WorkbookPath(userID)
}

// WorkbookPath returns data/users/{id}/livechat.xlsx.
func WorkbookPath(userID int64) string {
	return filepath.Join("data", "users", fmt.Sprintf("%d", userID), ExcelFileName)
}

// Export writes or updates one row for session. It is safe to call twice for
// the same session (the row is keyed by session id in the hidden "ID" column at
// Z so reruns do not duplicate the history). All status are written, including
// failed/timeout/stopped (plan §6.3).
func (e *Exporter) Export(ctx context.Context, userID int64, session Session) error {
	if userID <= 0 || session.ID <= 0 {
		return errors.New("sesi tidak valid untuk export excel")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	path := e.pathFor(userID)
	e.mu.Lock()
	defer e.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}

	f, err := e.openOrCreateLocked(path)
	if err != nil {
		return err
	}
	defer f.Close()

	const sheet = "Ringkasan Live Chat"
	if idx, err := f.GetSheetIndex(sheet); err != nil || idx < 0 {
		if _, err := f.NewSheet(sheet); err != nil {
			return err
		}
	} else {
		// NewFile starts with a default "Sheet1"; remove it once we have our own.
		if _, err := f.GetSheetIndex("Sheet1"); err == nil && sheet != "Sheet1" {
			_ = f.DeleteSheet("Sheet1")
		}
	}
	// Header (idempotent).
	for i, h := range summaryHeader {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		_ = f.SetCellValue(sheet, cell, h)
	}
	// Hidden ID column lets us upsert a row without duplicating.
	_ = f.SetCellValue(sheet, "Z1", "session_id")

	row, err := e.findRowBySessionID(f, sheet, session.ID)
	if err != nil {
		return err
	}
	if row == 0 {
		row = e.nextRow(f, sheet)
	}

	values := []any{
		row - 1, // No (1-based over data rows)
		session.CreatedAt.Format("2006-01-02 15:04"),
		session.WAPhone,
		session.BotPhone,
		string(session.CategoryMode),
		session.Title,
		session.TotalTurns,
		string(session.Status),
		session.EndReason,
		session.TicketNo,
		excelKeterangan(session),
		session.LastChatExcerpt,
		excelBerkasTerkirim(session.AssetIDs),
	}
	for i, v := range values {
		cell, _ := excelize.CoordinatesToCellName(i+1, row)
		_ = f.SetCellValue(sheet, cell, v)
	}
	idCell, _ := excelize.CoordinatesToCellName(26, row) // col Z
	_ = f.SetCellValue(sheet, idCell, session.ID)

	// Wrap long text so the chat excerpt stays readable.
	_ = f.SetColWidth(sheet, "F", "F", 34)
	_ = f.SetColWidth(sheet, "I", "I", 32)
	_ = f.SetColWidth(sheet, "L", "L", 48)
	_ = f.SetColWidth(sheet, "M", "M", 24)

	return writeAtomic(f, path)
}

// openOrCreateLocked reads the previous file (if any). A corrupt file is moved
// aside (never deleted) so the original can be recovered manually.
func (e *Exporter) openOrCreateLocked(path string) (*excelize.File, error) {
	f, err := excelize.OpenFile(path)
	if err == nil {
		return f, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		_ = os.Rename(path, path+".corrupt-"+time.Now().Format("20060102-150405"))
	}
	return excelize.NewFile(), nil
}

func (e *Exporter) findRowBySessionID(f *excelize.File, sheet string, id int64) (int, error) {
	rows, err := f.GetRows(sheet)
	if err != nil {
		return 0, err
	}
	want := fmt.Sprintf("%d", id)
	for i, cols := range rows {
		if i == 0 || len(cols) < 26 {
			continue
		}
		if strings.TrimSpace(cols[25]) == want {
			return i + 1, nil
		}
	}
	return 0, nil
}

func (e *Exporter) nextRow(f *excelize.File, sheet string) int {
	rows, err := f.GetRows(sheet)
	if err != nil {
		return 2
	}
	return len(rows) + 1
}

// excelKeterangan fills the "Keterangan" column for non-finished statuses so a
// reader can tell why the session ended (plan §9).
func excelKeterangan(s Session) string {
	switch s.Status {
	case StatusFinished:
		return ""
	case StatusTimeout:
		return "Batas giliran / tidak ada balasan bot: " + strings.TrimSpace(s.EndReason)
	case StatusFailed:
		return "Gagal: " + strings.TrimSpace(s.EndReason)
	case StatusStopped:
		return "Dihentikan manual: " + strings.TrimSpace(s.EndReason)
	case StatusRunning:
		return "Sesi masih berjalan"
	}
	return strings.TrimSpace(s.EndReason)
}

func excelBerkasTerkirim(ids []int64) string {
	if len(ids) == 0 {
		return ""
	}
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, fmt.Sprintf("asset#%d", id))
	}
	return strings.Join(parts, ", ")
}

// writeAtomic mirrors internal/excel/store.go's writeWorkbookAtomically
// (duplicated rather than imported to keep the module boundary clean).
func writeAtomic(f *excelize.File, destPath string) error {
	tmpPath := destPath + ".tmp"
	out, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("create temp excel: %w", err)
	}
	if err := f.Write(out); err != nil {
		out.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write temp excel: %w", err)
	}
	if err := out.Sync(); err != nil {
		out.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("sync temp excel: %w", err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close temp excel: %w", err)
	}
	return os.Rename(tmpPath, destPath)
}
