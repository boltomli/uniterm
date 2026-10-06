//go:build android

package session

import (
	"encoding/json"
	"fmt"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/ys-ll/uniterm/backend/log"
)

// startForegroundKeepalive promotes the app to a foreground service so the
// process is exempt from cached-app freezing while terminals are connected.
// The payload is rendered by WailsBridge into the low-importance
// "wails_foreground" notification channel; startForegroundService requires
// some user-visible notification to be present.
func startForegroundKeepalive(n int) {
	payload, _ := json.Marshal(struct {
		Title string `json:"title"`
		Text  string `json:"text"`
	}{
		Title: "uniTerm",
		Text:  fmt.Sprintf("%d active connection(s)", n),
	})
	application.Android.StartForegroundService(string(payload))
	log.Writef("[keepalive] StartForegroundService dispatched (%s)", payload)
}

// stopForegroundKeepalive demotes the app once the last connection ended, so
// a backgrounded uniTerm no longer holds a notification or battery budget.
func stopForegroundKeepalive() {
	application.Android.StopForegroundService()
	log.Writef("[keepalive] StopForegroundService dispatched")
}
