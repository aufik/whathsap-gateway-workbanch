// Command livechat-smoke runs one real Live Chat session end-to-end against the
// configured WhatsApp bot. It reuses the production bootstrap and stores the
// result in livechat.* exactly like the HTTP handler does. Run it manually,
// never in CI: it sends real WhatsApp messages.
//
//	go run ./cmd/livechat-smoke -device <uuid> -mode benar -assets <sha>
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"whatshap-gateway/internal/config"
	"whatshap-gateway/internal/database"
	"whatshap-gateway/internal/livechat"
	"whatshap-gateway/internal/model"
	"whatshap-gateway/internal/wa"
)

func main() {
	deviceFlag := flag.String("device", "", "deviceUUID of the connected WhatsApp tester")
	modeFlag := flag.String("mode", "benar", "benar|typo|ngasal|celah")
	titleFlag := flag.String("title", "Uji manual Live Chat", "judul pengaduan")
	reportFlag := flag.String("report", "Ada lubang besar di depan pasar yang membahayakan pengendara.", "isi laporan")
	assetFlag := flag.String("asset", "", "path PNG/JPEG/PDF yang diunggah sekali lalu dikirim ke bot")
	flag.Parse()
	title := *titleFlag

	cfg, err := config.Load(".env")
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	bot := cfg.Settings.BotPhone
	if bot == "" {
		log.Fatalf("BOT_PHONE tidak diset")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	db, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer db.Close()

	// NOTE: this command takes over the WhatsApp session of the device. The
	// main application MUST be stopped first: otherwise the bot receives
	// StreamReplaced and our smoke run fails with "disconnected".
	pools := wa.NewManager(func(int64, string, any) {})
	defer pools.CloseAll()

	// Discover the requested device and its owner.
	deviceUUID := *deviceFlag
	userID := int64(2)
	if deviceUUID == "" {
		devs, _ := db.ListWADevices(ctx, userID)
		for _, d := range devs {
			if d.IsConnected {
				deviceUUID = d.DeviceUUID
				break
			}
		}
	}
	if deviceUUID == "" {
		log.Fatalf("tidak ada device terhubung untuk user %d", userID)
	}
	// Attach + auto connect; a stored session will reconnect automatically.
	client, err := pools.AttachDevice(ctx, userID, deviceUUID, func(st model.WAStatus) {
		log.Printf("wa status connected=%v loggedIn=%v jid=%s err=%s", st.Connected, st.LoggedIn, st.AccountJID, st.LastError)
	})
	if err != nil {
		log.Fatalf("attach: %v", err)
	}
	client.AutoConnectIfLoggedIn(ctx)
	// Wait for the WA connection; if the stored session was revoked the user
	// must pair again through the web UI instead of this smoke command.
	for i := 0; i < 20; i++ {
		st := client.Status()
		if st.Connected && st.LoggedIn {
			log.Printf("WA terhubung sebagai %s (+%s)", st.AccountJID, st.PushName)
			break
		}
		time.Sleep(time.Second)
	}
	if st := client.Status(); !st.Connected || !st.LoggedIn {
		log.Fatalf("WhatsApp belum terhubung; status=%+v", st)
	}

	// Grab settings so the humanizer mirrors the user's configured rhythm.
	raw, botPhone, ok, _ := db.LoadSettings(ctx, userID)
	human := cfg.GetSettings()
	if ok {
		_ = jsonUnmarshal(raw, &human)
	}
	if botPhone != "" {
		human.BotPhone = botPhone
	}

	res, err := livechat.BootstrapLiveChat(ctx, livechat.BootOptions{
		DB: db.DB(),
		WA: livechat.PoolResolver{Pools: pools},
		Settings: func(ctx context.Context, uid int64) model.HumanSettings {
			return human
		},
		Emitter: livechat.NopEmitter{},
	})
	if err != nil || res == nil {
		log.Fatalf("bootstrap livechat: %v", err)
	}

	var assetIDs []int64
	if *assetFlag != "" {
		f, aerr := os.Open(*assetFlag)
		if aerr != nil {
			log.Fatalf("buka aset: %v", aerr)
		}
		fi, _ := f.Stat()
		asset, created, uerr := res.Assets.Upload(ctx, userID, livechat.UploadRequest{
			Label:    "Foto Bukti Manual",
			FileName: fi.Name(),
			File:     f,
			Size:     fi.Size(),
		})
		f.Close()
		if uerr != nil {
			log.Fatalf("upload aset: %v", uerr)
		}
		log.Printf("asset id=%d sha=%s created=%v", asset.ID, asset.SHA256[:8], created)
		assetIDs = []int64{asset.ID}
	}

	in := livechat.SessionInput{
		DeviceUUID:      deviceUUID,
		WAPhone:         stripJID(client.Status().AccountJID),
		BotPhone:        bot,
		Title:           title,
		ReportText:      *reportFlag,
		NamaPengadu:     "Budi Santoso",
		TanggalKejadian: time.Now().AddDate(0, 0, -3).Format("2006-01-02"),
		AlamatKejadian:  "Jl. Merdeka No. 12, Kel. Pusat Pasar, Kec. Medan Kota",
		TitikLokasi:     "https://maps.google.com/?q=3.5952,98.6722",
		TitikLokasiSrc:  livechat.SourceRandom,
		AssetIDs:        assetIDs,
		MaxTurns:        12,
		CategoryMode:    livechat.CategoryMode(*modeFlag),
	}
	sess, err := res.Manager.StartSession(ctx, userID, in)
	if err != nil {
		log.Fatalf("start session: %v", err)
	}
	log.Printf("SESSION %d started (mode=%s, max_turns=%d)", sess.ID, sess.CategoryMode, sess.MaxTurns)

	// Poll until the manager removes the active session, up to 20 minutes.
	deadline := time.Now().Add(20 * time.Minute)
	for time.Now().Before(deadline) {
		if res.Manager.ActiveSessionID(userID) == 0 {
			break
		}
		time.Sleep(3 * time.Second)
	}
	if res.Manager.ActiveSessionID(userID) != 0 {
		log.Printf("timeout lokal tercapai; hentikan manual")
		res.Manager.Stop(userID, sess.ID)
	}

	d, err := res.Store.GetSessionDetail(ctx, userID, sess.ID)
	if err != nil {
		log.Fatalf("detail: %v", err)
	}
	fmt.Printf("\n=== RESULT SESSION %d ===\n", d.Session.ID)
	fmt.Printf("status=%s end_reason=%q ticket=%q turns=%d\n",
		d.Session.Status, d.Session.EndReason, d.Session.TicketNo, d.Session.TotalTurns)
	fmt.Printf("last_excerpt:\n%s\n", d.Session.LastChatExcerpt)
	fmt.Printf("messages=%d turns=%d\n", len(d.Messages), len(d.Turns))
	for _, m := range d.Messages {
		fmt.Printf("  [%s] %s\n", m.Direction, truncate(m.Body, 140))
	}
	// Force an explicit exit status for CI wrappers.
	if d.Session.Status != livechat.StatusFinished && d.Session.Status != livechat.StatusStopped && d.Session.Status != livechat.StatusTimeout && d.Session.Status != livechat.StatusFailed {
		os.Exit(2)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func stripJID(jid string) string {
	for i, r := range jid {
		if r == ':' || r == '@' {
			return jid[:i]
		}
	}
	return jid
}

// jsonUnmarshal is a tiny indirection so cmd does not need to import encoding/json twice.
func jsonUnmarshal(raw []byte, dst any) error {
	return json.Unmarshal(raw, dst)
}
