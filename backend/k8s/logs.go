package k8s

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// startLogStream 建立 Pod 日志流并在后台 goroutine 里逐行读取，
// 每收到一行调 cb；stream 结束（EOF/错误/context 取消）时调 onEnd。
//
// Same reconnect-with-backoff shape as startWatchStream so a pod
// restart or apiserver bounce does not silently kill the log stream
// (K8S-02). onReconnect is fired on transient transport failures so the
// manager can emit a light reconnecting event without churning its maps
// (F-404).
func startLogStream(ctx context.Context, client *http.Client, base, path string,
	cb func(string), onEnd func(error), onReconnect func(error), done chan struct{}) error {

	if !strings.HasPrefix(path, "/") {
		return fmt.Errorf("path must start with /: %q", path)
	}
	go runLogLoop(ctx, client, base, path, cb, onEnd, onReconnect, done)
	return nil
}

func runLogLoop(ctx context.Context, client *http.Client, base, path string,
	cb func(string), onEnd func(error), onReconnect func(error), done chan struct{}) {
	defer close(done)
	backoff := time.Second
	const maxBackoff = 30 * time.Second
	// lastTS is the timestamp of the most recent delivered line (the UI
	// requests timestamps=true, so each line opens with an RFC3339 stamp).
	// Reconnects resume with sinceTime=lastTS instead of re-sending the
	// original tailLines window, which replayed the last N lines on every
	// apiserver/pod-log connection bounce.
	var lastTS time.Time
	attemptPath := path
	for {
		err := runOneLogStream(ctx, client, base, attemptPath, cb, &lastTS)
		if ctx.Err() != nil {
			onEnd(ctx.Err())
			return
		}
		var statusErr *httpStatusError
		if errors.As(err, &statusErr) && statusErr.terminal() {
			// 401/403/404/… will never succeed; end the stream so the UI
			// stops showing "reconnecting" instead of spinning forever.
			onEnd(err)
			return
		}
		if err == nil {
			// Clean EOF — pod log ended. Surface once and stop.
			onEnd(nil)
			return
		}
		attemptPath = withSinceTime(path, lastTS)
		// Transport / scanner error — likely apiserver bounce. Surface
		// a transient reconnect signal (if wired) and back off; onEnd is
		// reserved for the terminal case so the manager doesn't drop the
		// handle from its map on every reconnect attempt (F-404).
		if onReconnect != nil {
			onReconnect(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < maxBackoff {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

// withSinceTime rewrites the log query to resume just after since and drops
// the tailLines window so the reconnect doesn't replay it. A zero time leaves
// the path untouched (no timestamped line delivered yet).
func withSinceTime(path string, since time.Time) string {
	if since.IsZero() {
		return path
	}
	u, err := url.Parse(path)
	if err != nil {
		return path
	}
	q := u.Query()
	q.Set("sinceTime", since.UTC().Format(time.RFC3339Nano))
	q.Del("tailLines")
	u.RawQuery = q.Encode()
	return u.String()
}

// logLineTS extracts the RFC3339 timestamp the apiserver prepends when
// timestamps=true. ok=false for lines without one (timestamps=false, or a
// message that doesn't start with a parseable stamp).
func logLineTS(line string) (time.Time, bool) {
	sp := strings.IndexByte(line, ' ')
	if sp <= 0 {
		return time.Time{}, false
	}
	ts, err := time.Parse(time.RFC3339Nano, line[:sp])
	if err != nil {
		return time.Time{}, false
	}
	return ts, true
}

func runOneLogStream(ctx context.Context, client *http.Client, base, path string,
	cb func(string), lastTS *time.Time) error {
	req, err := http.NewRequestWithContext(ctx, "GET", base+path, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return readStatusError("log", resp)
	}
	defer resp.Body.Close()
	scanner := bufio.NewScanner(resp.Body)
	// F-412: log lines are typically < 1 KiB; start small and let
	// bufio.Scanner grow on demand. Caps at 4 MiB which is enough for
	// even multi-KiB stack traces from panic'd pods.
	scanner.Buffer(make([]byte, 0, 4*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if ts, ok := logLineTS(line); ok {
			*lastTS = ts
		}
		cb(line)
	}
	return scanner.Err()
}

// buildLogPath 组装 Pod 日志查询；previous 为一次性历史日志，故 follow = !previous。
func buildLogPath(ns, pod, container string, tailLines int, timestamps, previous bool) string {
	follow := !previous
	q := fmt.Sprintf("container=%s&follow=%t&previous=%t&tailLines=%d&timestamps=%t",
		container, follow, previous, tailLines, timestamps)
	return fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/log?%s", ns, pod, q)
}
