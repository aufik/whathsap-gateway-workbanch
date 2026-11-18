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
	"whatshap-gateway/internal/config"
	"whatshap-gateway/internal/excel"
	"whatshap-gateway/internal/model"
	"whatshap-gateway/internal/runner"
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

	waClient, err := wa.NewClient(ctx, cfg.WASessionDB, func(st model.WAStatus) {
		broker.Broadcast("wa_status", st)
	})
	if err != nil {
		log.Fatalf("Gagal menyiapkan sesi WhatsApp (%s): %v", cfg.WASessionDB, err)
	}
	defer waClient.Close()

	// Sambungkan otomatis jika sesi login WA sebelumnya sudah tersimpan di SQLite
	go waClient.AutoConnectIfLoggedIn(ctx)

	run := runner.NewRunner(cfg, store, aiEngine, waClient, func(eventType string, payload any) {
		broker.Broadcast(eventType, payload)
	})

	pubSub, err := fs.Sub(embeddedPublic, "public")
	if err != nil {
		log.Fatalf("Gagal memuat embedded public assets: %v", err)
	}

	srvHandler := web.NewServer(cfg, store, aiEngine, waClient, run, broker, pubSub)
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

	_ = run.Stop()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer shutdownCancel()
	_ = httpSrv.Shutdown(shutdownCtx)
}
