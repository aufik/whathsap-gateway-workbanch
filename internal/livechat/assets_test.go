package livechat

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- fixtures ---

// smallestPNG is a 1x1 transparent PNG. It is sniffed by net/http as
// image/png, which is what the classifier relies on.
var smallestPNG = []byte{
	0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A,
	0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4,
	0x89, 0x00, 0x00, 0x00, 0x0D, 0x49, 0x44, 0x41,
	0x54, 0x78, 0x9C, 0x63, 0x00, 0x01, 0x00, 0x00,
	0x05, 0x00, 0x01, 0x0D, 0x0A, 0x2D, 0xB4, 0x00,
	0x00, 0x00, 0x00, 0x49, 0x45, 0x4E, 0x44, 0xAE,
	0x42, 0x60, 0x82,
}

// minimalPDF starts with the %PDF-1.x magic that net/http sniffs as PDF.
var minimalPDF = []byte("%PDF-1.4\n%fake body for sniffing\n%%EOF\n")

// minimalZip starts with the PK zip magic (used to test the docx/xlsx path).
var minimalZip = []byte{0x50, 0x4B, 0x03, 0x04, 0x14, 0x00, 0x00, 0x00, 0x08, 0x00, 0x00, 0x00}

// openAssetsTestDB reuses the store's own openTestDB + cleanup helpers.
func openAssetsTestDB(t *testing.T) *sql.DB { return openTestDB(t) }

func setupAssetTest(t *testing.T) (*Store, *AssetService, string, int64) {
	t.Helper()
	db := openAssetsTestDB(t)
	t.Cleanup(func() { db.Close() })
	st := NewStore(db)
	base := t.TempDir()
	userID := int64(900010)
	t.Cleanup(func() { cleanupUser(t, db, userID) })
	return st, NewAssetService(st, base), base, userID
}

// --- classifyMime unit tests ---

func TestClassifyMimeMismatch(t *testing.T) {
	cases := []struct {
		sniff, ext string
		wantKind   AssetKind
		wantMime   string
		wantErr    bool
	}{
		{"image/png", ".png", AssetImage, "image/png", false},
		{"image/jpeg", ".jpg", AssetImage, "image/jpeg", false},
		{"image/jpeg", ".jpeg", AssetImage, "image/jpeg", false},
		{"application/pdf", ".pdf", AssetDocument, "application/pdf", false},
		{"application/zip", ".docx", AssetDocument, "application/vnd.openxmlformats-officedocument.wordprocessingml.document", false},
		// mismatches → reject
		{"image/png", ".jpg", "", "", true},
		{"application/pdf", ".jpg", "", "", true},
		{"application/zip", ".txt", "", "", true},
		{"text/html", ".html", "", "", true},
		{"image/gif", ".gif", "", "", true},
	}
	for _, c := range cases {
		kind, mime, err := classifyMime(c.sniff, c.ext)
		if (err != nil) != c.wantErr {
			t.Fatalf("classifyMime(%q,%q): err=%v wantErr=%v", c.sniff, c.ext, err, c.wantErr)
		}
		if err == nil {
			if kind != c.wantKind || mime != c.wantMime {
				t.Fatalf("classifyMime(%q,%q) = (%v,%q), want (%v,%q)", c.sniff, c.ext, kind, mime, c.wantKind, c.wantMime)
			}
		}
	}
}

func TestSafeRelPath(t *testing.T) {
	cases := map[string]bool{
		"ab/abcdef.png":    true,
		"/abs/path":        false,
		"../etc/passwd":    false,
		"dir/../../escape": false,
		"":                 false,
	}
	for p, want := range cases {
		if got := safeRelPath(p); got != want {
			t.Fatalf("safeRelPath(%q)=%v, want %v", p, got, want)
		}
	}
}

// --- end-to-end upload tests (real DB + temp dir) ---

