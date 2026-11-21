package workbench

import "errors"

// exportWorkbook menghasilkan workbook xlsx dari data PostgreSQL.
// Implementasi penuh ada di Tahap 8 (export.go). Stub ini sengaja
// mengembalikan error eksplisit supaya tidak pernah "berhasil" diam-diam
// dengan file kosong.
func (s *DBStore) exportWorkbook() ([]byte, string, error) {
	return nil, "", errors.New("workbench: generator Excel belum tersedia (Tahap 8)")
}
