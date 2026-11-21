// Package workbench menyediakan kontrak data pengujian (Bank Pertanyaan, Uji
// Diluar Bank, Uji Stress AI) yang dipakai bersama oleh implementasi file
// (internal/excel.Store, jalur lama) dan implementasi PostgreSQL
// (workbench.DBStore, jalur baru yang diaktifkan flag WORKBENCH_DB).
//
// Plan acuan: PLAN_WORKBENCH_DB.md v4, Tahap 4.
package workbench

import "whatshap-gateway/internal/model"

// Store adalah kontrak tunggal data pengujian per user.
//
// Signature di bawah disalin PERSIS dari method publik *excel.Store yang
// dipakai internal/runner dan internal/web (25 dari 28 method publik).
// MediaDir, FilePath, dan ReplaceWorkbook sengaja tidak termasuk karena tidak
// digunakan di luar paket excel.
//
// Pemenuhan kontrak dibuktikan secara kompilasi di assert.go.
type Store interface {
	// Snapshot & ringkasan (dipakai dashboard personal dan SSE state).
	Snapshot() ([]model.TestRow, model.OverallSummary)
	OutOfBankSnapshot() ([]model.OutOfBankRow, model.OutOfBankSummary)
	StressTestSnapshot() ([]model.StressTestRow, model.StressTestSummary)

	// Baca satu baris berdasarkan nomor urut (1-based, kolom A sheet).
	GetRow(no int) (model.TestRow, bool)
	GetOutOfBankRow(no int) (model.OutOfBankRow, bool)
	GetStressTestRow(no int) (model.StressTestRow, bool)

	// Antrean baris yang belum diuji (dipakai runner saat memulai batch).
	PendingRowNumbers(forceAll bool, limit int) []int
	PendingRowRange(fromNo, toNo int, forceAll bool) []int
	PendingOutOfBankNumbers(forceAll bool, limit int) []int
	PendingOutOfBankRange(fromNo, toNo int, forceAll bool) []int
	PendingStressNumbers(forceAll bool, limit int) []int
	PendingStressRange(fromNo, toNo int, forceAll bool) []int

	// Mutasi satu baris. Mengembalikan baris terbaru + ringkasan setelah update.
	UpdateRow(no int, patch model.RowPatch) (model.TestRow, model.OverallSummary, error)
	UpdateOutOfBankRow(no int, patch model.OutOfBankPatch) (model.OutOfBankRow, model.OutOfBankSummary, error)
	UpdateStressTestRow(no int, patch model.StressTestPatch) (model.StressTestRow, model.StressTestSummary, error)

	// Uji Diluar Bank: tambah, hapus, dan kosongkan.
	AppendOutOfBankRows(items []model.OutOfBankRow) ([]model.OutOfBankRow, model.OutOfBankSummary, int, error)
	DeleteOutOfBankRow(no int) ([]model.OutOfBankRow, model.OutOfBankSummary, error)
	DeleteOutOfBankBatch(numbers []int) ([]model.OutOfBankRow, model.OutOfBankSummary, error)
	ClearOutOfBank() ([]model.OutOfBankRow, model.OutOfBankSummary, error)

	// Uji Stress AI: tambah, hapus, dan kosongkan.
	AppendStressTestRows(items []model.StressTestRow) ([]model.StressTestRow, model.StressTestSummary, int, error)
	DeleteStressTestRow(no int) ([]model.StressTestRow, model.StressTestSummary, error)
	DeleteStressTestBatch(numbers []int) ([]model.StressTestRow, model.StressTestSummary, error)
	ClearStressTest() ([]model.StressTestRow, model.StressTestSummary, error)

	// Ganti seluruh workbook (jalur upload file .xlsx baru oleh user).
	ReplaceFromBytes(uploaded []byte) (model.OverallSummary, error)

	// Unduh Excel. Untuk excel.Store membaca file; untuk DBStore menghasilkan
	// workbook di memori dari data PostgreSQL.
	ExportBytes() ([]byte, string, error)
}
