package livechat

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"
)

// SummarySheet adalah nama sheet pada file livechat.xlsx.
const SummarySheet = "Pengaduan"

type excelStyles struct {
	header      int
	finished    int
	failed      int
	timeout     int
	stopped     int
	running     int
	ketFinished int
	ketFailed   int
	ketTimeout  int
	ticket      int
	cellCenter  int
	cellTop     int
	cellWrap    int
}

func initLiveChatStyles(f *excelize.File) excelStyles {
	var s excelStyles
	s.header, _ = f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Color: "#FFFFFF", Size: 11},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"#1E3A8A"}, Pattern: 1},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center", WrapText: true},
		Border: []excelize.Border{
			{Type: "top", Color: "#0F172A", Style: 1},
			{Type: "bottom", Color: "#0F172A", Style: 2},
			{Type: "left", Color: "#334155", Style: 1},
			{Type: "right", Color: "#334155", Style: 1},
		},
	})
	s.finished, _ = f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Color: "#166534", Size: 11},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"#DCFCE7"}, Pattern: 1},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"},
		Border: []excelize.Border{
			{Type: "top", Color: "#BBF7D0", Style: 1},
			{Type: "bottom", Color: "#BBF7D0", Style: 1},
			{Type: "left", Color: "#BBF7D0", Style: 1},
			{Type: "right", Color: "#BBF7D0", Style: 1},
		},
	})
	s.failed, _ = f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Color: "#991B1B", Size: 11},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"#FEE2E2"}, Pattern: 1},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"},
		Border: []excelize.Border{
			{Type: "top", Color: "#FECACA", Style: 1},
			{Type: "bottom", Color: "#FECACA", Style: 1},
			{Type: "left", Color: "#FECACA", Style: 1},
			{Type: "right", Color: "#FECACA", Style: 1},
		},
	})
	s.timeout, _ = f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Color: "#92400E", Size: 11},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"#FEF3C7"}, Pattern: 1},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"},
		Border: []excelize.Border{
			{Type: "top", Color: "#FDE68A", Style: 1},
			{Type: "bottom", Color: "#FDE68A", Style: 1},
			{Type: "left", Color: "#FDE68A", Style: 1},
			{Type: "right", Color: "#FDE68A", Style: 1},
		},
	})
	s.stopped, _ = f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Color: "#475569", Size: 11},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"#F1F5F9"}, Pattern: 1},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"},
		Border: []excelize.Border{
			{Type: "top", Color: "#E2E8F0", Style: 1},
			{Type: "bottom", Color: "#E2E8F0", Style: 1},
			{Type: "left", Color: "#E2E8F0", Style: 1},
			{Type: "right", Color: "#E2E8F0", Style: 1},
		},
	})
	s.running, _ = f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Color: "#1E40AF", Size: 11},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"#DBEAFE"}, Pattern: 1},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"},
		Border: []excelize.Border{
			{Type: "top", Color: "#BFDBFE", Style: 1},
			{Type: "bottom", Color: "#BFDBFE", Style: 1},
			{Type: "left", Color: "#BFDBFE", Style: 1},
			{Type: "right", Color: "#BFDBFE", Style: 1},
		},
	})
	s.ketFinished, _ = f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: false, Color: "#15803D", Size: 10.5},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"#F0FDF4"}, Pattern: 1},
		Alignment: &excelize.Alignment{Horizontal: "left", Vertical: "center", WrapText: true},
	})
	s.ketFailed, _ = f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: false, Color: "#B91C1C", Size: 10.5},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"#FEF2F2"}, Pattern: 1},
		Alignment: &excelize.Alignment{Horizontal: "left", Vertical: "center", WrapText: true},
	})
	s.ketTimeout, _ = f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: false, Color: "#B45309", Size: 10.5},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"#FFFBEB"}, Pattern: 1},
		Alignment: &excelize.Alignment{Horizontal: "left", Vertical: "center", WrapText: true},
	})
	s.ticket, _ = f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Color: "#1E3A8A", Size: 11},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"},
	})
	s.cellCenter, _ = f.NewStyle(&excelize.Style{
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"},
	})
	s.cellTop, _ = f.NewStyle(&excelize.Style{
		Alignment: &excelize.Alignment{Vertical: "top"},
	})
	s.cellWrap, _ = f.NewStyle(&excelize.Style{
		Alignment: &excelize.Alignment{Vertical: "top", WrapText: true},
	})
	return s
}

// LiveChatFilter defines filter criteria when exporting livechat sessions to Excel.
type LiveChatFilter struct {
	CategoryMode string
	Status       string
	DateFrom     time.Time
	DateTo       time.Time
}

