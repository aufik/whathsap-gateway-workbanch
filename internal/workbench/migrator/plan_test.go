package migrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"whatshap-gateway/internal/excel"
	"whatshap-gateway/internal/model"
)

// sampleWorkbook resolves the root template shipped in the repo.
func sampleWorkbook(t *testing.T) string {
	t.Helper()
	p := filepath.Join("..", "..", "..", "Uji-Bank-Pertanyaan-Programmer-4.xlsx")
	if _, err := os.Stat(p); err != nil {
		t.Skipf("workbook contoh tidak tersedia: %v", err)
	}
	return p
}

func TestBuildPlanFromTemplate(t *testing.T) {
	plan, err := Build(Options{
		UserID:   42,
		XLSXPath: sampleWorkbook(t),
		// Sidecar sengaja diarahkan ke file yang tidak ada → bundle kosong.
		MetricsPath: filepath.Join(t.TempDir(), "absent.metrics.json"),
		MediaRoots:  []string{t.TempDir()},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if plan.UserID != 42 {
		t.Fatalf("user id: %d", plan.UserID)
	}
	if len(plan.Bank) == 0 {
		t.Fatalf("bank rows kosong")
	}
	if err := plan.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	// Metrics tidak ada → tidak ada entri media (bukan error).
	if plan.BankMediaCount != 0 || len(plan.MediaPaths) != 0 {
		t.Fatalf("expected no media without sidecar, got bank=%d paths=%d",
			plan.BankMediaCount, len(plan.MediaPaths))
	}
}

func TestBuildRejectsMissingWorkbook(t *testing.T) {
	_, err := Build(Options{UserID: 1, XLSXPath: filepath.Join(t.TempDir(), "nope.xlsx")})
	if err == nil {
		t.Fatalf("missing workbook must error")
	}
}

func TestBuildRejectsBadUser(t *testing.T) {
	if _, err := Build(Options{UserID: 0, XLSXPath: sampleWorkbook(t)}); err == nil {
		t.Fatalf("user 0 must error")
	}
}

// TestCollectMediaResolvesAndDetectsMissing memastikan:
//   - path yang sama persis di-dedupe,
//   - path berbeda walau file sama tetap dihitung terpisah (kita tidak
//     menyamakan /media/x.png dengan /media/2/x.png — keduanya sah),
//   - path yang filenya tidak ada di root mana pun dilaporkan as missing.
func TestCollectMediaResolvesAndDetectsMissing(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ada.png"), []byte("x"), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	b := &excel.MetricsBundle{
		BankMedia: map[int][]string{
			1: {"/media/ada.png"},
			2: {"/media/ada.png"}, // duplikat persis → dedupe
		},
		OOBMedia: map[int][]string{
			3: {"/media/2/ada.png"},  // path beda, file ada
			4: {"/media/hilang.png"}, // file tidak ada
		},
	}
	all, missing := collectMedia(b, []string{dir})
	if len(all) != 3 {
		t.Fatalf("unique paths: got %d want 3 (%v)", len(all), all)
	}
	if len(missing) != 1 || missing[0] != "/media/hilang.png" {
		t.Fatalf("missing: %v", missing)
	}
}

func TestValidateDetectsDuplicateNo(t *testing.T) {
	p := &Plan{Bank: []model.TestRow{{No: 1}, {No: 1}}}
	if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "duplikat") {
		t.Fatalf("expected duplicate error, got %v", err)
	}
}

func TestValidateRejectsBadStatus(t *testing.T) {
	p := &Plan{Bank: []model.TestRow{{No: 1, ResultStatus: "Mungkin"}}}
	if err := p.Validate(); err == nil {
		t.Fatalf("expected invalid status error")
	}
}
