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
	"No", "Keterangan", "Tanggal Sesi", "Nomor WA Penguji", "Nomor WA Bot", "Kategori Mode",
	"Judul Pengaduan", "Jumlah Giliran", "Status Akhir", "Alasan Selesai",
	"Nomor Tiket", "Ringkasan Konfirmasi Bot", "Chat Terakhir", "Berkas Terkirim",
}

// Exporter writes one row per session to data/users/{id}/livechat.xlsx. Writes
// are atomic (.tmp + rename) and serialised per file so a burst of FinishSession
// calls cannot corrupt the file.
type Exporter struct {
	mu   sync.Mutex
	root string // optional override for tests; empty = data/users
	// AssetLabels maps asset ids to human labels so column M carries "Foto
	// Jalan Rusak" instead of "asset#12". Nil is allowed: exporter falls back
	// to ids without needing a DB round-trip.
	AssetLabels func(ctx context.Context, userID int64, ids []int64) (map[int64]string, error)
}

// NewExporter returns a fresh exporter. Every Export reads the file from disk
// first, so we never carry an in-memory workbook that could overwrite manual
// edits or run out of date across restarts/processes.
func NewExporter() *Exporter { return &Exporter{} }

// NewExporterAt is the test-friendly constructor that anchors writes to a custom
// root directory (e.g. t.TempDir()). It never touches data/users in tests.
func NewExporterAt(root string) *Exporter { return &Exporter{root: root} }

// WithAssetLabels wires the asset-name resolver used for column M. Chainable.
func (e *Exporter) WithAssetLabels(fn func(ctx context.Context, userID int64, ids []int64) (map[int64]string, error)) *Exporter {
	e.AssetLabels = fn
	return e
}

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

	const sheet = SummarySheet
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

	labels := e.labelsFor(ctx, userID, session.AssetIDs)
	values := []any{
		row - 1, // No (1-based over data rows)
		excelKeterangan(session),
		session.CreatedAt.Format("2006-01-02 15:04"),
		session.WAPhone,
		session.BotPhone,
		string(session.CategoryMode),
		session.Title,
		session.TotalTurns,
		string(session.Status),
		session.EndReason,
		session.TicketNo,
		"-",
		session.LastChatExcerpt,
		excelBerkasTerkirim(session.AssetIDs, labels),
	}
	for i, v := range values {
		cell, _ := excelize.CoordinatesToCellName(i+1, row)
		_ = f.SetCellValue(sheet, cell, v)
	}
	idCell, _ := excelize.CoordinatesToCellName(26, row) // col Z
	_ = f.SetCellValue(sheet, idCell, session.ID)

	// Wrap long text so the chat excerpt stays readable.
	_ = f.SetColWidth(sheet, "A", "A", 6)
	_ = f.SetColWidth(sheet, "B", "B", 28)
	_ = f.SetColWidth(sheet, "C", "C", 18)
	_ = f.SetColWidth(sheet, "D", "D", 18)
	_ = f.SetColWidth(sheet, "E", "E", 18)
	_ = f.SetColWidth(sheet, "F", "F", 16)
	_ = f.SetColWidth(sheet, "G", "G", 34)
	_ = f.SetColWidth(sheet, "H", "H", 14)
	_ = f.SetColWidth(sheet, "I", "I", 16)
	_ = f.SetColWidth(sheet, "J", "J", 28)
	_ = f.SetColWidth(sheet, "K", "K", 16)
	_ = f.SetColWidth(sheet, "L", "L", 52)
	_ = f.SetColWidth(sheet, "M", "M", 38)
	_ = f.SetColWidth(sheet, "N", "N", 24)

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

// excelKeterangan fills the "Keterangan" column using user custom remarks if set,
// or non-finished status descriptions so a reader can tell why the session ended (plan §9).
func excelKeterangan(s Session) string {
	if strings.TrimSpace(s.Keterangan) != "" {
		return strings.TrimSpace(s.Keterangan)
	}
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

func excelBerkasTerkirim(ids []int64, labels map[int64]string) string {
	if len(ids) == 0 {
		return ""
	}
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		if labels != nil {
			if name := strings.TrimSpace(labels[id]); name != "" {
				parts = append(parts, name)
				continue
			}
		}
		parts = append(parts, fmt.Sprintf("asset#%d", id))
	}
	return strings.Join(parts, ", ")
}

// labelsFor resolves the asset labels once per export; failures are non-fatal
// so the summary still writes (falling back to asset#id).
func (e *Exporter) labelsFor(ctx context.Context, userID int64, ids []int64) map[int64]string {
	if e.AssetLabels == nil || len(ids) == 0 {
		return nil
	}
	m, err := e.AssetLabels(ctx, userID, ids)
	if err != nil {
		return nil
	}
	return m
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
