package livechat

import "testing"

func TestDetectQuestionKindTable(t *testing.T) {
	cases := []struct {
		in   string
		want QuestionKind
	}{
		{"Silakan isi nama lengkap Anda.", KindNama},
		{"Siapa nama pengadu ya?", KindNama},
		{"Mohon isi laporan pengaduan Anda.", KindIsiLaporan},
		{"Silakan tuliskan aduan yang ingin dilaporkan.", KindIsiLaporan},
		{"Tanggal kejadian (format: YYYY-MM-DD)?", KindTanggal},
		{"Kapan kejadian itu terjadi?", KindTanggal},
		{"Alamat kejadian lengkap?", KindAlamat},
		{"Dimana alamat lengkapnya?", KindAlamat},
		{"Silakan kirim titik lokasi atau koordinat.", KindTitikLokasi},
		{"Bisa share lokasi kejadian?", KindTitikLokasi},
		{"Mohon unggah bukti foto lokasi.", KindBerkas},
		{"Silakan lampirkan dokumen pendukung.", KindBerkas},
		{"Apakah Anda ingin mengirim pengaduan sekarang? ya/tidak", KindKonfirmasi},
		{"Konfirmasi data sudah benar?", KindKonfirmasi},
		{"Halo kak, ada yang bisa dibantu?", KindLainnya},
		{"", KindUnknown},
	}
	for _, c := range cases {
		if got := DetectQuestionKind(c.in); got != c.want {
			t.Fatalf("Detect(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDetectSpecificityWins(t *testing.T) {
	// Message mentions both "pengaduan" and "nama lengkap"; name wins.
	got := DetectQuestionKind("Silakan isi nama lengkap untuk pengaduan ini")
	if got != KindNama {
		t.Fatalf("expected KindNama (more specific), got %q", got)
	}
	// Message mentions "pengaduan" and "tanggal kejadian"; date wins.
	got = DetectQuestionKind("Untuk kelengkapan pengaduan, isi tanggal kejadian")
	if got != KindTanggal {
		t.Fatalf("expected KindTanggal, got %q", got)
	}
}

func TestIsDoneSignals(t *testing.T) {
	cases := map[string]bool{
		"Pengaduan Anda telah kami terima. Nomor tiket #385692": true,
		"Laporan anda telah kami terima, terima kasih":          true,
		"Pengaduan berhasil dikirim ke dinas terkait":           true,
		"Nomor tiket Anda adalah #4821":                         true, // 4 digits also caught by direct
		"Tiket Anda #385692 sudah masuk":                        true,
		"Silakan isi nama lengkap":                              false,
		"Mohon unggah bukti foto":                               false,
	}
	for text, want := range cases {
		gotDone, reason := IsDone(text)
		if gotDone != want {
			t.Fatalf("IsDone(%q) = %v (reason=%q), want %v", text, gotDone, reason, want)
		}
		if gotDone && reason == "" {
			t.Fatalf("IsDone(%q) returned true with empty reason", text)
		}
	}
}

func TestExtractTicket(t *testing.T) {
	cases := map[string]string{
		"Nomor tiket #385692":            "385692",
		"tiket anda # 12345 sudah masuk": "12345",
		"#1234":                          "", // 4 digits under minimum
		"#9999999":                       "", // 7 digits over maximum
		"tidak ada tiket":                "",
	}
	for in, want := range cases {
		if got := ExtractTicket(in); got != want {
			t.Fatalf("ExtractTicket(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLastNExcerpt(t *testing.T) {
	bodies := []string{"pembuka", "", "tanya nama", "jawab", "tiket #385692"}
	got := LastNExcerpt(bodies, 3)
	want := "tanya nama\njawab\ntiket #385692"
	if got != want {
		t.Fatalf("LastNExcerpt = %q, want %q", got, want)
	}
	if got := LastNExcerpt(nil, 3); got != "" {
		t.Fatalf("empty input should yield empty string, got %q", got)
	}
}

func TestNormalizeForDetectHandlesUnicode(t *testing.T) {
	// Em-dash + curly quote are normalised; whitespace collapses.
	in := "Pengaduan\u2014\u201cberhasil\u201d   diterima"
	if got := NormalizeForDetect(in); got != `pengaduan-"berhasil" diterima` {
		t.Fatalf("got %q", got)
	}
}
