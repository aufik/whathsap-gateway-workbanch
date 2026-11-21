package workbench

import "whatshap-gateway/internal/excel"

// Assert kompilasi: implementasi mana pun WAJIB memenuhi Store.
//
// Bila salah satu baris di bawah gagal compile, definisi interface di store.go
// yang salah - bukan kode pemanggilnya. Ini mencegah mismatch baru ditemukan
// saat wiring runner/web (Tahap 10).
//
// DBStore ditambahkan di sini pada Tahap 7 saat implementasinya ada.
var _ Store = (*excel.Store)(nil)
