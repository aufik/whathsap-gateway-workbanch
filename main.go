package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"whatshap-gateway/internal/ai"
	"whatshap-gateway/internal/auth"
	"whatshap-gateway/internal/config"
	"whatshap-gateway/internal/database"
	"whatshap-gateway/internal/excel"
	"whatshap-gateway/internal/model"
	"whatshap-gateway/internal/runner"
	"whatshap-gateway/internal/tenants"
	"whatshap-gateway/internal/wa"
	"whatshap-gateway/internal/web"
)

//go:embed public/*
var embeddedPublic embed.FS

func main() {
	cfg, err := config.Load(".env")
	if err != nil {
		log.Fatalf("Gagal memuat konfigurasi: %v", err)
	}

	store, err := excel.NewStore(cfg.GetExcelFile())
	if err != nil {
		log.Fatalf("Gagal memuat berkas Excel (%s): %v", cfg.GetExcelFile(), err)
	}

	broker := web.NewSSEBroker()
	aiEngine := ai.NewEngine(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// PostgreSQL multi-tenant store (users, sessions, per-user workbooks)
	dbStore, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("Gagal terhubung ke PostgreSQL: %v", err)
	}
	defer dbStore.Close()

	if err := dbStore.Migrate(ctx); err != nil {
		log.Fatalf("Gagal migrasi schema PostgreSQL: %v", err)
	}
	admin, err := dbStore.SeedAdmin(ctx, cfg.InitialAdminUsername, cfg.InitialAdminPassword, cfg.InitialAdminFullName)
	if err != nil {
		log.Printf("PERINGATAN: gagal seed admin (%v)", err)
	} else {
		log.Printf("Akun admin siap: %s (id=%d)", admin.Username, admin.ID)
	}

	workbooks := tenants.NewManager(dbStore)
	authManager := auth.NewManager(dbStore, cfg.SessionSecret)
	authHandler := &auth.Handler{Store: dbStore, Manager: authManager}

	// Default owner ID is superadmin_kamil (id 2) who owns the migrated test data.
	// Falls back to seeded admin if kamil is not yet found.
	ownerID := int64(2)
	if kamilUser, err := dbStore.Authenticate(ctx, "superadmin_kamil", "12345"); err == nil {
		ownerID = kamilUser.ID
	} else if admin.ID > 0 {
		ownerID = admin.ID
	}

	// Disable legacy duplicate client: each user now connects through their own
	// pool of devices with independent session databases, preventing conflicts.
	run := runner.NewRunner(cfg, store, aiEngine, nil, func(eventType string, payload any) {
		broker.Broadcast(ownerID, eventType, payload)
	})
	run.OnRowSaved(func() {
		workbooks.MarkDirty(ownerID)
	})

	// Per-user Runner manager: each account owns an independent execution worker,
	// eliminating race conditions and allowing multiple testers to run batches simultaneously.
	runnerManager := runner.NewManager(cfg, aiEngine, func(userID int64, eventType string, payload any) {
		broker.Broadcast(userID, eventType, payload)
	}, func(userID int64) {
		workbooks.MarkDirty(userID)
	})

	// Per-user WhatsApp pools: each account owns its own device set.
	waPools := wa.NewManager(func(userID int64, eventType string, payload any) {
		broker.Broadcast(userID, eventType, payload)
	})
	defer waPools.CloseAll()

	// Reconnect every previously linked device so sessions survive restarts.
	go func() {
		if dbStore == nil {
			return
		}
		users, err := dbStore.ListUsers(ctx)
		if err != nil {
			return
		}
		for _, u := range users {
			devices, err := dbStore.ListWADevices(ctx, u.ID)
			if err != nil {
				continue
			}
			for _, d := range devices {
				deviceUUID := d.DeviceUUID
				client, err := waPools.AttachDevice(ctx, u.ID, deviceUUID, func(st model.WAStatus) {
					broker.Broadcast(u.ID, "wa_status", map[string]any{"deviceUuid": deviceUUID, "status": st})
					if dbStore != nil {
						_ = dbStore.SetDeviceConnectedByUUID(context.Background(), u.ID, deviceUUID, st.Connected && st.LoggedIn, st.AccountJID)
					}
				})
				if err != nil {
					continue
				}
				client.AutoConnectIfLoggedIn(ctx)
			}
		}
	}()

	pubSub, err := fs.Sub(embeddedPublic, "public")
	if err != nil {
		log.Fatalf("Gagal memuat embedded public assets: %v", err)
	}

	srvHandler := web.NewServer(cfg, store, aiEngine, nil, run, broker, pubSub, web.AuthWiring{
		Handler:   authHandler,
		Manager:   authManager,
		DB:        dbStore,
		PublicFS:  pubSub,
		Workbooks: workbooks,
		WAPool:    waPools,
		Runners:   runnerManager,
	})

	// Live Chat Emulator: independent module, schema livechat.*. Booted only
	// when the schema exists (read-only check inside), so a deployment that has
	// not applied the SQL still starts cleanly without this tab.
	if lc, lcErr := web.BuildLiveChat(ctx, dbStore.DB(), waPools, srvHandler.SettingsForUser, broker); lcErr != nil {
		log.Printf("PERINGATAN: modul Live Chat gagal diinisialisasi: %v", lcErr)
	} else if lc != nil {
		srvHandler.AttachLiveChat(lc.Store, lc.Assets, lc.Manager, lc.Exporter)
		log.Printf("Live Chat Emulator aktif (schema livechat)")
	}

	// Persist per-user workbooks (results + attachments) before shutdown so the
	// database always mirrors the newest state.
	go func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := workbooks.FlushDirty(ctx); err != nil {
					log.Printf("PERINGATAN: gagal sinkron workbook ke database: %v", err)
				}
			}
		}
	}()
	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)

	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srvHandler.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		_, sum := store.Snapshot()
		log.Printf("WA Bot QA Workbench siap di http://%s", addr)
		log.Printf("Berkas Excel aktif: %s (%d soal: %d approved, %d waiting)", sum.FileName, sum.Total, sum.Approved, sum.Waiting)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("HTTP server berhenti dengan error: %v", err)
		}
	}()

	sig := <-sigCh
	log.Printf("Menerima sinyal %v, menutup antrean & koneksi WhatsApp dengan aman...", sig)

	lcCtx, lcCancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = srvHandler.ShutdownLiveChat(lcCtx)
	lcCancel()
	runnerManager.StopAll()
	_ = run.Stop()
	if err := workbooks.PersistAll(context.Background()); err != nil {
		log.Printf("PERINGATAN: gagal simpan workbook terakhir: %v", err)
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer shutdownCancel()
	_ = httpSrv.Shutdown(shutdownCtx)
}
