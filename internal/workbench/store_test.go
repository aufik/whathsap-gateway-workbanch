package workbench

import (
	"reflect"
	"sort"
	"testing"

	"whatshap-gateway/internal/excel"
)

// expectedMethods adalah daftar kontrak Store yang WAJIB ada, disalin dari
// method publik *excel.Store yang dipakai internal/runner dan internal/web.
// Test ini gagal bila suatu saat seseorang "membersihkan" interface dengan
// menghapus method tanpa memperbarui pemanggilnya (era wiring baru sadar).
var expectedMethods = []string{
	"AppendOutOfBankRows",
	"AppendStressTestRows",
	"ClearOutOfBank",
	"ClearStressTest",
	"DeleteOutOfBankBatch",
	"DeleteOutOfBankRow",
	"DeleteStressTestBatch",
	"DeleteStressTestRow",
	"ExportBytes",
	"GetOutOfBankRow",
	"GetRow",
	"GetStressTestRow",
	"OutOfBankSnapshot",
	"PendingOutOfBankNumbers",
	"PendingOutOfBankRange",
	"PendingRowNumbers",
	"PendingRowRange",
	"PendingStressNumbers",
	"PendingStressRange",
	"ReplaceFromBytes",
	"Snapshot",
	"StressTestSnapshot",
	"UpdateOutOfBankRow",
	"UpdateRow",
	"UpdateStressTestRow",
}

func TestStoreContractShape(t *testing.T) {
	iface := reflect.TypeOf((*Store)(nil)).Elem()
	got := make([]string, 0, iface.NumMethod())
	for i := 0; i < iface.NumMethod(); i++ {
		got = append(got, iface.Method(i).Name)
	}
	sort.Strings(got)

	want := append([]string(nil), expectedMethods...)
	sort.Strings(want)

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("kontrak Store berubah.\n got : %v\n want: %v", got, want)
	}
}

// TestExcelStoreSatisfiesContract memastikan assert.go tetap relevan: bila
// method dihapus dari *excel.Store, test ini gagal saat paket di-build.
//
// Catatan untuk Tahap 10: menugaskan *excel.Store nil ke interface Store
// menghasilkan interface NON-nil (typed nil). Periksa nilai nil pada tipe
// konkretnya, bukan pada interface.
func TestExcelStoreSatisfiesContract(t *testing.T) {
	var concrete *excel.Store
	if concrete != nil {
		t.Fatalf("expected nil concrete")
	}
	var s Store = concrete
	if s == nil {
		t.Fatalf("typed nil assigned to interface should be non-nil; " +
			"callers must nil-check the concrete type, not the interface")
	}
	// Membaca method pada typed nil akan panic: pastikan pemanggil di Tahap 10
	// melakukan guard sebelum memanggil Snapshot().
	defer func() {
		if recover() == nil {
			t.Fatalf("expected panic calling Snapshot on typed nil")
		}
	}()
	_, _ = s.Snapshot()
}
