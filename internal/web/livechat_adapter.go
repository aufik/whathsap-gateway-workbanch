package web

import (
	"context"
	"database/sql"

	"whatshap-gateway/internal/livechat"
	"whatshap-gateway/internal/model"
	"whatshap-gateway/internal/wa"
)

// BuildLiveChat bootstraps the module for main.go. It returns nil, nil when the
// livechat schema is missing (the rest of the app keeps working untouched).
func BuildLiveChat(
	ctx context.Context,
	db *sql.DB,
	pools *wa.Manager,
	userSettings func(ctx context.Context, userID int64) model.HumanSettings,
	broker *SSEBroker,
) (*livechat.BootstrapResult, error) {
	if db == nil || pools == nil {
		return nil, nil
	}
	return livechat.BootstrapLiveChat(ctx, livechat.BootOptions{
		DB:       db,
		WA:       livechat.PoolResolver{Pools: pools},
		Settings: userSettings,
		Emitter:  LiveChatEmitter(broker),
	})
}
