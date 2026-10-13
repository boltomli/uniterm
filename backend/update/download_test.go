package update

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// TestValidateAsset locks in fail-closed asset validation: a candidate is
// rejected when the checksum is missing/malformed or when the URL is not a
// known release source, so no unverified payload can ever be staged.
func TestValidateAsset(t *testing.T) {
	goodSum := sha256Hex([]byte("payload"))

	cases := []struct {
		name    string
		asset   UpdateAsset
		wantErr string // substring; "" = must validate
	}{
		{
			name: "missing sha256 is refused",
			asset: UpdateAsset{
				Name: "uniterm-linux-amd64-v1.9.9.tar.gz",
				URL:  "https://github.com/ys-ll/uniterm/releases/download/v1.9.9/uniterm-linux-amd64-v1.9.9.tar.gz",
			},
			wantErr: "sha256",
		},
		{
			name: "malformed sha256 is refused",
			asset: UpdateAsset{
				Name:   "uniterm-linux-amd64-v1.9.9.tar.gz",
				URL:    "https://github.com/ys-ll/uniterm/releases/download/v1.9.9/uniterm-linux-amd64-v1.9.9.tar.gz",
				SHA256: "not-a-digest",
			},
			wantErr: "sha256",
		},
		{
			name: "foreign host is refused",
			asset: UpdateAsset{
				Name:   "uniterm-linux-amd64-v1.9.9.tar.gz",
				URL:    "https://evil.example.com/uniterm-linux-amd64-v1.9.9.tar.gz",
				SHA256: goodSum,
			},
			wantErr: "host",
		},
		{
			name: "look-alike host is refused",
			asset: UpdateAsset{
				Name:   "uniterm-linux-amd64-v1.9.9.tar.gz",
				URL:    "https://github.com.evil.example/uniterm.tar.gz",
				SHA256: goodSum,
			},
			wantErr: "host",
		},
		{
			name: "plaintext http on a remote host is refused",
			asset: UpdateAsset{
				Name:   "uniterm-linux-amd64-v1.9.9.tar.gz",
				URL:    "http://github.com/ys-ll/uniterm/releases/download/v1.9.9/uniterm.tar.gz",
				SHA256: goodSum,
			},
			wantErr: "scheme",
		},
		{
			name: "github release asset",
			asset: UpdateAsset{
				Name:   "uniterm-linux-amd64-v1.9.9.tar.gz",
				URL:    "https://github.com/ys-ll/uniterm/releases/download/v1.9.9/uniterm-linux-amd64-v1.9.9.tar.gz",
				SHA256: goodSum,
			},
		},
		{
			name: "github cdn redirect target",
			asset: UpdateAsset{
				Name:   "uniterm-linux-amd64-v1.9.9.tar.gz",
				URL:    "https://objects.githubusercontent.com/github-production-release-asset-2e65be/x",
				SHA256: goodSum,
			},
		},
		{
			name: "gitee release asset",
			asset: UpdateAsset{
				Name:   "uniterm-linux-amd64-v1.9.9.tar.gz",
				URL:    "https://gitee.com/ys-l/uniterm/releases/download/v1.9.9/uniterm-linux-amd64-v1.9.9.tar.gz",
				SHA256: goodSum,
			},
		},
		{
			name: "empty url",
			asset: UpdateAsset{
				Name:   "uniterm-linux-amd64-v1.9.9.tar.gz",
				SHA256: goodSum,
			},
			wantErr: "url",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.asset.Validate()
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() = nil, want an error mentioning %q", c.wantErr)
			}
			if !strings.Contains(strings.ToLower(err.Error()), c.wantErr) {
				t.Fatalf("Validate() = %q, want an error mentioning %q", err, c.wantErr)
			}
		})
	}
}

// TestValidateAssetLoopbackReleaseServer: the local end-to-end autotest
// serves release assets from a loopback API base. Loopback URLs are only
// acceptable while that override is active.
func TestValidateAssetLoopbackReleaseServer(t *testing.T) {
	sum := sha256Hex([]byte("payload"))
	loopback := UpdateAsset{
		Name:   "uniterm-linux-amd64-v1.9.9.tar.gz",
		URL:    "http://127.0.0.1:8080/uniterm-linux-amd64-v1.9.9.tar.gz",
		SHA256: sum,
	}

	if err := loopback.Validate(); err == nil {
		t.Fatal("loopback asset URL must be refused without the release-server override")
	}

	t.Setenv("UNITERM_UPDATE_API_BASE", "http://127.0.0.1:8080")
	if err := loopback.Validate(); err != nil {
		t.Fatalf("loopback asset URL must be accepted with a loopback API base: %v", err)
	}

	t.Setenv("UNITERM_UPDATE_API_BASE", "https://attacker.example")
	if err := loopback.Validate(); err == nil {
		t.Fatal("non-loopback API base must not legitimise arbitrary hosts")
	}
}

// assetServer serves a payload on loopback and points the update staging
// directory at a temp dir (os.UserCacheDir reads XDG_CACHE_HOME / LOCALAPPDATA).
// HOME is deliberately untouched: the app log lives there and keeps its file
// handle open, which would break TempDir cleanup on Windows.
func assetServer(t *testing.T, payload []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	t.Cleanup(srv.Close)

	sandbox := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", filepath.Join(sandbox, "cache"))
	t.Setenv("LOCALAPPDATA", filepath.Join(sandbox, "AppData", "Local"))
	return srv
}