// FillSummarySheet writes the "Pengaduan" sheet into an existing excelize.File.
// If the sheet does not exist in f, it creates it. If the livechat schema or store is nil,
// it returns nil without error.
func FillSummarySheet(ctx context.Context, f *excelize.File, store *Store, userID int64, filter ...LiveChatFilter) error {
	if f == nil || store == nil || userID <= 0 {
		return nil
	}

	var flt LiveChatFilter
	if len(filter) > 0 {
		flt = filter[0]
	}

	idx, err := f.GetSheetIndex(SummarySheet)
	if err != nil || idx < 0 {
		_, _ = f.NewSheet(SummarySheet)
	}

	styles := initLiveChatStyles(f)

	// Ambil seluruh sesi milik user (batas atas tinggi agar tidak terpotong).
	sessions, err := store.ListSessions(ctx, userID, 100000)
	if err != nil {
		for i, h := range summaryHeader {
			cell, _ := excelize.CoordinatesToCellName(i+1, 1)
			_ = f.SetCellValue(SummarySheet, cell, h)
			if styles.header > 0 {
				_ = f.SetCellStyle(SummarySheet, cell, cell, styles.header)
			}
		}
		return nil
	}
	// ListSessions mengembalikan terbaru lebih dulu; Excel historis dibaca
	// naik (sesi terlama di atas).
	for i, j := 0, len(sessions)-1; i < j; i, j = i+1, j-1 {
		sessions[i], sessions[j] = sessions[j], sessions[i]
	}

	// Filter sessions by category/mode, status, and dates
	if flt.CategoryMode != "" && flt.CategoryMode != "all" {
		var matched []Session
		for _, s := range sessions {
			if strings.EqualFold(string(s.CategoryMode), flt.CategoryMode) {
				matched = append(matched, s)
			}
		}
		sessions = matched
	}
	if flt.Status != "" && flt.Status != "all" {
		var matched []Session
		for _, s := range sessions {
			if strings.EqualFold(string(s.Status), flt.Status) ||
				(strings.EqualFold(flt.Status, "Cocok") && s.Status == StatusFinished) ||
				(strings.EqualFold(flt.Status, "Tidak") && (s.Status == StatusFailed || s.Status == StatusTimeout)) {
				matched = append(matched, s)
			}
		}
		sessions = matched
	}
	if !flt.DateFrom.IsZero() {
		var matched []Session
		for _, s := range sessions {
			if !s.CreatedAt.Before(flt.DateFrom) {
				matched = append(matched, s)
			}
		}
		sessions = matched
	}
	if !flt.DateTo.IsZero() {
		var matched []Session
		for _, s := range sessions {
			if !s.CreatedAt.After(flt.DateTo) {
				matched = append(matched, s)
			}
		}
		sessions = matched
	}

	assets, err := store.ListAssets(ctx, userID)
	labels := make(map[int64]string, len(assets))
	if err == nil {
		for _, a := range assets {
			labels[a.ID] = a.Label
		}
	}

	sessionIDs := make([]int64, len(sessions))
	for i, s := range sessions {
		sessionIDs[i] = s.ID
	}
	confirmations, _ := store.ListConfirmationSummaries(ctx, sessionIDs)

	// Header di baris 1.
	_ = f.SetRowHeight(SummarySheet, 1, 28)
	for i, h := range summaryHeader {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		if err := f.SetCellValue(SummarySheet, cell, h); err != nil {
			return err
		}
		if styles.header > 0 {
			_ = f.SetCellStyle(SummarySheet, cell, cell, styles.header)
		}
	}

	for i, s := range sessions {
		row := i + 2
		confText := confirmations[s.ID]
		if confText == "" {
			confText = "-"
		}
		values := []any{
			i + 1, // No (A)
			excelKeterangan(s), // Keterangan (B) - di sebelah kiri Tanggal Sesi
			s.CreatedAt.Format("2006-01-02 15:04"), // Tanggal Sesi (C)
			s.WAPhone, // Nomor WA Penguji (D)
			s.BotPhone, // Nomor WA Bot (E)
			string(s.CategoryMode), // Kategori Mode (F)
			s.Title, // Judul Pengaduan (G)
			s.TotalTurns, // Jumlah Giliran (H)
			string(s.Status), // Status Akhir (I)
			s.EndReason, // Alasan Selesai (J)
			s.TicketNo, // Nomor Tiket (K)
			confText, // Ringkasan Konfirmasi Bot (L)
			s.LastChatExcerpt, // Chat Terakhir (M)
			excelBerkasTerkirim(s.AssetIDs, labels), // Berkas Terkirim (N)
		}
		for c, v := range values {
			cell, _ := excelize.CoordinatesToCellName(c+1, row)
			if err := f.SetCellValue(SummarySheet, cell, v); err != nil {
				return fmt.Errorf("tulis baris %d kolom %d: %w", row, c+1, err)
			}
		}

		// Apply cell styles per column
		noCell, _ := excelize.CoordinatesToCellName(1, row)
		_ = f.SetCellStyle(SummarySheet, noCell, noCell, styles.cellCenter)

		// Kolom B: Keterangan
		ketCell, _ := excelize.CoordinatesToCellName(2, row)
		switch s.Status {
		case StatusFinished:
			_ = f.SetCellStyle(SummarySheet, ketCell, ketCell, styles.ketFinished)
		case StatusFailed:
			_ = f.SetCellStyle(SummarySheet, ketCell, ketCell, styles.ketFailed)
		case StatusTimeout:
			_ = f.SetCellStyle(SummarySheet, ketCell, ketCell, styles.ketTimeout)
		default:
			_ = f.SetCellStyle(SummarySheet, ketCell, ketCell, styles.cellTop)
		}

		// Kolom C, D, E, F: Center
		for c := 3; c <= 6; c++ {
			cell, _ := excelize.CoordinatesToCellName(c, row)
			_ = f.SetCellStyle(SummarySheet, cell, cell, styles.cellCenter)
		}

		// Kolom G: Judul (Top)
		gCell, _ := excelize.CoordinatesToCellName(7, row)
		_ = f.SetCellStyle(SummarySheet, gCell, gCell, styles.cellTop)

		// Kolom H: Turns (Center)
		hCell, _ := excelize.CoordinatesToCellName(8, row)
		_ = f.SetCellStyle(SummarySheet, hCell, hCell, styles.cellCenter)

		// Kolom I: Status Akhir (Berwarna: Hijau untuk berhasil, Merah untuk gagal, Amber untuk timeout)
		statusCell, _ := excelize.CoordinatesToCellName(9, row)
		switch s.Status {
		case StatusFinished:
			_ = f.SetCellStyle(SummarySheet, statusCell, statusCell, styles.finished)
		case StatusFailed:
			_ = f.SetCellStyle(SummarySheet, statusCell, statusCell, styles.failed)
		case StatusTimeout:
			_ = f.SetCellStyle(SummarySheet, statusCell, statusCell, styles.timeout)
		case StatusStopped:
			_ = f.SetCellStyle(SummarySheet, statusCell, statusCell, styles.stopped)
		default:
			_ = f.SetCellStyle(SummarySheet, statusCell, statusCell, styles.running)
		}

		// Kolom J: Alasan Selesai (Top)
		jCell, _ := excelize.CoordinatesToCellName(10, row)
		_ = f.SetCellStyle(SummarySheet, jCell, jCell, styles.cellTop)

		// Kolom K: Nomor Tiket (Bold Indigo Center)
		ticketCell, _ := excelize.CoordinatesToCellName(11, row)
		if s.TicketNo != "" {
			_ = f.SetCellStyle(SummarySheet, ticketCell, ticketCell, styles.ticket)
		} else {
			_ = f.SetCellStyle(SummarySheet, ticketCell, ticketCell, styles.cellCenter)
		}

		// Kolom L & M: Wrap text + Top
		lCell, _ := excelize.CoordinatesToCellName(12, row)
		mCell, _ := excelize.CoordinatesToCellName(13, row)
		_ = f.SetCellStyle(SummarySheet, lCell, lCell, styles.cellWrap)
		_ = f.SetCellStyle(SummarySheet, mCell, mCell, styles.cellWrap)

		// Kolom N: Berkas (Top)
		nCell, _ := excelize.CoordinatesToCellName(14, row)
		_ = f.SetCellStyle(SummarySheet, nCell, nCell, styles.cellTop)
	}

	_ = f.SetColWidth(SummarySheet, "A", "A", 6)
	_ = f.SetColWidth(SummarySheet, "B", "B", 28)
	_ = f.SetColWidth(SummarySheet, "C", "C", 18)
	_ = f.SetColWidth(SummarySheet, "D", "D", 18)
	_ = f.SetColWidth(SummarySheet, "E", "E", 18)
	_ = f.SetColWidth(SummarySheet, "F", "F", 16)
	_ = f.SetColWidth(SummarySheet, "G", "G", 34)
	_ = f.SetColWidth(SummarySheet, "H", "H", 14)
	_ = f.SetColWidth(SummarySheet, "I", "I", 16)
	_ = f.SetColWidth(SummarySheet, "J", "J", 28)
	_ = f.SetColWidth(SummarySheet, "K", "K", 16)
	_ = f.SetColWidth(SummarySheet, "L", "L", 52)
	_ = f.SetColWidth(SummarySheet, "M", "M", 38)
	_ = f.SetColWidth(SummarySheet, "N", "N", 24)

	return nil
}

// GenerateExcel membangun workbook ringkasan Live Chat di memori dari isi
// schema livechat. Tidak ada file yang dibaca atau ditulis: seluruh baris
// berasal dari tabel livechat.sessions (dengan label aset dari livechat.assets).
//
// Kolom mengikuti plan §9 (A..N). Baris diurutkan kronologis (created_at, id)
// agar nomor urut "No" stabil antar unduhan. Semua status ditulis, termasuk
// failed/timeout/stopped.
func GenerateExcel(ctx context.Context, store *Store, userID int64, filter ...LiveChatFilter) ([]byte, string, error) {
	if store == nil {
		return nil, "", errors.New("livechat: store nil")
	}
	if userID <= 0 {
		return nil, "", errors.New("user id tidak valid")
	}

	f := excelize.NewFile()
	defer f.Close()
	_ = f.SetSheetName("Sheet1", SummarySheet)

	if err := FillSummarySheet(ctx, f, store, userID, filter...); err != nil {
		return nil, "", err
	}

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
