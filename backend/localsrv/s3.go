package localsrv

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// s3Time formats a time as the ISO 8601 UTC string that S3 clients expect
// (e.g. "2024-12-03T03:16:14.649Z"). Using a plain string instead of
// time.Time avoids Go's RFC3339Nano XML encoding, which emits a local timezone
// offset and nanosecond digits that strict S3 clients fail to parse.
func s3Time(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

// StartS3Server starts an experimental S3-compatible server that exposes the
// local directory cfg.RootDir as an object store over the Amazon S3 REST API.
//
// Buckets map to sub-directories of RootDir and objects map to files (the
// object key is the path relative to the bucket directory). Only a practical
// subset of S3 is implemented — enough for standard S3 clients (e.g.
// `mc --no-sign-request`, `aws s3 --no-sign-request`) to create buckets and
// upload / download / list / delete objects. Returns the server id.
//
// The access/secret keys are accepted for compatibility with clients that send
// them, but signatures are not cryptographically verified (experimental).
func (m *Manager) StartS3Server(cfg S3Config) (string, error) {
	if cfg.Port <= 0 || cfg.Port > 65535 {
		return "", fmt.Errorf("invalid port: %d", cfg.Port)
	}
	absRoot, err := filepath.Abs(cfg.RootDir)
	if err != nil {
		return "", fmt.Errorf("invalid root directory: %w", err)
	}
	if err := os.MkdirAll(absRoot, 0o755); err != nil {
		return "", fmt.Errorf("cannot create root directory: %w", err)
	}

	id, ctx := m.newServer()
	m.log(ServiceS3, id, fmt.Sprintf("Starting S3-compatible server on port %d (experimental) ...", cfg.Port))

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.log(ServiceS3, id, fmt.Sprintf("%s %s %s", r.RemoteAddr, r.Method, r.URL.RequestURI()))
		m.handleS3(w, r, id, absRoot, cfg)
	})

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.Port),
		Handler: handler,
	}

	ln, err := listen(ctx, "tcp", srv.Addr)
	if err != nil {
		m.finish(id)
		return "", fmt.Errorf("failed to listen on port %d: %w", cfg.Port, err)
	}

	m.setStop(id, func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	})

	go func() {
		defer m.finish(id)
		m.state(ServiceS3, id, true)
		m.log(ServiceS3, id, "S3-compatible server started (experimental)")
		m.log(ServiceS3, id, fmt.Sprintf("Object store root: %s", absRoot))
		m.log(ServiceS3, id, "Client hint: mc --no-sign-request alias set local http://<host>:/ ; mc ls local")
		err := srv.Serve(ln)
		if err != nil && err != http.ErrServerClosed {
			m.log(ServiceS3, id, fmt.Sprintf("S3 server error: %v", err))
		}
		m.state(ServiceS3, id, false)
		m.log(ServiceS3, id, "S3-compatible server stopped")
	}()

	m.startTimeout(id, ServiceS3, cfg.TimeoutSeconds)
	return id, nil
}

