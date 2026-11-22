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

func (a *aiHookAdapter) ComposeAnswer(ctx context.Context, kind livechat.QuestionKind, draft, botQuestion string) (string, error) {
	if a.engine == nil || !a.engine.IsLLMEnabled() {
		return "", nil
	}
	sys := "Kamu adalah warga Kota Medan yang sedang membalas chat WhatsApp ke Customer Service / Bot Pemko Medan. " +
		"Tugasmu: balas pertanyaan bot dengan kalimat santai, sopan, wajar khas gaya chat WA warga Medan (boleh singkatan umum seperti yg, dmn, pak, min). " +
		"Jawab singkat (1 kalimat pendek, maksimal 15 kata). " +
		"WAJIB JAGA FAKTA berikut tanpa mengubah angka, nama, tanggal, atau alamat: " + draft + ". " +
		"Balas HANYA kalimat chat-nya saja tanpa tanda kutip dan tanpa penjelasan."
	usr := fmt.Sprintf("Pertanyaan Bot: %s\nFakta Jawaban: %s", botQuestion, draft)

	timeoutCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	return a.engine.ChatReply(timeoutCtx, sys, usr, 0.4)
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
