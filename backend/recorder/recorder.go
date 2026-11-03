// Package recorder implements terminal session recording and replay support
// using the asciinema cast v2 format.
//
// Cast v2 file layout:
//
//	line 1: JSON header  {"version":2,"width":80,"height":24,"timestamp":<unix>,"env":{...}}
//	line 2..: JSON array [<elapsed_seconds_float>, "o", "<raw_terminal_data>"]
//
// All data lines use raw (non-base64) terminal bytes encoded as a JSON string,
// which preserves ANSI escape sequences for faithful replay in xterm.js.
//
// Files live under <home>/Documents/uniTerm/records/<sessionId>_<unixTs>.cast,
// next to the session output logs. The directory is created on first use. A
// process-wide Recorder instance tracks open file handles so the hot path
// (WriteRecordingData) is a single mutex'd append, not an open/write/close
// cycle per data chunk.
package recorder

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// RecordingInfo describes a saved recording file for the UI list.
type RecordingInfo struct {
	FilePath  string `json:"filePath"`
	FileName  string `json:"fileName"`
	SessionID string `json:"sessionId"`
	Title     string `json:"title"`     // connection name from the header, "" if absent
	StartTime int64  `json:"startTime"` // unix seconds, parsed from filename
	Duration  int64  `json:"duration"`  // seconds, derived from last data row
	Size      int64  `json:"size"`      // bytes
}

// castHeader is the JSON object on line 1 of a cast v2 file.
type castHeader struct {
	Version   int               `json:"version"`
	Width     int               `json:"width"`
	Height    int               `json:"height"`
	Timestamp int64             `json:"timestamp"`
	Title     string            `json:"title,omitempty"`
	Env       map[string]string `json:"env"`
}

// openRecording tracks an in-flight recording's file handle and start time.
type openRecording struct {
	file      *os.File
	startTime time.Time
	startUnix int64
	writeMu   sync.Mutex
}

// Recorder is the process-wide terminal recording manager. Zero value is
// not usable; use New().
type Recorder struct {
	mu      sync.Mutex
	records map[string]*openRecording // keyed by absolute file path
	baseDir string                    // resolved records directory
	dirOnce sync.Once
	dirErr  error
}

// global singleton so the Wails binding layer can call without threading a
// reference through every App method. New() returns the shared instance.
var (
	globalMu   sync.Mutex
	globalInst *Recorder
)

// New returns the process-wide Recorder singleton, creating it on first call.
func New() *Recorder {
	globalMu.Lock()
	defer globalMu.Unlock()
	if globalInst == nil {
		globalInst = &Recorder{records: make(map[string]*openRecording)}
	}
	return globalInst
}

// recordsDir resolves <home>/Documents/uniTerm/records — the same base as the
// session output logs — creating it on first use. Falls back to
// <tmp>/uniterm/records if the home directory is unavailable.
func (r *Recorder) recordsDir() (string, error) {
	r.dirOnce.Do(func() {
		home, err := os.UserHomeDir()
		if err != nil {
			r.baseDir = filepath.Join(os.TempDir(), "uniterm", "records")
		} else {
			r.baseDir = filepath.Join(home, "Documents", "uniTerm", "records")
		}
		r.dirErr = os.MkdirAll(r.baseDir, 0755)
	})
	return r.baseDir, r.dirErr
}

// RecordsDir returns (and creates) the records directory. Used by the App
// layer for the "open folder" action.
func (r *Recorder) RecordsDir() string {
	dir, err := r.recordsDir()
	if err != nil {
		return ""
	}
	return dir
}