// bucketBasename returns the root directory used to store the objects of one
// bucket: root/<bucket>. The bucket name is validated to avoid traversal.
func bucketDir(root, bucket string) (string, error) {
	if bucket == "" || bucket == "." || bucket == ".." ||
		strings.ContainsAny(bucket, `/\`) {
		return "", fmt.Errorf("invalid bucket name")
	}
	return filepath.Join(root, bucket), nil
}

// objectPath returns the on-disk path bd/<key>, i.e. one object file inside
// the bucket directory bd. The key is validated to stay inside bd.
func objectPath(bd, key string) (string, error) {
	if key == "" {
		return "", fmt.Errorf("empty object key")
	}
	clean := filepath.Clean(filepath.FromSlash(key))
	obj := filepath.Join(bd, clean)
	rel, err := filepath.Rel(bd, obj)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid object key")
	}
	return obj, nil
}

func writeXMLError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	_ = xml.NewEncoder(w).Encode(struct {
		XMLName xml.Name `xml:"Error"`
		Code    string   `xml:"Code"`
		Message string   `xml:"Message"`
	}{Code: code, Message: message})
}

// handleS3 routes an incoming HTTP request to the matching S3 operation.
func (m *Manager) handleS3(w http.ResponseWriter, r *http.Request, id string, root string, cfg S3Config) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	seg := strings.SplitN(path, "/", 2)
	bucket := seg[0]
	key := ""
	if len(seg) == 2 {
		key = seg[1]
	}

	switch {
	case bucket == "":
		// Root path: list buckets.
		m.handleListBuckets(w, root)
		return
	}

	bd, berr := bucketDir(root, bucket)
	if berr != nil {
		writeXMLError(w, http.StatusBadRequest, "InvalidBucketName", "invalid bucket name")
		return
	}

	switch {

	case key == "" && r.Method == http.MethodPut:
		// PUT /{bucket} → create bucket.
		if err := os.MkdirAll(bd, 0o755); err != nil {
			writeXMLError(w, http.StatusInternalServerError, "InternalError", err.Error())
			return
		}
		w.WriteHeader(http.StatusOK)

	case key == "" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		// GET /{bucket} → list objects; HEAD /{bucket} → bucket does it exist.
		if r.Method == http.MethodHead {
			if _, serr := os.Stat(bd); serr != nil {
				writeXMLError(w, http.StatusNotFound, "NoSuchBucket", "bucket does not exist")
				return
			}
			w.WriteHeader(http.StatusOK)
			return
		}
		m.handleListObjects(w, r, bd, bucket)

	case key == "" && r.Method == http.MethodDelete:
		// DELETE /{bucket} → delete bucket (must be empty).
		m.handleDeleteBucket(w, bd)

	case key == "" && r.Method == http.MethodPost && strings.Contains(r.URL.RawQuery, "delete"):
		// POST /{bucket}?delete → DeleteObjects (S3 batch delete).
		m.handleDeleteObjects(w, r, bd)

	case key != "" && r.Method == http.MethodPut:
		// PUT /{bucket}/{key} → put object, or copy object when the
		// x-amz-copy-source header is present (S3 CopyObject).
		if src := r.Header.Get("x-amz-copy-source"); src != "" {
			m.handleCopyObject(w, bd, key, src)
			return
		}
		m.handlePutObject(w, r, bd, key)

	case key != "" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		// GET/HEAD /{bucket}/{key} → get/head object.
		m.handleGetObject(w, r, bd, key)

	case key != "" && r.Method == http.MethodDelete:
		// DELETE /{bucket}/{key} → delete object.
		m.handleDeleteObject(w, bd, key)

	default:
		writeXMLError(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "method not allowed")
	}
}

// handleListBuckets responds with the list of sub-directories under root, each
// treated as a bucket.
func (m *Manager) handleListBuckets(w http.ResponseWriter, root string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		writeXMLError(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}
	type bucket struct {
		Name         string `xml:"Name"`
		CreationDate string `xml:"CreationDate"`
	}
	var buckets []bucket
	for _, e := range entries {
		if e.IsDir() {
			fi, err := e.Info()
			if err != nil {
				continue
			}
			buckets = append(buckets, bucket{Name: e.Name(), CreationDate: s3Time(fi.ModTime())})
		}
	}
	if buckets == nil {
		buckets = []bucket{}
	}

	resp := struct {
		XMLName xml.Name `xml:"ListAllMyBucketsResult"`
		Owner   struct {
			ID          string `xml:"ID"`
			DisplayName string `xml:"DisplayName"`
		} `xml:"Owner"`
		Buckets []bucket `xml:"Buckets>Bucket"`
	}{}
	resp.Owner.ID = "local"
	resp.Owner.DisplayName = "local"
	resp.Buckets = buckets
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	_ = xml.NewEncoder(w).Encode(resp)
}

// handleListObjects supports both ListObjectsV2 (?list-type=2) and the basic
// ListObjects (?prefix=&delimiter=&max-keys=).
func (m *Manager) handleListObjects(w http.ResponseWriter, r *http.Request, bd, bucket string) {
	prefix := r.URL.Query().Get("prefix")
	delimiter := r.URL.Query().Get("delimiter")
	maxKeys := 1000
	if mk := r.URL.Query().Get("max-keys"); mk != "" {
		if n, err := strconv.Atoi(mk); err == nil && n >= 0 {
			maxKeys = n
		}
	}
	continuation := r.URL.Query().Get("continuation-token")
	listType2 := r.URL.Query().Get("list-type") == "2"
	_ = continuation // pagination is out of scope for this subset

	searchDir := filepath.Join(bd, filepath.FromSlash(prefix))
	searchDir = filepath.Clean(searchDir)
	rel, rerr := filepath.Rel(bd, searchDir)
	if rerr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		writeXMLError(w, http.StatusBadRequest, "InvalidPrefix", "invalid prefix")
		return
	}

	type content struct {
		Key          string `xml:"Key"`
		LastModified string `xml:"LastModified"`
		ETag         string `xml:"ETag"`
		Size         int64  `xml:"Size"`
		StorageClass string `xml:"StorageClass"`
	}
	var objects []content
	var prefixes []string
	seenPrefixes := map[string]bool{}

	var walk func(dir string) error
	walk = func(dir string) error {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, e := range entries {
			relPath, _ := filepath.Rel(bd, filepath.Join(dir, e.Name()))
			s3Key := filepath.ToSlash(relPath)
			if e.IsDir() {
				sub := s3Key + "/"
				if delimiter != "" {
					// Emit a CommonPrefix at the delimiter boundary.
					idx := strings.Index(sub, delimiter)
					if idx >= 0 {
						cp := sub[:idx+len(delimiter)]
						if !seenPrefixes[cp] {
							seenPrefixes[cp] = true
							prefixes = append(prefixes, cp)
						}
						continue
					}
				}
				if err := walk(filepath.Join(dir, e.Name())); err != nil {
					return err
				}
				continue
			}
			if strings.HasSuffix(s3Key, "/") {
				continue
			}
			fi, err := e.Info()
			if err != nil {
				continue
			}
			objects = append(objects, content{
				Key:          s3Key,
				LastModified: s3Time(fi.ModTime()),
				ETag:         fmt.Sprintf(`"%x-%x"`, fi.Size(), fi.ModTime().UnixNano()),
				Size:         fi.Size(),
				StorageClass: "STANDARD",
			})
		}
		return nil
	}
	if err := walk(searchDir); err != nil && !os.IsNotExist(err) {
		writeXMLError(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	if listType2 {
		type commonPrefix struct {
			Prefix string `xml:"Prefix"`
		}
		commonPrefixes := make([]commonPrefix, 0, len(prefixes))
		for _, p := range prefixes {
			commonPrefixes = append(commonPrefixes, commonPrefix{Prefix: p})
		}
		resp := struct {
			XMLName        xml.Name       `xml:"ListBucketResult"`
			Name           string         `xml:"Name"`
			Prefix         string         `xml:"Prefix"`
			KeyCount       int            `xml:"KeyCount"`
			MaxKeys        int            `xml:"MaxKeys"`
			IsTruncated    bool           `xml:"IsTruncated"`
			Contents       []content      `xml:"Contents"`
			CommonPrefixes []commonPrefix `xml:"CommonPrefixes"`
			Delimiter      string         `xml:"Delimiter,omitempty"`
		}{
			Name:           bucket,
			Prefix:         prefix,
			KeyCount:       len(objects) + len(prefixes),
			MaxKeys:        maxKeys,
			IsTruncated:    false,
			Contents:       objects,
			CommonPrefixes: commonPrefixes,
			Delimiter:      delimiter,
		}
		_ = xml.NewEncoder(w).Encode(resp)
		return
	}
	resp := struct {
		XMLName        xml.Name  `xml:"ListBucketResult"`
		Name           string    `xml:"Name"`
		Prefix         string    `xml:"Prefix"`
		Marker         string    `xml:"Marker"`
		MaxKeys        int       `xml:"MaxKeys"`
		Delimiter      string    `xml:"Delimiter,omitempty"`
		IsTruncated    bool      `xml:"IsTruncated"`
		Contents       []content `xml:"Contents"`
		CommonPrefixes []struct {
			Prefix string `xml:"Prefix"`
		} `xml:"CommonPrefixes,omitempty"`
	}{
		Name:        bucket,
		Prefix:      prefix,
		MaxKeys:     maxKeys,
		Delimiter:   delimiter,
		IsTruncated: len(objects) == maxKeys,
		Contents:    objects,
	}
	for _, p := range prefixes {
		resp.CommonPrefixes = append(resp.CommonPrefixes, struct {
			Prefix string `xml:"Prefix"`
		}{Prefix: p})
	}
	_ = xml.NewEncoder(w).Encode(resp)
}

func (m *Manager) handleDeleteBucket(w http.ResponseWriter, bd string) {
	entries, err := os.ReadDir(bd)
	if err != nil {
		if os.IsNotExist(err) {
			writeXMLError(w, http.StatusNotFound, "NoSuchBucket", "bucket does not exist")
			return
		}
		writeXMLError(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}
	if len(entries) > 0 {
		writeXMLError(w, http.StatusConflict, "BucketNotEmpty", "bucket is not empty")
		return
	}
	if err := os.Remove(bd); err != nil {
		writeXMLError(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (m *Manager) handlePutObject(w http.ResponseWriter, r *http.Request, bd, key string) {
	obj, err := objectPath(bd, key)
	if err != nil {
		writeXMLError(w, http.StatusBadRequest, "InvalidKey", err.Error())
		return
	}
	if _, serr := os.Stat(bd); serr != nil && os.IsNotExist(serr) {
		writeXMLError(w, http.StatusNotFound, "NoSuchBucket", "bucket does not exist")
		return
	}

	// A key ending in "/" is a folder marker (S3's convention for the
	// "create folder" operation in GUIs). Create the directory instead of a
	// file so clients like the Java AWS SDK can create folders.
	if strings.HasSuffix(key, "/") {
		if err := os.MkdirAll(obj, 0o755); err != nil {
			writeXMLError(w, http.StatusInternalServerError, "InternalError", err.Error())
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	}

	if err := os.MkdirAll(filepath.Dir(obj), 0o755); err != nil {
		writeXMLError(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}
	f, err := os.Create(obj)
	if err != nil {
		writeXMLError(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}
	defer f.Close()
	// Plain body (S3 also allows multipart form uploads; unsupported here).
	if _, err := io.Copy(f, r.Body); err != nil {
		writeXMLError(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}
	w.WriteHeader(http.StatusOK)
}

// handleCopyObject implements the S3 CopyObject operation (PUT with the
// x-amz-copy-source header). The source is copied server-side into <key> under
// the destination bucket directory bd. Directories are copied recursively.
func (m *Manager) handleCopyObject(w http.ResponseWriter, bd, key, src string) {
	// The x-amz-copy-source header has the form "/<bucket>/<key>" with the key
	// URL-encoded by the client; it may also carry a "?versionId=" suffix.
	src = strings.SplitN(src, "?", 2)[0]
	source := strings.TrimPrefix(src, "/")
	parts := strings.SplitN(source, "/", 2)
	srcBucket := parts[0]
	srcKey := ""
	if len(parts) == 2 {
		if decoded, err := url.PathUnescape(parts[1]); err == nil {
			srcKey = decoded
		} else {
			srcKey = parts[1]
		}
	}

	srcBd, err := bucketDir(filepath.Dir(bd), srcBucket)
	if err != nil {
		writeXMLError(w, http.StatusBadRequest, "InvalidBucketName", "invalid source bucket")
		return
	}
	srcObj, err := objectPath(srcBd, srcKey)
	if err != nil {
		writeXMLError(w, http.StatusBadRequest, "InvalidKey", err.Error())
		return
	}
	dstObj, err := objectPath(bd, key)
	if err != nil {
		writeXMLError(w, http.StatusBadRequest, "InvalidKey", err.Error())
		return
	}

	fi, err := os.Stat(srcObj)
	if err != nil {
		if os.IsNotExist(err) {
			writeXMLError(w, http.StatusNotFound, "NoSuchKey", "source object does not exist")
			return
		}
		writeXMLError(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}

	if fi.IsDir() {
		// Recursively copy a source directory into the destination key.
		if err := os.MkdirAll(dstObj, 0o755); err != nil {
			writeXMLError(w, http.StatusInternalServerError, "InternalError", err.Error())
			return
		}
		filepath.Walk(srcObj, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(srcObj, p)
			target := filepath.Join(dstObj, rel)
			if info.IsDir() {
				if rel != "." {
					return os.MkdirAll(target, 0o755)
				}
				return nil
			}
			return copyFile(p, target)
		})
	} else {
		if err := os.MkdirAll(filepath.Dir(dstObj), 0o755); err != nil {
			writeXMLError(w, http.StatusInternalServerError, "InternalError", err.Error())
			return
		}
		if err := copyFile(srcObj, dstObj); err != nil {
			writeXMLError(w, http.StatusInternalServerError, "InternalError", err.Error())
			return
		}
	}

	dstFi, _ := os.Stat(dstObj)
	etag := ""
	if dstFi != nil && !dstFi.IsDir() {
		etag = fmt.Sprintf(`"%x-%x"`, dstFi.Size(), dstFi.ModTime().UnixNano())
	}
	resp := struct {
		XMLName xml.Name `xml:"CopyObjectResult"`
		ETag    string   `xml:"ETag"`
	}{ETag: etag}
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	_ = xml.NewEncoder(w).Encode(resp)
}

// copyFile copies a single file from src to dst, overwriting any existing file.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

func (m *Manager) handleGetObject(w http.ResponseWriter, r *http.Request, bd, key string) {
	obj, err := objectPath(bd, key)
	if err != nil {
		writeXMLError(w, http.StatusBadRequest, "InvalidKey", err.Error())
		return
	}
	fi, err := os.Stat(obj)
	if err != nil {
		if os.IsNotExist(err) {
			writeXMLError(w, http.StatusNotFound, "NoSuchKey", "object does not exist")
			return
		}
		writeXMLError(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}
	if fi.IsDir() {
		writeXMLError(w, http.StatusNotFound, "NoSuchKey", "object is a directory")
		return
	}
	f, err := os.Open(obj)
	if err != nil {
		writeXMLError(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}
	defer f.Close()
	w.Header().Set("Content-Length", strconv.FormatInt(fi.Size(), 10))
	w.Header().Set("Last-Modified", fi.ModTime().UTC().Format(http.TimeFormat))
	w.Header().Set("ETag", fmt.Sprintf(`"%x-%x"`, fi.Size(), fi.ModTime().UnixNano()))
	w.Header().Set("Content-Type", "application/octet-stream")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, f)
}

func (m *Manager) handleDeleteObject(w http.ResponseWriter, bd, key string) {
	obj, err := objectPath(bd, key)
	if err != nil {
		writeXMLError(w, http.StatusBadRequest, "InvalidKey", err.Error())
		return
	}
	if err := os.Remove(obj); err != nil {
		if os.IsNotExist(err) {
			// Idempotent delete — S3 returns 204 even for missing objects.
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeXMLError(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDeleteObjects implements the S3 DeleteObjects API (POST /{bucket}?delete).
// The request body is an XML document listing objects to delete; already-absent
// keys are treated as successfully deleted (per S3 semantics).
func (m *Manager) handleDeleteObjects(w http.ResponseWriter, r *http.Request, bd string) {
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeXMLError(w, http.StatusBadRequest, "InvalidRequest", err.Error())
		return
	}

	var req struct {
		Objects []struct {
			Key string `xml:"Key"`
		} `xml:"Object"`
	}
	if err := xml.Unmarshal(body, &req); err != nil {
		writeXMLError(w, http.StatusBadRequest, "MalformedXML", err.Error())
		return
	}

	type deleted struct {
		Key string `xml:"Key"`
	}
	del := make([]deleted, 0, len(req.Objects))
	for _, o := range req.Objects {
		obj, perr := objectPath(bd, o.Key)
		if perr != nil {
			continue
		}
		// Keys ending with "/" represent folders; remove them recursively.
		if strings.HasSuffix(o.Key, "/") {
			_ = os.RemoveAll(obj)
		} else {
			_ = os.Remove(obj)
		}
		del = append(del, deleted{Key: o.Key})
	}

	resp := struct {
		XMLName xml.Name  `xml:"DeleteResult"`
		Deleted []deleted `xml:"Deleted"`
	}{Deleted: del}
	w.Header().Set("Content-Type", "application/xml")
	_ = xml.NewEncoder(w).Encode(resp)
}
