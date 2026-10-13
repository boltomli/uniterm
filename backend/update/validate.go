package update

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
)

// allowedAssetHosts are the hosts a release asset may be downloaded from:
// the two release sources themselves plus the GitHub asset CDN that
// browser_download_url redirects resolve to. Anything else (a mirror, a
// compromised webview, a poisoned cache entry) is refused before a single
// byte is fetched.
var allowedAssetHosts = map[string]bool{
	"github.com":                           true,
	"objects.githubusercontent.com":        true,
	"release-assets.githubusercontent.com": true,
	"gitee.com":                            true,
}

// allowedAssetHostSuffixes covers CDN hostnames under the known owners without
// accepting a look-alike such as "githubusercontent.com.evil.example" (the
// leading dot anchors the match).
var allowedAssetHostSuffixes = []string{
	".githubusercontent.com",
}

// Validate reports why this asset must not be downloaded. It fails closed:
// a missing or malformed checksum, an empty/malformed URL, a non-allow-listed
// host or a plaintext scheme on a remote host all refuse the candidate.
func (a UpdateAsset) Validate() error {
	if strings.TrimSpace(a.Name) == "" {
		return errors.New("update asset has no name")
	}
	if a.SHA256 == "" {
		return fmt.Errorf("%s: missing sha256 checksum; refusing to install an unverified update", a.Name)
	}
	if len(a.SHA256) != 64 {
		return fmt.Errorf("%s: malformed sha256 checksum", a.Name)
	}
	if _, err := hex.DecodeString(a.SHA256); err != nil {
		return fmt.Errorf("%s: malformed sha256 checksum", a.Name)
	}
	return validateAssetURL(a.URL)
}

// validateAssetURL enforces the release-source allowlist for one asset URL.
// Loopback URLs are accepted only while UNITERM_UPDATE_API_BASE points at a
// loopback release server (the local end-to-end autotest); plaintext http is
// accepted only for that same loopback server.
func validateAssetURL(raw string) error {
	if raw == "" {
		return errors.New("asset url is empty")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("invalid asset url %q", raw)
	}
	host := strings.ToLower(u.Hostname())
	if u.Scheme != "https" && u.Scheme != "http" {
		return fmt.Errorf("asset url scheme %q is not supported for host %q", u.Scheme, host)
	}
	if u.Scheme == "http" && !isLoopbackHost(host) {
		return fmt.Errorf("asset url scheme http is refused for host %q", host)
	}
	if allowedAssetHost(host) {
		return nil
	}
	if loopbackReleaseServer() && isLoopbackHost(host) {
		return nil
	}
	return fmt.Errorf("asset url host %q is not a known release source", host)
}

// allowedAssetHost reports whether host is one of the known release sources.
func allowedAssetHost(host string) bool {
	if allowedAssetHosts[host] {
		return true
	}
	for _, suffix := range allowedAssetHostSuffixes {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}

// loopbackReleaseServer reports whether the local release-server override is
// active (and loopback, as enforced by IsLoopbackAPIBase).
func loopbackReleaseServer() bool {
	return IsLoopbackAPIBase(os.Getenv("UNITERM_UPDATE_API_BASE"))
}

// isLoopbackHost reports whether host is a loopback IP literal or "localhost".
func isLoopbackHost(host string) bool {
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return host == "localhost"
}
