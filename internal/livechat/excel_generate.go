package livechat

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/xuri/excelize/v2"
)

// SummarySheet adalah nama sheet pada file livechat.xlsx.
const SummarySheet = "Ringkasan Live Chat"

// GenerateExcel membangun workbook ringkasan Live Chat di memori dari isi
// schema livechat. Tidak ada file yang dibaca atau ditulis: seluruh baris
// berasal dari tabel livechat.sessions (dengan label aset dari livechat.assets).
//
// Kolom mengikuti plan §9 (A..M). Baris diurutkan kronologis (created_at, id)
// agar nomor urut "No" stabil antar unduhan. Semua status ditulis, termasuk
// failed/timeout/stopped.
func GenerateExcel(ctx context.Context, store *Store, userID int64) ([]byte, string, error) {
	if store == nil {
		return nil, "", errors.New("livechat: store nil")
	}
	if userID <= 0 {
		return nil, "", errors.New("user id tidak valid")
	}

	// Ambil seluruh sesi milik user (batas atas tinggi agar tidak terpotong).
	sessions, err := store.ListSessions(ctx, userID, 100000)
	if err != nil {
		return nil, "", err
	}
	// ListSessions mengembalikan terbaru lebih dulu; Excel historis dibaca
	// naik (sesi terlama di atas).
	for i, j := 0, len(sessions)-1; i < j; i, j = i+1, j-1 {
		sessions[i], sessions[j] = sessions[j], sessions[i]
	}

	assets, err := store.ListAssets(ctx, userID)
	if err != nil {
		return nil, "", err
	}
	labels := make(map[int64]string, len(assets))
	for _, a := range assets {
		labels[a.ID] = a.Label
	}

	f := excelize.NewFile()
	defer f.Close()
	_ = f.SetSheetName("Sheet1", SummarySheet)

	// Header di baris 1.
	for i, h := range summaryHeader {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		if err := f.SetCellValue(SummarySheet, cell, h); err != nil {
			return nil, "", err
		}
	}

	for i, s := range sessions {
		row := i + 2
		values := []any{
			i + 1, // No
			s.CreatedAt.Format("2006-01-02 15:04"),
			s.WAPhone,
			s.BotPhone,
			string(s.CategoryMode),
			s.Title,
			s.TotalTurns,
			string(s.Status),
			s.EndReason,
			s.TicketNo,
			excelKeterangan(s),
			s.LastChatExcerpt,
			excelBerkasTerkirim(s.AssetIDs, labels),
		}
		for c, v := range values {
			cell, _ := excelize.CoordinatesToCellName(c+1, row)
			if err := f.SetCellValue(SummarySheet, cell, v); err != nil {
				return nil, "", fmt.Errorf("tulis baris %d kolom %d: %w", row, c+1, err)
			}
		}
	}

	_ = f.SetColWidth(SummarySheet, "A", "A", 6)
	_ = f.SetColWidth(SummarySheet, "F", "F", 34)
	_ = f.SetColWidth(SummarySheet, "I", "I", 32)
	_ = f.SetColWidth(SummarySheet, "L", "L", 48)
	_ = f.SetColWidth(SummarySheet, "M", "M", 24)

	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		return nil, "", fmt.Errorf("tulis workbook livechat: %w", err)
	}
	return buf.Bytes(), ExcelFileName, nil
}

// ExportOnDemand adalah varian api yang selalu menghasilkan data terbaru dari
// database. Dibedakan dari (*Exporter).Export (jalur file lama) agar kedua
// perilaku bisa hidup berdampingan selama masa transisi.
func (s *Store) ExportOnDemand(ctx context.Context, userID int64) ([]byte, string, error) {
	return GenerateExcel(ctx, s, userID)
}
