package web

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"whatshap-gateway/internal/ai"
	"whatshap-gateway/internal/livechat"
	"whatshap-gateway/internal/model"
	"whatshap-gateway/internal/wa"
)

type aiHookAdapter struct {
	engine *ai.Engine
}

func (a *aiHookAdapter) ComposeAnswer(ctx context.Context, mode livechat.CategoryMode, kind livechat.QuestionKind, draft, botQuestion string) (string, error) {
	if a.engine == nil || !a.engine.IsLLMEnabled(ctx) {
		return "", nil
	}
	sys := buildLiveChatSystemPrompt(mode)
	usr := fmt.Sprintf("Mode: %s\nJenis Data: %s\nPertanyaan/Pesan Bot: %s\nFakta Acuan: %s\n\nBalas chat WhatsApp:",
		mode, kind, botQuestion, draft)

	temp := 0.3
	switch mode {
	case livechat.ModeTypo:
		temp = 0.5
	case livechat.ModeNgasal:
		temp = 0.7
	case livechat.ModeCelah:
		temp = 0.5
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	return a.engine.ChatReply(timeoutCtx, sys, usr, temp)
}

func buildLiveChatSystemPrompt(mode livechat.CategoryMode) string {
	base := "Kamu adalah warga Kota Medan yang sedang membalas chat WhatsApp ke Bot Layanan Pengaduan Pemko Medan.\n" +
		"Tugasmu: Balas pertanyaan bot dalam 1 kalimat chat WhatsApp pendek (maksimal 15-20 kata), santai, khas warga Medan, langsung teks chat tanpa tanda kutip dan tanpa penjelasan.\n" +
		"ATURAN WAJIB:\n" +
		"1. Jika bot bertanya apakah pengaduan ingin dibatalkan (misalnya 'apakah Anda ingin membatalkan pengaduan saat ini?'), kamu TIDAK INGIN BATAL! Wajib tolak pembatalan dan minta dilanjutkan (misal: 'tidak min, jangan dibatalkan, lanjutkan laporan saya').\n"

	switch mode {
	case livechat.ModeTypo:
		return base + "2. Mode TYPO: Jawab hal yang ditanyakan bot secara tepat sesuai fakta acuan, namun sisipkan gaya chat WA terburu-buru dengan singkatan wajar (contoh: yg, dmn, bgt, tlg, mhon, jln, ga/nggak, sy) dan sedikit typo huruf keyboard alami tanpa merusak nama, angka, atau makna laporan."
	case livechat.ModeNgasal:
		return base + "2. Mode NGASAL: Balas dengan nada warga yang agak bingung, salah paham konteks pertanyaan bot, atau memberikan respon yang melenceng namun tetap dalam situasi pengaduan (contoh jika ditanya nama: 'nama ktp apa nama panggilan pak?', jika ditanya tanggal: 'kemarin pas hujan lebat itu min')."
	case livechat.ModeCelah:
		return base + "2. Mode CELAH: Balas dengan jawaban yang menantang validasi input bot (misalnya jawaban sangat pendek '-', 'lewat dulu pak', atau format ambigu)."
	default: // livechat.ModeBenar
		return base + "2. Mode BENAR: Jawab secara sopan, wajar, dan presisi sesuai fakta acuan yang diberikan. Jaga fakta (nama, tanggal, alamat, atau inti masalah) tanpa mengubahnya."
	}
}

// BuildLiveChat bootstraps the module for main.go. It returns nil, nil when the
// livechat schema is missing (the rest of the app keeps working untouched).
func BuildLiveChat(
	ctx context.Context,
	db *sql.DB,
	pools *wa.Manager,
	userSettings func(ctx context.Context, userID int64) model.HumanSettings,
	broker *SSEBroker,
	aiEngine *ai.Engine,
) (*livechat.BootstrapResult, error) {
	if db == nil || pools == nil {
		return nil, nil
	}
	var aiHook livechat.AIHook
	if aiEngine != nil && aiEngine.IsLLMEnabled() {
		aiHook = &aiHookAdapter{engine: aiEngine}
	}
	return livechat.BootstrapLiveChat(ctx, livechat.BootOptions{
		DB:       db,
		WA:       livechat.PoolResolver{Pools: pools},
		Settings: userSettings,
		Emitter:  LiveChatEmitter(broker),
		AI:       aiHook,
	})
}