func TestAssetUploadAndDedup(t *testing.T) {
	st, svc, base, userID := setupAssetTest(t)
	ctx := context.Background()

	a, created, err := svc.Upload(ctx, userID, UploadRequest{
		Label:    "Foto Jalan",
		FileName: "foto.png",
		File:     bytes.NewReader(smallestPNG),
		Size:     int64(len(smallestPNG)),
	})
	if err != nil || !created {
		t.Fatalf("first upload: created=%v err=%v", created, err)
	}
	if a.SHA256 == "" || a.SizeBytes != int64(len(smallestPNG)) || a.Mimetype != "image/png" {
		t.Fatalf("asset fields wrong: %+v", a)
	}
	if !safeRelPath(a.StoredPath) {
		t.Fatalf("stored path unsafe: %q", a.StoredPath)
	}
	abs := filepath.Join(base, a.StoredPath)
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("asset file missing: %v", err)
	}

	// Duplicate upload: same sha → same row, created=false, no extra file.
	again, created2, err := svc.Upload(ctx, userID, UploadRequest{
		Label: "Foto Jalan (ulang)", FileName: "lain.png",
		File: bytes.NewReader(smallestPNG), Size: int64(len(smallestPNG)),
	})
	if err != nil || created2 {
		t.Fatalf("dedup expected created=false, got created=%v err=%v", created2, err)
	}
	if again.ID != a.ID {
		t.Fatalf("dedup must reuse row id: %d vs %d", again.ID, a.ID)
	}
	// Count files under the user's directory: exactly one content file.
	files := 0
	_ = filepath.Walk(base, func(_ string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() && !strings.HasPrefix(info.Name(), ".upload-") {
			files++
		}
		return nil
	})
	if files != 1 {
		t.Fatalf("expected 1 content file after dedup, got %d", files)
	}

	// Verify catches mutation
	path := filepath.Join(base, again.StoredPath)
	if err := os.WriteFile(path, []byte("tampered"), 0644); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	if _, err := svc.Verify(ctx, userID, a.ID); err == nil {
		t.Fatalf("Verify must fail after tamper")
	}

	// Cross-user isolation
	other := int64(900011)
	t.Cleanup(func() { cleanupUser(t, st.db, other) })
	if _, _, err := svc.Open(ctx, other, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-user Open must return ErrNotFound, got %v", err)
	}
}

func TestAssetUploadRejectsMismatch(t *testing.T) {
	_, svc, _, userID := setupAssetTest(t)
	ctx := context.Background()

	// PDF sniffed but named .jpg → reject
	if _, _, err := svc.Upload(ctx, userID, UploadRequest{
		FileName: "fake.jpg", File: bytes.NewReader(minimalPDF), Size: int64(len(minimalPDF)),
	}); err == nil {
		t.Fatalf("mismatched extension must be rejected")
	}

	// Zip container with unknown ext → reject
	if _, _, err := svc.Upload(ctx, userID, UploadRequest{
		FileName: "thing.bin", File: bytes.NewReader(minimalZip), Size: int64(len(minimalZip)),
	}); err == nil {
		t.Fatalf("unknown extension must be rejected")
	}

	// Valid docx path (zip + .docx)
	if _, created, err := svc.Upload(ctx, userID, UploadRequest{
		Label: "Bukti", FileName: "bukti.docx", File: bytes.NewReader(minimalZip), Size: int64(len(minimalZip)),
	}); err != nil || !created {
		t.Fatalf("docx upload should pass: created=%v err=%v", created, err)
	}

	// Empty input → reject
	if _, _, err := svc.Upload(ctx, userID, UploadRequest{
		FileName: "kosong.png", File: bytes.NewReader(nil), Size: 0,
	}); err == nil {
		t.Fatalf("empty file must be rejected")
	}

	// Oversized declared size → reject before reading
	if _, _, err := svc.Upload(ctx, userID, UploadRequest{
		FileName: "big.png", File: bytes.NewReader(smallestPNG), Size: MaxAssetBytes + 1,
	}); err == nil {
		t.Fatalf("oversized file must be rejected")
	}
}

func TestSessionRejectsForeignAsset(t *testing.T) {
	st, svc, _, userID := setupAssetTest(t)
	ctx := context.Background()

	a, _, err := svc.Upload(ctx, userID, UploadRequest{
		FileName: "foto.png", File: bytes.NewReader(smallestPNG), Size: int64(len(smallestPNG)),
	})
	if err != nil {
		t.Fatalf("upload: %v", err)
	}

	other := int64(900012)
	t.Cleanup(func() { cleanupUser(t, st.db, other) })
	in := sampleInput()
	in.AssetIDs = []int64{a.ID}
	if _, err := st.CreateSession(ctx, other, in); err == nil {
		t.Fatalf("CreateSession must reject another user's asset_id")
	}
	// Same user is accepted.
	if _, err := st.CreateSession(ctx, userID, in); err != nil {
		t.Fatalf("CreateSession with own asset: %v", err)
	}
}
