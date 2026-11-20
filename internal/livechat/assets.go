package livechat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Limits and allowlist for uploaded assets. Values are intentionally constants
// (not env-configurable) so the trust boundary is identical in every deploy.
const (
	MaxAssetBytes int64 = 16 << 20  // 16 MiB per file
	MaxUserBytes  int64 = 500 << 20 // 500 MiB per user total
	assetsDirName       = "livechat_assets"
)

var (
	// ooxmlExt covers .docx/.xlsx/.pptx; those files sniff as PK zip.
	ooxmlExt = map[string]string{
		".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	}
	hexSHA = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// AssetService stores uploaded files on disk, content-addressed by SHA-256, and
// records them in livechat.assets. Duplicate uploads reuse the existing file and
// row, so a tester never pays twice for the same bytes.
type AssetService struct {
	store   *Store
	baseDir string // optional override for tests; empty = UserAssetsRoot(userID)
}

// NewAssetService wires the store + optional base directory override.
func NewAssetService(store *Store, baseDir string) *AssetService {
	return &AssetService{store: store, baseDir: baseDir}
}

// UserAssetsRoot returns the on-disk directory for one user's livechat assets.
func UserAssetsRoot(userID int64) string {
	return filepath.Join("data", "users", fmt.Sprintf("%d", userID), assetsDirName)
}

// UploadRequest is the caller's view of one multipart file before validation.
// FileName is the original client filename (used for extension + display);
// Label is the tester-visible label and may be empty (falls back to FileName).
type UploadRequest struct {
	Label    string
	FileName string
	File     io.Reader
	Size     int64 // from Content-Length when known; enforced again while reading
}

// Upload validates and persists one file for userID. The bool result is true
// when the bytes were new; false when an identical SHA-256 was already stored
// (the existing Asset is returned in both cases).
func (a *AssetService) Upload(ctx context.Context, userID int64, req UploadRequest) (Asset, bool, error) {
	if userID <= 0 {
		return Asset{}, false, ErrNotFound
	}
	if req.File == nil {
		return Asset{}, false, errors.New("berkas tidak diberikan")
	}
	if req.Size > MaxAssetBytes {
		return Asset{}, false, fmt.Errorf("berkas melebihi batas %d MB", MaxAssetBytes>>20)
	}

	// 1. Stream to a temp file under the user's own directory to keep disk
	//    writes bounded and avoid holding big payloads in RAM.
	root := a.userRoot(userID)
	if err := os.MkdirAll(root, 0755); err != nil {
		return Asset{}, false, err
	}
	tmp, err := os.CreateTemp(root, ".upload-*")
	if err != nil {
		return Asset{}, false, err
	}
	tmpName := tmp.Name()
	cleanupTmp := func() { _ = os.Remove(tmpName) }

	hasher := sha256.New()
	limited := io.LimitReader(req.File, MaxAssetBytes+1)
	written, err := io.Copy(io.MultiWriter(tmp, hasher), limited)
	closeErr := tmp.Close()
	if err != nil {
		cleanupTmp()
		return Asset{}, false, err
	}
	if closeErr != nil {
		cleanupTmp()
		return Asset{}, false, closeErr
	}
	if written > MaxAssetBytes {
		cleanupTmp()
		return Asset{}, false, fmt.Errorf("berkas melebihi batas %d MB", MaxAssetBytes>>20)
	}
	if written == 0 {
		cleanupTmp()
		return Asset{}, false, errors.New("berkas kosong")
	}

	// 2. Confirm that the first bytes match what the extension claims. Never
	//    trust a client-supplied Content-Type header.
	sniffed := sniffMime(tmpName)
	ext := strings.ToLower(filepath.Ext(req.FileName))
	kind, canonicalMime, err := classifyMime(sniffed, ext)
	if err != nil {
		cleanupTmp()
		return Asset{}, false, err
	}

	sha := hex.EncodeToString(hasher.Sum(nil))
	if !hexSHA.MatchString(sha) {
		cleanupTmp()
		return Asset{}, false, errors.New("checksum tidak valid")
	}

	// 3. Look up an existing row by (user_id, sha256); if it exists we can
	//    short-circuit without touching disk. Otherwise insert a fresh row.
	if existing, lerr := a.store.findAssetBySHA(ctx, userID, sha); lerr == nil {
		cleanupTmp()
		return existing, false, nil
	} else if !errors.Is(lerr, ErrNotFound) {
		cleanupTmp()
		return Asset{}, false, lerr
	}

	storedRel := storedRelPath(sha, canonicalMime)
	storedAbs := filepath.Join(root, storedRel)
	if err := os.MkdirAll(filepath.Dir(storedAbs), 0755); err != nil {
		cleanupTmp()
		return Asset{}, false, err
	}
	if err := copyTo(tmpName, storedAbs); err != nil {
		cleanupTmp()
		return Asset{}, false, err
	}

	// 4. Quota before we persist; a failure removes the just-written bytes so
	//    no orphan stays behind.
	if err := a.enforceQuota(root); err != nil {
		cleanupTmp()
		_ = os.Remove(storedAbs)
		return Asset{}, false, err
	}

	// 5. Insert the DB row last; on failure roll the file back so we never
	//    leave an orphan asset on disk.
	asset, err := a.store.CreateAsset(ctx, userID, AssetInput{
		Kind:       kind,
		Label:      firstNonEmpty(req.Label, req.FileName),
		FileName:   filepath.Base(req.FileName),
		StoredPath: storedRel,
		Mimetype:   canonicalMime,
		SizeBytes:  written,
		SHA256:     sha,
	})
	cleanupTmp()
	if err != nil {
		_ = os.Remove(storedAbs)
		return Asset{}, false, err
	}
	return asset, true, nil
}

// Open returns an io.ReadCloser for a stored asset, verifying shape (path
// traversal, sha length) before touching the disk.
func (a *AssetService) Open(ctx context.Context, userID, assetID int64) (io.ReadCloser, Asset, error) {
	asset, err := a.store.GetAsset(ctx, userID, assetID)
	if err != nil {
		return nil, Asset{}, err
	}
	if !safeRelPath(asset.StoredPath) {
		return nil, Asset{}, errors.New("path aset tidak valid")
	}
	abs := filepath.Join(a.userRoot(userID), asset.StoredPath)
	f, err := os.Open(abs)
	if err != nil {
		return nil, Asset{}, fmt.Errorf("aset tidak tersedia di disk: %w", err)
	}
	return f, asset, nil
}

// Verify re-checks a stored file's sha256 on each send so a modified disk
// cannot smuggle different bytes to the bot.
func (a *AssetService) Verify(ctx context.Context, userID, assetID int64) (Asset, error) {
	f, asset, err := a.Open(ctx, userID, assetID)
	if err != nil {
		return Asset{}, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return Asset{}, err
	}
	if hex.EncodeToString(h.Sum(nil)) != asset.SHA256 {
		return Asset{}, errors.New("checksum aset berubah; tolong unggah ulang")
	}
	return asset, nil
}

// --- helpers ---

func (a *AssetService) userRoot(userID int64) string {
	if a.baseDir != "" {
		return a.baseDir
	}
	return UserAssetsRoot(userID)
}

// enforceQuota walks the user's asset directory and returns an error when the
// total exceeds MaxUserBytes.
func (a *AssetService) enforceQuota(root string) error {
	var total int64
	err := filepath.Walk(root, func(_ string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() || strings.HasPrefix(info.Name(), ".upload-") {
			return nil
		}
		total += info.Size()
		if total > MaxUserBytes {
			return fmt.Errorf("total aset pengguna melebihi batas %d MB", MaxUserBytes>>20)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return nil
}

// storedRelPath mirrors the plan's layout: {sha[:2]}/{sha}{ext}.
func storedRelPath(sha, mime string) string {
	return filepath.Join(sha[:2], sha+extForMime(mime))
}

// sniffMime reads the first 512 bytes from a small file.
func sniffMime(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	if n <= 0 {
		return ""
	}
	return http.DetectContentType(buf[:n])
}

// classifyMime maps the sniffed mime (and optional extension) to an asset kind
// and a canonical mimetype. Mismatches fail. An empty extension is allowed for
// images/PDF (the sniff is reliable); OOXML requires an explicit extension
// because sniffing only reveals the zip container.
func classifyMime(sniffed, ext string) (AssetKind, string, error) {
	base := strings.ToLower(strings.TrimSpace(strings.SplitN(sniffed, ";", 2)[0]))
	switch base {
	case "image/png":
		if ext == "" || ext == ".png" {
			return AssetImage, "image/png", nil
		}
		return "", "", fmt.Errorf("ekstensi %q tidak cocok dengan isi gambar PNG", ext)
	case "image/jpeg":
		if ext == "" || ext == ".jpg" || ext == ".jpeg" {
			return AssetImage, "image/jpeg", nil
		}
		return "", "", fmt.Errorf("ekstensi %q tidak cocok dengan isi gambar JPEG", ext)
	case "application/pdf":
		if ext == "" || ext == ".pdf" {
			return AssetDocument, "application/pdf", nil
		}
		return "", "", fmt.Errorf("ekstensi %q tidak cocok dengan isi PDF", ext)
	case "application/zip", "application/octet-stream":
		if m, ok := ooxmlExt[ext]; ok {
			return AssetDocument, m, nil
		}
		return "", "", errors.New("dokumen tidak dikenal; gunakan pdf/docx/xlsx/pptx")
	}
	return "", "", fmt.Errorf("jenis berkas tidak diizinkan: %s", base)
}

// extForMime maps a canonical mimetype to the on-disk extension.
func extForMime(mime string) string {
	switch mime {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "application/pdf":
		return ".pdf"
	case "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		return ".docx"
	case "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":
		return ".xlsx"
	case "application/vnd.openxmlformats-officedocument.presentationml.presentation":
		return ".pptx"
	}
	return ".bin"
}

// safeRelPath ensures the stored path is relative and free of traversal.
func safeRelPath(p string) bool {
	if p == "" || filepath.IsAbs(p) || strings.Contains(p, "..") {
		return false
	}
	return true
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

func copyTo(src, dst string) error {
	r, err := os.Open(src)
	if err != nil {
		return err
	}
	defer r.Close()
	tmp := dst + ".tmp"
	w, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, r); err != nil {
		w.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := w.Sync(); err != nil {
		w.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := w.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
