package livechat

import (
	"context"
	"database/sql"
	"errors"
	"log"

	"whatshap-gateway/internal/model"
)

// Bootstrap creates the store + assets + manager from the already-opened pool.
// It is a no-op stub (returns nil, nil) when the `livechat` schema has not been
// migrated yet, so the rest of the app keeps running even if a deployment
// forgets to apply the SQL. The check is read-only.
type BootOptions struct {
	DB        *sql.DB
	WA        DeviceResolver
	Settings  func(ctx context.Context, userID int64) model.HumanSettings
	Emitter   EngineEmitter
	MediaRoot func(userID int64) string
	AssetRoot func(userID int64) string
}

// BootstrapResult bundles everything main.go needs to wire to the server.
type BootstrapResult struct {
	Store   *Store
	Assets  *AssetService
	Manager *Manager
}

// BootstrapLiveChat returns (nil, nil) when the schema is missing.
func BootstrapLiveChat(ctx context.Context, opts BootOptions) (*BootstrapResult, error) {
	if opts.DB == nil || opts.WA == nil || opts.Settings == nil {
		return nil, errors.New("bootstrap livechat butuh DB, WA, dan Settings")
	}
	missing, err := schemaMissing(ctx, opts.DB)
	if err != nil {
		return nil, err
	}
	if missing {
		log.Printf("livechat: schema belum dimigrasi — modul Live Chat dinonaktifkan (jalankan migration_livechat_v1.sql terlebih dulu)")
		return nil, nil
	}
	store := NewStore(opts.DB)
	assets := NewAssetService(store, "")
	// The manager's AssetRoot override is used by the engine when sending; the
	// asset service uses UserAssetsRoot(userID) at upload time by default.
	assetRoot := opts.AssetRoot
	if assetRoot == nil {
		assetRoot = UserAssetsRoot
	}
	mgr, err := NewManager(ManagerOptions{
		Store:     store,
		Assets:    assets,
		Responder: &Responder{},
		WA:        opts.WA,
		Settings: func(ctx context.Context, userID int64) (model.HumanSettings, bool) {
			return opts.Settings(ctx, userID), true
		},
		MediaRoot: opts.MediaRoot,
		AssetRoot: assetRoot,
		Emitter:   opts.Emitter,
	})
	if err != nil {
		return nil, err
	}
	return &BootstrapResult{Store: store, Assets: assets, Manager: mgr}, nil
}

// schemaMissing is a cheap read that never writes. It treats both "schema not
// present" and "tables not present" as missing.
func schemaMissing(ctx context.Context, db *sql.DB) (bool, error) {
	var present bool
	err := db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'livechat' AND table_name = 'sessions'
		)`).Scan(&present)
	if err != nil {
		return false, err
	}
	return !present, nil
}
