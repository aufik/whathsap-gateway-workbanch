package livechat

import (
	"errors"
	"math"
	"math/rand"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Sample values for the "random preview" filled by the form (plan §6.2). They
// must be Medan-flavored; when the tester edits them the edited value wins.

var randomNama = []string{
	"Budi Santoso", "Siti Nurhaliza", "Ahmad Fauzi", "Rina Marlina",
	"Dedi Kurniawan", "Sri Wahyuni", "Joko Susilo", "Maya Sari",
	"Rudi Hartono", "Fitri Handayani", "Andi Pratama", "Nurul Aini",
}

var randomAlamat = []string{
	"Jl. Merdeka No. 12, Kel. Pusat Pasar, Kec. Medan Kota",
	"Jl. Gatot Subroto No. 88, Kel. Sekip, Kec. Medan Petisah",
	"Jl. Sisingamangaraja No. 45, Kel. Harjosari, Kec. Medan Amplas",
	"Jl. Willem Iskander No. 7, Kel. Pahlawan, Kec. Medan Perjuangan",
	"Jl. Sutomo No. 120, Kel. Gang Buntu, Kec. Medan Timur",
	"Jl. Setiabudi No. 33, Kel. Tanjung Sari, Kec. Medan Selayang",
	"Jl. Menteng VII No. 5, Kel. Medan Tenggara, Kec. Medan Denai",
	"Jl. Karya Wisata No. 60, Kel. Pangkalan Masyhur, Kec. Medan Johor",
}

// medanBBox is the rough bounding box of Kota Medan for random coordinates.
const (
	medanLatMin = 3.52
	medanLatMax = 3.68
	medanLngMin = 98.60
	medanLngMax = 98.74
)

type CivicCase struct {
	Title      string `json:"title"`
	ReportText string `json:"reportText"`
}

var randomCases = []CivicCase{
	{
		Title:      "Lampu jalan mati",
		ReportText: "Lampu penerangan jalan umum padam sejak 3 hari lalu sehingga jalan gelap gulita dan rawan kecelakaan di malam hari min.",
	},
	{
		Title:      "Jalan berlubang dan aspal rusak",
		ReportText: "Ada lubang jalan berukuran cukup besar dan dalam yang sangat membahayakan pengendara sepeda motor, mohon segera ditambal aspalnya.",
	},
	{
		Title:      "Tumpukan sampah liar",
		ReportText: "Sampah menumpuk di pinggir jalan dan menimbulkan bau busuk karena belum diangkut petugas selama beberapa hari.",
	},
	{
		Title:      "Saluran parit tersumbat banjir",
		ReportText: "Drainase parit tersumbat endapan lumpur dan sampah sehingga saat hujan lebat air meluap menggenangi jalan.",
	},
	{
		Title:      "Pohon tumbang menghalangi jalan",
		ReportText: "Ada dahan pohon besar patah menimpa kabel dan menghalangi sebagian badan jalan, tolong dibantu pemangkasan.",
	},
}

// RandomSessionPreview holds every value shown in the form when it opens. The
// caller lets the tester edit any field; the final map is persisted to the
// session so the loop never re-rolls.
type RandomSessionPreview struct {
	Title           string            `json:"title"`
	ReportText      string            `json:"reportText"`
	NamaPengadu     string            `json:"namaPengadu"`
	TanggalKejadian string            `json:"tanggalKejadian"` // YYYY-MM-DD
	AlamatKejadian  string            `json:"alamatKejadian"`
	TitikLokasi     string            `json:"titikLokasi"`
	TitikLokasiSrc  TitikLokasiSource `json:"titikLokasiSource"`
}

// GenerateRandomPreview uses the supplied rng so tests are deterministic; pass
// rand.New(rand.NewSource(seed)) for reproducibility.
func GenerateRandomPreview(rng *rand.Rand, now time.Time) RandomSessionPreview {
	lat := medanLatMin + rng.Float64()*(medanLatMax-medanLatMin)
	lng := medanLngMin + rng.Float64()*(medanLngMax-medanLngMin)
	tanggal := now.AddDate(0, 0, -1-rng.Intn(14))
	c := randomCases[rng.Intn(len(randomCases))]
	return RandomSessionPreview{
		Title:           c.Title,
		ReportText:      c.ReportText,
		NamaPengadu:     randomNama[rng.Intn(len(randomNama))],
		TanggalKejadian: tanggal.Format("2006-01-02"),
		AlamatKejadian:  randomAlamat[rng.Intn(len(randomAlamat))],
		TitikLokasi:     FormatMapsLink(lat, lng),
		TitikLokasiSrc:  SourceRandom,
	}
}

// FormatMapsLink renders a Google Maps URL, which is what the bot accepts as a
// drop-in location text. Kept separate so tests can assert the shape.
func FormatMapsLink(lat, lng float64) string {
	return "https://maps.google.com/?q=" + strings.TrimRight(strings.TrimRight(
		strconv.FormatFloat(lat, 'f', 6, 64), "0"), ".") + "," +
		strings.TrimRight(strings.TrimRight(strconv.FormatFloat(lng, 'f', 6, 64), "0"), ".")
}

// coordInText extracts "lat,lng" whether it came from a Google Maps URL, a bare
// coordinate line, or the "geo:" URI some testers paste. Returns an error when
// no coordinate pair is present or values are out-of-range.
var coordPatterns = []*regexp.Regexp{
	regexp.MustCompile(`[?&]q=(-?\d+(?:\.\d+)?)\s*,\s*(-?\d+(?:\.\d+)?)`),
	regexp.MustCompile(`@(-?\d+(?:\.\d+)?),(-?\d+(?:\.\d+)?)`),
	regexp.MustCompile(`geo:(-?\d+(?:\.\d+)?),(-?\d+(?:\.\d+)?)`),
	regexp.MustCompile(`^\s*(-?\d+(?:\.\d+)?)\s*,\s*(-?\d+(?:\.\d+)?)\s*$`),
}

// ParseCoordinates returns (lat,lng) parsed from titik_lokasi. When the text is
// not a coordinate the caller keeps it as-is and notes the fallback in turns.
func ParseCoordinates(raw string) (float64, float64, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, 0, errors.New("titik lokasi kosong")
	}
	for _, re := range coordPatterns {
		m := re.FindStringSubmatch(s)
		if len(m) != 3 {
			continue
		}
		lat, err1 := strconv.ParseFloat(m[1], 64)
		lng, err2 := strconv.ParseFloat(m[2], 64)
		if err1 != nil || err2 != nil {
			continue
		}
		if lat < -90 || lat > 90 || lng < -180 || lng > 180 {
			return 0, 0, errors.New("koordinat di luar jangkauan")
		}
		return lat, lng, nil
	}
	return 0, 0, errors.New("koordinat tidak ditemukan")
}

// IsOutsideMedan returns true when the coordinate lies far outside the Kota
// Medan bounding box. Used only by the celah mode (max 1 turn per session).
func IsOutsideMedan(lat, lng float64) bool {
	const pad = 0.5
	return lat < medanLatMin-pad || lat > medanLatMax+pad ||
		lng < medanLngMin-pad || lng > medanLngMax+pad
}

// OuterMedanCoord returns a deterministic coordinate outside the city bounds,
// used once by celah mode so the bot's area validation can be probed safely.
func OuterMedanCoord(rng *rand.Rand) (float64, float64) {
	lat := math.Round((medanLatMax+1.0+rng.Float64()*0.5)*1e6) / 1e6
	lng := math.Round((medanLngMax+1.0+rng.Float64()*0.5)*1e6) / 1e6
	return lat, lng
}