// StartRecording creates a new cast file under the records directory, writes
// the v2 header, and returns the absolute file path. The sessionID is
// embedded in the filename; width/height and the human-readable title
// (connection name) populate the header. If a recording for the returned
// path is already open, it is closed first (defensive).
func (r *Recorder) StartRecording(sessionID string, width, height int, title string) (string, error) {
	if sessionID == "" {
		return "", fmt.Errorf("recorder: empty session id")
	}
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}
	dir, err := r.recordsDir()
	if err != nil {
		return "", fmt.Errorf("recorder: records dir: %w", err)
	}
	now := time.Now()
	ts := now.Unix()
	fileName := fmt.Sprintf("%s_%d.cast", sanitizeFileName(sessionID), ts)
	path := filepath.Join(dir, fileName)

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return "", fmt.Errorf("recorder: create file: %w", err)
	}

	header := castHeader{
		Version:   2,
		Width:     width,
		Height:    height,
		Timestamp: ts,
		Title:     title,
		Env: map[string]string{
			"SHELL": os.Getenv("SHELL"),
			"TERM":  "xterm-256color",
		},
	}
	hb, err := json.Marshal(header)
	if err != nil {
		f.Close()
		return "", fmt.Errorf("recorder: marshal header: %w", err)
	}
	// Header is line 1; ensure trailing newline so data rows start on line 2.
	if _, err := f.Write(append(hb, '\n')); err != nil {
		f.Close()
		return "", fmt.Errorf("recorder: write header: %w", err)
	}

	rec := &openRecording{
		file:      f,
		startTime: now,
		startUnix: ts,
	}

	r.mu.Lock()
	// Replace any stale entry for the same path (shouldn't normally happen
	// because the filename embeds a fresh timestamp, but be safe).
	if old := r.records[path]; old != nil {
		old.file.Close()
	}
	r.records[path] = rec
	r.mu.Unlock()

	return path, nil
}

// WriteRecordingData appends one `[elapsed, "o", data]` row to the recording
// at filePath. elapsed is seconds since StartRecording as a float. No-op
// (returns nil) if the path is not an open recording — callers in the hot
// data path should not error out when a recording was stopped concurrently.
//
// The data string is written as a JSON string literal so ANSI escapes and
// arbitrary bytes survive round-trip; we do not base64-encode (cast v2
// permits raw UTF-8 in the data field).
func (r *Recorder) WriteRecordingData(filePath string, data string) error {
	r.mu.Lock()
	rec := r.records[filePath]
	r.mu.Unlock()
	if rec == nil {
		return nil
	}
	elapsed := time.Since(rec.startTime).Seconds()

	rec.writeMu.Lock()
	defer rec.writeMu.Unlock()

	// Build the row manually to avoid the allocation overhead of
	// json.Marshal on a struct + a separate Write. The data field needs
	// JSON string escaping; json.Marshal of a string is the cheapest
	// correct way.
	encoded, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("recorder: encode data: %w", err)
	}
	row := fmt.Sprintf("[%f,\"o\",%s]\n", elapsed, encoded)
	if _, err := rec.file.WriteString(row); err != nil {
		return fmt.Errorf("recorder: write row: %w", err)
	}
	return nil
}

// StopRecording flushes and closes the recording at filePath. Returns nil if
// the path is not currently open (idempotent).
func (r *Recorder) StopRecording(filePath string) error {
	r.mu.Lock()
	rec := r.records[filePath]
	if rec != nil {
		delete(r.records, filePath)
	}
	r.mu.Unlock()
	if rec == nil {
		return nil
	}
	rec.writeMu.Lock()
	defer rec.writeMu.Unlock()
	return rec.file.Close()
}

// IsRecording reports whether the given file path is an open recording.
func (r *Recorder) IsRecording(filePath string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.records[filePath]
	return ok
}

// ListRecordings enumerates every .cast file in the records directory, sorted
// by start time descending (newest first). Each entry's Duration is derived
// from the timestamp of the last data row minus the header timestamp; if
// parsing fails, Duration is 0.
func (r *Recorder) ListRecordings() ([]RecordingInfo, error) {
	dir, err := r.recordsDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []RecordingInfo{}, nil
		}
		return nil, err
	}
	infos := make([]RecordingInfo, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".cast") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		ri := RecordingInfo{
			FilePath:  path,
			FileName:  e.Name(),
			Size:      info.Size(),
			SessionID: parseSessionID(e.Name()),
			Title:     parseHeaderTitle(path),
			StartTime: parseStartTime(e.Name()),
		}
		ri.Duration = deriveDuration(path, ri.StartTime)
		infos = append(infos, ri)
	}
	sort.Slice(infos, func(i, j int) bool {
		return infos[i].StartTime > infos[j].StartTime
	})
	return infos, nil
}

