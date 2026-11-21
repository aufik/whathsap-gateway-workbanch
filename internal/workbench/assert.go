package workbench

import "whatshap-gateway/internal/excel"

// Assert kompilasi: implementasi mana pun WAJIB memenuhi Store.
//
// Bila salah satu baris di bawah gagal compile, definisi interface di store.go
// yang salah - bukan kode pemanggilnya. Ini mencegah mismatch baru ditemukan
// saat wiring runner/web (Tahap 10).
var _ Store = (*excel.Store)(nil)
var _ Store = (*DBStore)(nil)
