package sync

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrWebDAVNotFound reports HTTP 404 from a WebDAV resource read.
var ErrWebDAVNotFound = errors.New("webdav: resource not found")

// WebDAVClient is a minimal WebDAV client: GET/PUT for resources,
// PROPFIND for existence, MKCOL for directory creation. Good enough for
// snapshot sync against common WebDAV servers.
type WebDAVClient struct {
	base     *url.URL // parsed server root (collection root)
	segments []string // basePath split on "/", possibly empty
	user     string
	password string
	http     *http.Client

	// parseErr holds a url.Parse failure of serverURL; it is surfaced by
	// every request instead of at construction (the constructor cannot fail).
	parseErr error
}

// NewWebDAVClient builds a client. basePath is created lazily by MkdirAll.
func NewWebDAVClient(serverURL, basePath, user, password string) *WebDAVClient {
	base, err := url.Parse(strings.TrimRight(serverURL, "/") + "/")
	return &WebDAVClient{
		base:     base,
		segments: splitSegments(basePath),
		user:     user,
		password: password,
		http:     &http.Client{Timeout: 30 * time.Second},
		parseErr: err,
	}
}

func splitSegments(basePath string) []string {
	basePath = strings.Trim(basePath, "/")
	if basePath == "" {
		return nil
	}
	return strings.Split(basePath, "/")
}

func (c *WebDAVClient) rootURL() (*url.URL, error) {
	if c.parseErr != nil {
		return nil, fmt.Errorf("webdav: invalid server url: %w", c.parseErr)
	}
	return c.base, nil
}

// resourceURL returns the URL of a resource, percent-escaping every path
// segment so that spaces, '#', '?' and '%' in names or basePath are safe.
func (c *WebDAVClient) resourceURL(name string) (*url.URL, error) {
	root, err := c.rootURL()
	if err != nil {
		return nil, err
	}
	segs := make([]string, 0, len(c.segments)+1)
	segs = append(segs, c.segments...)
	segs = append(segs, name)
	return root.JoinPath(escapeSegments(segs)...), nil
}

func escapeSegments(segs []string) []string {
	elems := make([]string, len(segs))
	for i, s := range segs {
		elems[i] = url.PathEscape(s)
	}
	return elems
}

func (c *WebDAVClient) auth(req *http.Request) {
	if c.user != "" || c.password != "" {
		req.SetBasicAuth(c.user, c.password)
	}
}

// Get reads a resource. ErrWebDAVNotFound on 404.
func (c *WebDAVClient) Get(name string) ([]byte, error) {
	u, err := c.resourceURL(name)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	c.auth(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("webdav get %s: %w", name, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, ErrWebDAVNotFound
	case resp.StatusCode/100 != 2:
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("webdav get %s: status %d", name, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// Put writes a resource.
func (c *WebDAVClient) Put(name string, data []byte) error {
	u, err := c.resourceURL(name)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPut, u.String(), bytes.NewReader(data))
	if err != nil {
		return err
	}
	c.auth(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("webdav put %s: %w", name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		_, _ = io.Copy(io.Discard, resp.Body)
		return fmt.Errorf("webdav put %s: status %d", name, resp.StatusCode)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// Exists reports whether a resource is present (PROPFIND Depth 0).
func (c *WebDAVClient) Exists(name string) (bool, error) {
	u, err := c.resourceURL(name)
	if err != nil {
		return false, err
	}
	req, err := http.NewRequest("PROPFIND", u.String(), nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Depth", "0")
	c.auth(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return false, fmt.Errorf("webdav propfind %s: %w", name, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	switch {
	case resp.StatusCode == http.StatusMultiStatus:
		return true, nil
	case resp.StatusCode == http.StatusNotFound:
		return false, nil
	default:
		return false, fmt.Errorf("webdav propfind %s: status %d", name, resp.StatusCode)
	}
}

// MkdirAll MKCOLs each missing segment of basePath under the server root.
// 405 (already exists) is treated as success so the call is idempotent;
// any other non-2xx status is a real failure.
func (c *WebDAVClient) MkdirAll() error {
	if len(c.segments) == 0 {
		return nil
	}
	root, err := c.rootURL()
	if err != nil {
		return err
	}
	for i := 1; i <= len(c.segments); i++ {
		u := root.JoinPath(escapeSegments(c.segments[:i])...)
		req, err := http.NewRequest("MKCOL", u.String(), nil)
		if err != nil {
			return err
		}
		c.auth(req)
		resp, err := c.http.Do(req)
		if err != nil {
			return fmt.Errorf("webdav mkcol %s: %w", u, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode/100 != 2 && resp.StatusCode != http.StatusMethodNotAllowed {
			return fmt.Errorf("webdav mkcol %s: status %d", u, resp.StatusCode)
		}
	}
	return nil
}