// TestDownloadRejectsMissingSHA256 locks in fail-closed download: the old
// code skipped verification whenever cand.SHA256 was empty and staged the
// payload for Apply anyway. The asset is served from a reachable loopback
// server, so pre-fix the download succeeds and this test fails.
func TestDownloadRejectsMissingSHA256(t *testing.T) {
	srv := assetServer(t, []byte("installer-bytes"))
	m := NewManager()
	cand := UpdateAsset{
		Name: "uniterm-windows-amd64-installer-v1.9.9.exe",
		URL:  srv.URL + "/uniterm-windows-amd64-installer-v1.9.9.exe",
	}
	_, err := m.Download([]UpdateAsset{cand}, nil)
	if err == nil {
		t.Fatal("Download must refuse an asset without a sha256 checksum")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "sha256") {
		t.Fatalf("error should name the missing checksum, got: %v", err)
	}
	if m.Pending() != nil {
		t.Fatal("an unverified asset must never be staged for Apply")
	}
}

// TestDownloadRejectsForeignHost: even with a checksum, a URL outside the
// known release sources is refused before any bytes are fetched. The server
// is reachable, so pre-fix this download succeeds and the test fails.
func TestDownloadRejectsForeignHost(t *testing.T) {
	payload := []byte("installer-bytes")
	srv := assetServer(t, payload)
	m := NewManager()
	cand := UpdateAsset{
		Name:   "uniterm-windows-amd64-installer-v1.9.9.exe",
		URL:    srv.URL + "/uniterm-windows-amd64-installer-v1.9.9.exe",
		SHA256: sha256Hex(payload),
	}
	_, err := m.Download([]UpdateAsset{cand}, nil)
	if err == nil {
		t.Fatal("Download must refuse a URL outside the known release sources")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "host") {
		t.Fatalf("error should name the rejected host, got: %v", err)
	}
	if m.Pending() != nil {
		t.Fatal("a refused asset must never be staged for Apply")
	}
}

// TestDownloadVerifiesGoodAsset keeps the happy path working: a checksummed
// asset from an approved source (the loopback release server used by the
// local autotest) downloads and stages.
func TestDownloadVerifiesGoodAsset(t *testing.T) {
	payload := []byte("installer-bytes")
	srv := assetServer(t, payload)
	t.Setenv("UNITERM_UPDATE_API_BASE", srv.URL)

	m := NewManager()
	cand := UpdateAsset{
		Name:   "uniterm-windows-amd64-installer-v1.9.9.exe",
		URL:    srv.URL + "/uniterm-windows-amd64-installer-v1.9.9.exe",
		SHA256: sha256Hex(payload),
	}
	pend, err := m.Download([]UpdateAsset{cand}, nil)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if pend == nil || pend.FilePath == "" {
		t.Fatal("expected a staged update")
	}
	got, err := os.ReadFile(pend.FilePath)
	if err != nil {
		t.Fatalf("read staged file: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("staged payload mismatch: %q", got)
	}
}

// TestDownloadRejectsTamperedAsset: a payload that does not match its
// checksum is dropped, not staged.
func TestDownloadRejectsTamperedAsset(t *testing.T) {
	payload := []byte("installer-bytes")
	srv := assetServer(t, payload)
	t.Setenv("UNITERM_UPDATE_API_BASE", srv.URL)

	m := NewManager()
	cand := UpdateAsset{
		Name:   "uniterm-windows-amd64-installer-v1.9.9.exe",
		URL:    srv.URL + "/uniterm-windows-amd64-installer-v1.9.9.exe",
		SHA256: sha256Hex([]byte("something-else")),
	}
	if _, err := m.Download([]UpdateAsset{cand}, nil); err == nil {
		t.Fatal("Download must fail on a checksum mismatch")
	}
	if m.Pending() != nil {
		t.Fatal("a tampered asset must never be staged for Apply")
	}
}

// TestDownloadClearsStaleStageDirs: every download creates a fresh stage-*
// directory and Clear() deliberately keeps installer stages for the detached
// updater script, so repeated check-then-cancel cycles used to leave about
// 100 MB each in the user cache. A new download supersedes all previous
// staging, so afterwards only the freshly staged directory may remain.
func TestDownloadClearsStaleStageDirs(t *testing.T) {
	payload := []byte("installer-bytes")
	srv := assetServer(t, payload)
	t.Setenv("UNITERM_UPDATE_API_BASE", srv.URL)

	root, err := updateCacheDir()
	if err != nil {
		t.Fatalf("updateCacheDir: %v", err)
	}
	for _, name := range []string{"stage-leftover-one", "stage-leftover-two"} {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "uniterm.exe"), []byte("old payload"), 0644); err != nil {
			t.Fatalf("seed %s payload: %v", name, err)
		}
	}

	m := NewManager()
	cand := UpdateAsset{
		Name:   "uniterm-windows-amd64-installer-v1.9.9.exe",
		URL:    srv.URL + "/uniterm-windows-amd64-installer-v1.9.9.exe",
		SHA256: sha256Hex(payload),
	}
	pend, err := m.Download([]UpdateAsset{cand}, nil)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read staging root: %v", err)
	}
	var stages []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "stage-") {
			stages = append(stages, e.Name())
		}
	}
	if len(stages) != 1 || filepath.Join(root, stages[0]) != pend.StageDir {
		t.Fatalf("staging root should hold exactly the fresh stage dir, got %v (staged %s)", stages, pend.StageDir)
	}
}
