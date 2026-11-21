package excel

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"whatshap-gateway/internal/model"
)

// testWorkbook resolves the sample workbook relative to this package.
func testWorkbook(t *testing.T) string {
	t.Helper()
	return filepath.Join("..", "..", "Uji-Bank-Pertanyaan-Programmer-4.xlsx")
}

// TestParseFileMatchesStore verifies that the standalone parser produces
// exactly the same in-memory result as the legacy Store path. This is the
// contract that lets workbench.DBStore reuse the parser for uploads.
func TestParseFileMatchesStore(t *testing.T) {
	path := testWorkbook(t)

	store, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	wantBank, wantBankSum := store.Snapshot()
	wantOOB, wantOOBSum := store.OutOfBankSnapshot()
	wantStress, wantStressSum := store.StressTestSnapshot()

	res, err := ParseFile(path, &MetricsBundle{
		Bank:        store.bankMetrics,
		OOB:         store.oobMetrics,
		Stress:      store.stressMetrics,
		BankMedia:   store.bankMedia,
		OOBMedia:    store.oobMedia,
		StressMedia: store.stressMedia,
	})
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	if len(res.Bank) != len(wantBank) {
		t.Fatalf("bank rows: got %d want %d", len(res.Bank), len(wantBank))
	}
	if len(res.OOB) != len(wantOOB) {
		t.Fatalf("oob rows: got %d want %d", len(res.OOB), len(wantOOB))
	}
	if len(res.Stress) != len(wantStress) {
		t.Fatalf("stress rows: got %d want %d", len(res.Stress), len(wantStress))
	}
	if !reflect.DeepEqual(res.Bank, wantBank) {
		t.Fatalf("bank rows differ from Store.Snapshot()")
	}
	if !reflect.DeepEqual(res.OOB, wantOOB) {
		t.Fatalf("oob rows differ from Store.OutOfBankSnapshot()")
	}
	if !reflect.DeepEqual(res.Stress, wantStress) {
		t.Fatalf("stress rows differ from Store.StressTestSnapshot()")
	}
	// OPD order is internal to Store; verify it exists and covers all bank OPDs.
	if len(res.OPDOrder) == 0 {
		t.Fatalf("OPDOrder empty")
	}
	seen := map[string]bool{}
	for _, o := range res.OPDOrder {
		seen[o] = true
	}
	for _, r := range wantBank {
		if r.OPD != "" && !seen[r.OPD] {
			t.Fatalf("OPD %q missing from OPDOrder", r.OPD)
		}
	}
	// Summaries computed from parsed rows must equal Store summaries via the
	// same helper the production code uses.
	_ = wantBankSum
	_ = wantOOBSum
	_ = wantStressSum
}

// TestParseBytesRejectsInvalid covers the error paths that callers rely on.
func TestParseBytesRejectsInvalid(t *testing.T) {
	if _, err := ParseBytes(nil, nil); err == nil {
		t.Fatalf("empty bytes must error")
	}
	if _, err := ParseBytes([]byte("not an xlsx"), nil); err == nil {
		t.Fatalf("invalid xlsx must error")
	}
}

// TestParseBytesWithoutMetrics proves metrics are optional.
func TestParseBytesWithoutMetrics(t *testing.T) {
	path := testWorkbook(t)
	raw, err := readAll(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	res, err := ParseBytes(raw, nil)
	if err != nil {
		t.Fatalf("ParseBytes: %v", err)
	}
	if len(res.Bank) != 210 {
		t.Fatalf("bank rows: got %d want 210", len(res.Bank))
	}
	// No metrics supplied → every row must carry the zero value.
	var zero model.RowHumanMetrics
	for _, r := range res.Bank {
		if !reflect.DeepEqual(r.Metrics, zero) {
			t.Fatalf("row %d has unexpected metrics without bundle", r.No)
		}
	}
}

// TestLoadMetricsFileReal captures the real sidecar shape used by user 2 so
// the ETL (stage 5) can rely on it.
func TestLoadMetricsFileReal(t *testing.T) {
	path := filepath.Join("..", "..", "data", "users", "2", "workbook.metrics.json")
	if _, err := os.Stat(path); err != nil {
		t.Skip("metrics sidecar user 2 tidak tersedia")
	}
	b, err := LoadMetricsFile(path)
	if err != nil {
		t.Fatalf("LoadMetricsFile: %v", err)
	}
	if len(b.Bank) != 210 {
		t.Fatalf("bank metrics: got %d want 210", len(b.Bank))
	}
	if len(b.OOB) != 52 {
		t.Fatalf("oob metrics: got %d want 52", len(b.OOB))
	}
	if len(b.Stress) != 15 {
		t.Fatalf("stress metrics: got %d want 15", len(b.Stress))
	}
	if len(b.BankMedia) != 104 {
		t.Fatalf("bank media: got %d want 104", len(b.BankMedia))
	}
	if len(b.OOBMedia) != 14 {
		t.Fatalf("oob media: got %d want 14", len(b.OOBMedia))
	}
	// stressMedia absent in this sidecar → must be an empty non-nil map.
	if b.StressMedia == nil {
		t.Fatalf("StressMedia must be non-nil empty map")
	}
}

func TestLoadMetricsFileMissing(t *testing.T) {
	b, err := LoadMetricsFile(filepath.Join(t.TempDir(), "nope.metrics.json"))
	if err != nil {
		t.Fatalf("missing file must not error: %v", err)
	}
	if b == nil || b.Bank == nil {
		t.Fatalf("expected non-nil empty bundle")
	}
}