// DeleteRecording removes a recording file from disk. If the recording is
// currently open, it is stopped first.
func (r *Recorder) DeleteRecording(filePath string) error {
	if filePath == "" {
		return fmt.Errorf("recorder: empty file path")
	}
	_ = r.StopRecording(filePath)
	if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("recorder: delete: %w", err)
	}
	return nil
}

// GetRecordingContent reads the entire cast file into a string. Used by the
// frontend player to parse the header + data rows. For very large recordings
// the player could switch to chunked reads, but the typical session is small
// enough to load whole.
func (r *Recorder) GetRecordingContent(filePath string) (string, error) {
	if filePath == "" {
		return "", fmt.Errorf("recorder: empty file path")
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("recorder: read: %w", err)
	}
	return string(data), nil
}

// sanitizeFileName replaces characters that are unsafe in cross-platform
// filenames. SessionIDs are typically UUIDs but defensive coding is cheap.
func sanitizeFileName(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '/' || r == '\\' || r == ':' || r == '*' || r == '?' ||
			r == '"' || r == '<' || r == '>' || r == '|' || r == ' ':
			b.WriteByte('_')
		case r < 0x20:
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "session"
	}
	return b.String()
}

// parseSessionID extracts the leading <sessionId> token from a filename of
// the form "<sessionId>_<unixTs>.cast". Returns "" on any parse failure.
func parseSessionID(name string) string {
	base := strings.TrimSuffix(name, ".cast")
	idx := strings.LastIndex(base, "_")
	if idx <= 0 {
		return ""
	}
	return base[:idx]
}

// parseHeaderTitle reads the cast header (line 1) and returns the connection
// title recorded in it. Returns "" on any parse failure — the UI falls back
// to the session id / file name.
func parseHeaderTitle(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	var header castHeader
	if err := json.NewDecoder(io.LimitReader(f, 4096)).Decode(&header); err != nil {
		return ""
	}
	return header.Title
}

// parseStartTime extracts the unix timestamp from a filename of the form
// "<sessionId>_<unixTs>.cast". Returns 0 on parse failure.
func parseStartTime(name string) int64 {
	base := strings.TrimSuffix(name, ".cast")
	idx := strings.LastIndex(base, "_")
	if idx < 0 || idx == len(base)-1 {
		return 0
	}
	var ts int64
	if _, err := fmt.Sscanf(base[idx+1:], "%d", &ts); err != nil {
		return 0
	}
	return ts
}

// deriveDuration scans the last few KB of the cast file for the final data
// row's elapsed-time field and returns its integer seconds. Returns 0 on
// any failure (file too small, no parseable row, etc.) — Duration is a UI
// hint, not a guarantee.
func deriveDuration(path string, startUnix int64) int64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return 0
	}
	size := info.Size()
	if size <= 0 {
		return 0
	}
	// Read the trailing 8KB (or whole file if smaller) — the last row is
	// what we want; reading the whole file for a long session would be
	// wasteful.
	tail := int64(8 * 1024)
	if size < tail {
		tail = size
	}
	buf := make([]byte, tail)
	if _, err := f.ReadAt(buf, size-tail); err != nil {
		return 0
	}
	// Find the last '[' that begins a row (preceded by '\n' or at start).
	lastRow := -1
	for i := len(buf) - 1; i >= 0; i-- {
		if buf[i] == '\n' && i+1 < len(buf) && buf[i+1] == '[' {
			lastRow = i + 1
			break
		}
	}
	if lastRow < 0 {
		// Maybe the only row is at offset 0 (no trailing newline yet).
		if len(buf) > 0 && buf[0] == '[' {
			lastRow = 0
		} else {
			return 0
		}
	}
	// Parse the leading float (elapsed seconds) from `[<float>,"o",...]`.
	rest := buf[lastRow:]
	if len(rest) < 4 || rest[0] != '[' {
		return 0
	}
	rest = rest[1:]
	end := 0
	for end < len(rest) && rest[end] != ',' {
		end++
	}
	if end == 0 || end == len(rest) {
		return 0
	}
	var elapsed float64
	if _, err := fmt.Sscanf(string(rest[:end]), "%f", &elapsed); err != nil {
		return 0
	}
	return int64(elapsed)
}
