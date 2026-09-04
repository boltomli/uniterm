package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	stdsync "sync"
	"sync/atomic"
	"time"

	"go.bug.st/serial"

	"github.com/google/uuid"
	"github.com/wailsapp/wails/v3/pkg/application"
	"golang.org/x/crypto/ssh"

	"github.com/ys-ll/uniterm/backend/container"
	"github.com/ys-ll/uniterm/backend/credentials"
	"github.com/ys-ll/uniterm/backend/database"
	"github.com/ys-ll/uniterm/backend/importer"
	"github.com/ys-ll/uniterm/backend/k8s"
	"github.com/ys-ll/uniterm/backend/log"
	"github.com/ys-ll/uniterm/backend/platform"
	"github.com/ys-ll/uniterm/backend/session"
	"github.com/ys-ll/uniterm/backend/store"
	"github.com/ys-ll/uniterm/backend/sync"
	"github.com/ys-ll/uniterm/backend/update"
)

type App struct {
	ctx                  context.Context
	app                  *application.App
	window               *application.WebviewWindow
	sessionManager       *session.SessionManager
	k8sManager           *k8s.Manager
	containerManager     *container.Manager
	connectionStore      *store.ConnectionStore
	aiSessionStore       *store.AISessionStore
	settingsStore        *store.SettingsStore
	identityStore        *store.IdentityStore
	proxyStore           *store.ProxyStore
	localStateStore      *store.LocalStateStore
	quickCommandsStore   *store.QuickCommandsStore
	skillsStore          *store.SkillsStore
	commandsStore        *store.CommandsStore
	tunnelStore          *store.TunnelStore
	terminalHistoryStore *store.TerminalHistoryStore
	recentStore          *store.RecentStore
	syncService          *sync.SyncService
	tunnelService        *session.TunnelService
	mainHwnd             uintptr
	originalWndProc      uintptr
	wndProcCb            uintptr // keep alive to prevent GC
	inSizeMove           bool
	webviewDataPath      string
	// dataDir is the resolved config data directory, passed to store
	// constructors. Resolved at startup; finalized by bootstrap in a later task.
	dataDir         string
	credentialStore *credentials.Store
	storesReady     bool
	chatCancel      atomic.Pointer[context.CancelFunc] // F-308: active stream cancellation, per-call swap so overlap is safe
	moveResizeCh    chan string                        // defer EventsEmit from WndProc
	// F-043: foreground flag — true while the window is visible and the
	// user is interacting; background goroutines (keepalive, output_log
	// flush, k8s watches, auto-sync) should consult IsForeground before
	// burning CPU. Updated via SetAppVisibility (frontend bridge) and a
	// low-frequency minimised poll as a fallback for paths that don't
	// fire visibilitychange (e.g. app hidden via Cmd+H on macOS).
	foreground   atomic.Bool
	foregroundMu stdsync.RWMutex
	// F-212: last seen connections snapshot so emitConnDelta (F-204) can
	// compute upsert/remove deltas without re-shipping the full store on
	// every save.
	lastConnSnapshot   session.ConnectionStoreData
	lastConnSnapshotMu stdsync.RWMutex

	// F-208: single shared http.Client for chatCompletion* /
	// FetchModels calls. Built lazily once on first use so tests that
	// don't hit the LLM path don't pay for the transport; subsequent
	// calls reuse the keep-alive pool and skip the TCP+TLS handshake.
	httpClient     *http.Client
	httpClientOnce stdsync.Once

	// Session output log state (issue #227). Logs are keyed by panelID so
	// they survive reconnects — a single panel may cycle through many
	// session objects and the log file spans all of them. sessionToPanel
	// tracks the current session→panel binding so emitData can look up
	// the right logger. panelAutoTriggered records which panels have
	// already been considered for the LogOnConnect auto-enable so
	// reconnects don't re-enable a log the user manually stopped.
	panelLogs          map[string]*session.OutputLogger
	sessionToPanel     map[string]string
	panelAutoTriggered map[string]bool
	panelLogMu         stdsync.Mutex
	// customLogDir, when non-empty, overrides defaultSessionLogDir()
	// as the target for new session logs. Set from settings via
	// SetDefaultSessionLogDir; ongoing logs are not migrated.
	customLogDir   string
	customLogDirMu stdsync.RWMutex

	// errCh accumulates non-fatal init failures during startup() so the
	// frontend can surface them (see StartupError / "app:startup-error"
	// event). Stores may stay nil if their init fails — the existing
	// nil-guard pattern is preserved so today's working configs keep
	// loading; the additive channel just makes the failure visible.
	errCh      chan error
	startupErr error
}

func NewApp(webviewDataPath string) *App {
	return &App{
		webviewDataPath:    webviewDataPath,
		panelLogs:          make(map[string]*session.OutputLogger),
		sessionToPanel:     make(map[string]string),
		panelAutoTriggered: make(map[string]bool),
		k8sManager:         k8s.NewManager(),
		containerManager:   container.NewManager(),
		errCh:              make(chan error, 16),
	}
}

// emit is a v3 helper that forwards an event to the frontend. It no-ops when
// the application reference is not yet attached (e.g. in unit tests that build
// an App without a running Wails runtime), matching the previous
// `if a.ctx != nil` defensiveness.
func (a *App) emit(name string, data ...any) {
	if a.app == nil {
		return
	}
	a.app.Event.Emit(name, data...)
}

// win returns the single application window, used to drive window operations
// from the v3 application object.
func (a *App) win() *application.WebviewWindow {
	return a.window
}

func (a *App) ServiceStartup(ctx context.Context, options application.ServiceOptions) error {
	a.ctx = ctx

	a.k8sManager.SetEventEmitter(func(name string, payload any) {
		a.emit(name, payload)
	})
	a.containerManager.SetEventEmitter(func(name string, payload any) {
		a.emit(name, payload)
	})

	// Init logger first so subsequent log.Writef calls actually write
	if err := log.Init(); err != nil {
		fmt.Printf("WARN: log.Init failed: %v\n", err)
	}

	// On macOS, disable the system press-and-hold accent picker for this app so
	// that holding a key repeats input in the terminal (see app_darwin.go).
	a.configureMacKeyRepeat()

	a.sessionManager = session.NewSessionManager()
	a.tunnelService = session.NewTunnelService()

	// Defer EventsEmit from WndProc to avoid blocking the modal resize/move loop.
	a.moveResizeCh = make(chan string, 10)
	go func() {
		for evt := range a.moveResizeCh {
			a.emit(evt)
			if evt == "rdp:move-resize-end" {
				// Notify the frontend that the native window stopped resizing
				// (drag-to-restore / maximize / programmatic resize). The terminal
				// re-fits on this signal because the browser does not always fire a
				// final window.resize at the settled size after a native drag, which
				// otherwise leaves the canvas at a stale small row count (issue #656).
				// Platform-neutral name; on non-Windows it simply never fires.
				a.emit("window:resize-end")
				a.saveWindowStateFromRuntime()
			}
		}
	}()

	// Discover main window HWND for RDP child window embedding
	a.mainHwnd = a.findMainWindow()
	a.subclassMainWindow()

	// Resolve data directory. First run (no bootstrap, no existing config)
	// defers all store init until the frontend calls SetDataDir.
	dd, err := store.ResolveDataDir()
	if err != nil {
		log.Writef("Failed to resolve data dir: %v", err)
		a.sendStartupErr(fmt.Errorf("data dir: %w", err))
		a.drainStartupErr()
		return nil
	}
	if dd.FirstRun {
		a.dataDir = ""
		a.emit("app:firstRun", nil)
		a.drainStartupErr()
		return nil
	}
	a.dataDir = dd.Path

	a.initStores(dd.Path, dd.Upgrade)
	return nil
}

// initStores initializes every config store under dataDir and brings up the
// credential store + sync service. Extracted from startup so first-run defers
// it until SetDataDir picks a directory; on the normal path it runs once at
// startup. Runs exactly once either way.
func (a *App) initStores(dataDir string, upgrade bool) {
	cs, err := store.NewConnectionStore(dataDir)
	if err != nil {
		log.Writef("Failed to init connection store: %v", err)
		a.sendStartupErr(fmt.Errorf("connection store: %w", err))
	} else {
		a.connectionStore = cs
	}

	ass, err := store.NewAISessionStore(dataDir)
	if err != nil {
		log.Writef("Failed to init AI session store: %v", err)
		a.sendStartupErr(fmt.Errorf("ai session store: %w", err))
	} else {
		a.aiSessionStore = ass
	}

	ss, err := store.NewSettingsStore(dataDir)
	if err != nil {
		log.Writef("Failed to init settings store: %v", err)
		a.sendStartupErr(fmt.Errorf("settings store: %w", err))
	} else {
		a.settingsStore = ss
		// Prime the session-log directory override from persisted settings
		// so a log Enable that lands before the settings UI opens still
		// respects the user's choice from a prior run.
		if settings, err := ss.Load(); err == nil {
			a.SetDefaultSessionLogDir(settings.Terminal.SessionLogDir)
		}
	}

	is, err := store.NewIdentityStore(dataDir)
	if err != nil {
		log.Writef("Failed to init identity store: %v", err)
		a.sendStartupErr(fmt.Errorf("identity store: %w", err))
	} else {
		a.identityStore = is
	}

	ps, err := store.NewProxyStore(dataDir)
	if err != nil {
		log.Writef("Failed to init proxy store: %v", err)
		a.sendStartupErr(fmt.Errorf("proxy store: %w", err))
	} else {
		a.proxyStore = ps
	}

	a.terminalHistoryStore = store.NewTerminalHistoryStore(dataDir)
	a.quickCommandsStore = store.NewQuickCommandsStore(dataDir)
	a.skillsStore = store.NewSkillsStore(dataDir)
	a.commandsStore = store.NewCommandsStore(dataDir)
	a.tunnelStore = store.NewTunnelStore(dataDir)
	a.localStateStore = store.NewLocalStateStore(dataDir)
	a.recentStore = store.NewRecentStore(dataDir)
	if _, err := a.recentStore.Load(); err != nil {
		log.Writef("recentStore.Load: %v", err)
	}

	// Push tunnel runtime state to the frontend, and bring up auto-start tunnels.
	a.tunnelService.SetStateCallback(func(st session.TunnelState) {
		a.emit("tunnel:state", st)
	})
	go a.autoStartTunnels()
	go a.watchForeground(a.ctx)

	// Credential store + auto-unlock / upgrade (wires PasswordStore into
	// connection + settings stores).
	a.initCredentials(dataDir, upgrade)

	// Sync service: sync metadata (sync-config.json + local repo clone) lives
	// in the system user-config dir; the config files it encrypts/decrypts are
	// read from dataDir.
	syncSvc, err := sync.NewSyncService(dataDir)
	if err != nil {
		log.Writef("Failed to create sync service: %v", err)
		a.sendStartupErr(fmt.Errorf("sync service: %w", err))
	} else {
		a.syncService = syncSvc
		// Normalize enc:v1: fields at the sync boundary using the credential
		// store (set by initCredentials above).
		syncSvc.SetPasswordStore(a.credentialStore)
		// Auto-sync on startup if enabled
		if syncSvc.IsAutoSyncEnabled() {
			go func() {
				result, err := syncSvc.Sync()
				if err != nil {
					log.Writef("Auto-sync on startup failed: %v", err)
				} else if result.Direction == sync.SyncConflict {
					a.emit("sync:conflict", map[string]interface{}{
						"localTime":  result.Conflict.LocalTime.Format(time.RFC3339),
						"remoteTime": result.Conflict.RemoteTime.Format(time.RFC3339),
					})
				}
				// Reload in-memory stores after a startup pull so the UI shows
				// the freshly synced config without requiring a restart.
				if err == nil && result.Direction == sync.SyncPull {
					a.reloadStoresAfterSync()
				}
				a.emit("sync:completed")
			}()
		}
	}

	a.storesReady = true

	// Raise the window to the foreground once, shortly after launch. On Windows a
	// relaunched instance can otherwise land behind other windows; the short delay
	// keeps this one-shot raise inside the window where the old (foreground)
	// process is still alive, which is what grants the set-foreground permission
	// (see RelaunchApp). No-op on other platforms.
	go func() {
		time.Sleep(250 * time.Millisecond)
		a.bringMainWindowToFront()
	}()

	// Drain any non-fatal init failures and surface them to the frontend so
	// the user sees a banner instead of getting an NPE on the first store
	// call. Additive only — stores that failed to init are still nil and
	// guarded as before; the app still launches.
	a.drainStartupErr()
	if a.startupErr != nil {
		a.emit("app:startup-error", a.startupErr.Error())
	}
}

// initCredentials wires the credential store as the PasswordStore for the
// connection + settings stores, silently auto-upgrading existing users
// (bootstrap + keychain mode + legacy migration) or auto-unlocking on the
// normal path.
func (a *App) initCredentials(dataDir string, upgrade bool) {
	cred := credentials.New(dataDir, sync.NewKeychain())

	if upgrade {
		// Only this once (the upgrade migration) is worth logging; ordinary
		// starts share the same AutoUnlock path and would be noise.
		m, _ := credentials.ReadMeta(dataDir)
		if m != nil {
			log.Writef("[upgrade] existing credentials.meta mode=%q", m.Mode)
		} else {
			log.Writef("[upgrade] no credentials.meta → auto-setup keychain")
		}
		// Existing user: silently auto-upgrade to default + keychain mode.
		if err := store.WriteBootstrap("default", ""); err != nil {
			log.Writef("bootstrap write failed (default pointer): %v", err)
		}
		// Idempotency (review Critical #1): Setup generates a NEW random key and
		// overwrites the keychain entry. If a prior upgrade already ran but
		// bootstrap.json was lost (deleted / <exe>/data unwritable / portable zip
		// extracted to a new folder), credentials.meta still exists and the fields
		// are already encrypted under the persisted key — running Setup again would
		// orphan them. Recover the existing key instead.
		if meta, _ := credentials.ReadMeta(dataDir); meta == nil {
			if err := cred.Setup(credentials.ModeKeychain, ""); err != nil {
				log.Writef("credential auto-upgrade setup failed: %v", err)
			} else if _, err := store.MigrateLegacyKeychainToInPlace(dataDir, sync.NewKeychain(), cred); err != nil {
				log.Writef("legacy migration failed: %v", err)
			}
		} else if err := cred.AutoUnlock(); err != nil {
			log.Writef("credential auto-unlock failed: %v", err)
		}
		// Re-run the legacy migration on every upgrade. It is idempotent (only
		// backfills connections whose JSON password field is still empty) and
		// closes the window where a user's first-launch migration failed and
		// was then skipped forever because credentials.meta already existed.
		// Only meaningful when the credential store holds a usable key.
		if cred.Unlocked() {
			if _, err := store.MigrateLegacyKeychainToInPlace(dataDir, sync.NewKeychain(), cred); err != nil {
				log.Writef("legacy migration failed: %v", err)
			}
		}
	} else if err := cred.AutoUnlock(); err != nil {
		log.Writef("credential auto-unlock failed: %v", err)
	}

	a.credentialStore = cred
	// Only log when startup lands in a state that prompts a credential dialog
	// (setup/keychain-lost), so a normal unlocked start stays silent.
	if st := cred.Status(); st.NeedsSetup || st.KeychainLost {
		log.Writef("[cred-dialog] mode=%q unlocked=%v keychainLost=%v needsSetup=%v",
			st.Mode, st.Unlocked, st.KeychainLost, st.NeedsSetup)
	}
	if a.connectionStore != nil {
		a.connectionStore.SetPasswordStore(cred)
		// Fall back to the pre-enc:v1 keychain (conn/<id>) so passwords from
		// the old scheme remain usable if the one-shot migration didn't run.
		a.connectionStore.SetLegacyKeychain(sync.NewKeychain())
	}
	if a.settingsStore != nil {
		a.settingsStore.SetPasswordStore(cred)
	}
	if a.identityStore != nil {
		a.identityStore.SetPasswordStore(cred)
	}
	if a.proxyStore != nil {
		a.proxyStore.SetPasswordStore(cred)
	}
}

// sendStartupErr records a non-fatal init failure so the frontend can see
// it after startup completes. Channel is buffered (16) and only written
// from the startup goroutine, so the send is non-blocking.
func (a *App) sendStartupErr(err error) {
	if err == nil {
		return
	}
	select {
	case a.errCh <- err:
	default:
		// Channel full — best-effort drop. The log line is the
		// last-resort record in this case.
		log.Writef("startup error channel full, dropping: %v", err)
	}
}

// drainStartupErr joins every error sent during startup into a single
// startupErr the frontend can query via StartupError().
func (a *App) drainStartupErr() {
	var errs []error
	for {
		select {
		case err := <-a.errCh:
			if err != nil {
				errs = append(errs, err)
			}
		default:
			a.startupErr = errors.Join(errs...)
			return
		}
	}
}

// StartupError returns a human-readable, newline-joined list of any
// non-fatal errors that occurred during startup, or "" if startup
// completed cleanly. The frontend can call this on demand (e.g. after
// the "app:startup-error" event) to display a banner.
func (a *App) StartupError() string {
	if a.startupErr == nil {
		return ""
	}
	return a.startupErr.Error()
}

// saveWindowStateFromRuntime saves the current window geometry using runtime
// API calls. Called from the WndProc event loop on Windows (WM_EXITSIZEMOVE).
func (a *App) saveWindowStateFromRuntime() {
	if a.localStateStore == nil {
		return
	}
	// Do not save geometry when minimised — the position is off-screen
	// (-32000, -32000 on Windows) and the size is the tiny taskbar thumbnail,
	// which would restore incorrectly.
	if a.win().IsMinimised() {
		return
	}
	ls, err := a.localStateStore.Load()
	if err != nil {
		return
	}
	ls.WindowX, ls.WindowY = a.window.Position()
	ls.WindowWidth, ls.WindowHeight = a.window.Size()
	ls.WindowMaximised = a.window.IsMaximised()
	_ = a.localStateStore.Save(ls)
}

func (a *App) SaveWindowState(x, y, width, height int, maximised bool) {
	if a.localStateStore == nil {
		return
	}
	ls, err := a.localStateStore.Load()
	if err != nil {
		return
	}
	ls.WindowX = x
	ls.WindowY = y
	ls.WindowWidth = width
	ls.WindowHeight = height
	ls.WindowMaximised = maximised
	a.localStateStore.Save(ls)
}

// IsForeground reports whether the app window is currently in the
// foreground. Background goroutines consult this before running work
// that should pause when the user can't see the terminal (F-043).
func (a *App) IsForeground() bool {
	return a.foreground.Load()
}

// SetAppVisibility is the lifecycle hook the frontend fires from
// document.visibilitychange. It updates the foreground flag, emits a
// `app:visibility` event so other Go-side listeners (e.g. auto-sync,
// AI SSE keepalive) can pause/resume, and is safe to call from any
// goroutine.
//
// Pass visible=false when the page goes hidden (tab switch, OS minimise,
// Cmd+H, etc.). The polling goroutine started in startup() is a
// fallback for cases where the JS event doesn't fire (e.g. macOS Cmd+H
// before any document has loaded).
func (a *App) SetAppVisibility(visible bool) {
	prev := a.foreground.Load()
	if prev == visible {
		return
	}
	a.foreground.Store(visible)
	a.foregroundMu.Lock()
	a.foregroundMu.Unlock()
	if a.ctx != nil {
		a.emit("app:visibility", visible)
	}
}

// connDelta is the wire shape for store:connections:delta — only the
// changed connection (or all connections on first emit) crosses the
// bridge instead of the full store blob. See F-204.
type connDelta struct {
	Kind string                       `json:"kind"`         // "upsert" | "remove" | "replace"
	ID   string                       `json:"id,omitempty"` // for upsert/remove
	Conn *session.ConnectionConfig    `json:"connection,omitempty"`
	All  *session.ConnectionStoreData `json:"all,omitempty"` // for replace (first emit)
}

// F-205: typed event shapes + pooled buffer so session:data emits
// stop allocating a fresh map[string]interface{} per chunk.
type sessionDataEvent struct {
	ID   string `json:"id"`
	Data string `json:"data"`
}

type sessionBinaryEvent struct {
	ID   string `json:"id"`
	Data string `json:"data"`
}

var sessionDataPool = stdsync.Pool{
	New: func() any {
		b := &bytes.Buffer{}
		b.Grow(8 * 1024) // typical SSH chunk size, avoids re-grow on small inputs
		return b
	},
}

// computeConnDelta returns the set of upsert/remove deltas between
// the last snapshot and newData. If no snapshot exists yet (first save
// after startup), returns a single "replace" delta carrying the full
// new data so the frontend can hydrate without waiting for a sync.
func (a *App) computeConnDelta(newData session.ConnectionStoreData) []connDelta {
	a.lastConnSnapshotMu.RLock()
	prev := a.lastConnSnapshot
	a.lastConnSnapshotMu.RUnlock()

	if prev.Connections == nil && prev.Groups == nil {
		// F-204: no prior snapshot — ship a single replace so the
		// frontend can hydrate without waiting for sync.
		all := newData
		return []connDelta{{Kind: "replace", All: &all}}
	}

	prevIDs := make(map[string]struct{}, len(prev.Connections))
	for _, c := range prev.Connections {
		prevIDs[c.ID] = struct{}{}
	}
	newIDs := make(map[string]struct{}, len(newData.Connections))
	for _, c := range newData.Connections {
		newIDs[c.ID] = struct{}{}
	}

	var deltas []connDelta
	for _, c := range newData.Connections {
		if _, ok := prevIDs[c.ID]; !ok {
			cc := c
			deltas = append(deltas, connDelta{Kind: "upsert", ID: c.ID, Conn: &cc})
		}
	}
	for id := range prevIDs {
		if _, ok := newIDs[id]; !ok {
			deltas = append(deltas, connDelta{Kind: "remove", ID: id})
		}
	}
	return deltas
}

// saveConnSnapshot updates the snapshot used for future delta
// computation. Called after every successful Save.
func (a *App) saveConnSnapshot(data session.ConnectionStoreData) {
	a.lastConnSnapshotMu.Lock()
	a.lastConnSnapshot = data
	a.lastConnSnapshotMu.Unlock()
}

// llmHTTPClient returns the App-wide *http.Client used by every
// LLM-bound call. F-208: hoisted here so three back-to-back
// ChatCompletion calls reuse the same TCP+TLS connection instead of
// paying a fresh handshake each time. FetchModels uses a shorter
// timeout via a derived client (see FetchModels).
func (a *App) llmHTTPClient() *http.Client {
	a.httpClientOnce.Do(func() {
		tr := &http.Transport{
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   8,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
			ExpectContinueTimeout: 2 * time.Second,
		}
		a.httpClient = &http.Client{Transport: tr}
	})
	return a.httpClient
}

// injectCacheControl adds ephemeral cache_control breakpoints on the
// static system prompt and tools array so Anthropic's prompt caching
// beta actually caches them across turns. Without this the
// prompt-caching-2024-07-31 header is sent but the request body has
// no breakpoints, so every turn re-ships and re-bills the static
// prefix (~3 KB in typical Claude Code sessions). F-303.
func injectCacheControl(reqBody map[string]interface{}) {
	if sys, ok := reqBody["system"].(string); ok && sys != "" {
		reqBody["system"] = []map[string]interface{}{{
			"type":          "text",
			"text":          sys,
			"cache_control": map[string]string{"type": "ephemeral"},
		}}
	}
	if tools, ok := reqBody["tools"].([]interface{}); ok && len(tools) > 0 {
		if last, ok := tools[len(tools)-1].(map[string]interface{}); ok {
			last["cache_control"] = map[string]string{"type": "ephemeral"}
		}
	}
}

// which don't fire the JS visibilitychange event (Cmd+H on macOS before
// the WebView is loaded, OS-level Alt+Tab) still update the foreground
// flag. Runs every 2s — coarse on purpose, this is a lifecycle hint not
// a hot path. Exits when ctx is done.
func (a *App) watchForeground(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if a.ctx == nil {
				continue
			}
			visible := !a.win().IsMinimised()
			if visible != a.foreground.Load() {
				a.SetAppVisibility(visible)
			}
		}
	}
}

func (a *App) shutdown() {
	a.unsubclassMainWindow()
	if a.tunnelService != nil {
		a.tunnelService.Shutdown()
	}
	if a.sessionManager != nil {
		a.sessionManager.CloseAll()
	}
	cleanupExtEditsOnExit()
	session.CleanupSSHX11Server()
	if a.terminalHistoryStore != nil {
		_ = a.terminalHistoryStore.Close()
	}
	os.RemoveAll(a.webviewDataPath)
}

// ConnectionStore methods

func (a *App) SaveConnections(data session.ConnectionStoreData) error {
	if a.connectionStore == nil {
		return fmt.Errorf("connection store not initialized")
	}
	err := a.connectionStore.Save(data)
	if err == nil {
		a.emit("store:connections:changed", data)
		a.triggerAutoSync()
	}
	return err
}

func (a *App) LoadConnections() (session.ConnectionStoreData, error) {
	if a.connectionStore == nil {
		return session.ConnectionStoreData{}, fmt.Errorf("connection store not initialized")
	}
	return a.connectionStore.Load()
}

func (a *App) LoadIdentities() (session.IdentityStoreData, error) {
	if a.identityStore == nil {
		return session.IdentityStoreData{Identities: []session.Identity{}}, nil
	}
	return a.identityStore.Load()
}

func (a *App) SaveIdentities(data session.IdentityStoreData) error {
	if a.identityStore == nil {
		return fmt.Errorf("identity store not initialized")
	}
	return a.identityStore.Save(data)
}

func (a *App) LoadProxies() (session.ProxyStoreData, error) {
	if a.proxyStore == nil {
		return session.ProxyStoreData{Proxies: []session.Proxy{}}, nil
	}
	return a.proxyStore.Load()
}

func (a *App) SaveProxies(data session.ProxyStoreData) error {
	if a.proxyStore == nil {
		return fmt.Errorf("proxy store not initialized")
	}
	return a.proxyStore.Save(data)
}

// ExportConnections writes the full store to destPath as a .utm file. When
// password is non-empty, password fields are encrypted; otherwise cleared.
func (a *App) ExportConnections(destPath, password string) error {
	if a.connectionStore == nil {
		return fmt.Errorf("connection store not initialized")
	}
	data, err := a.connectionStore.Load()
	if err != nil {
		return err
	}
	out, err := importer.ExportUniterm(data, password)
	if err != nil {
		return err
	}
	return os.WriteFile(destPath, out, 0600)
}

// ParseImportFile parses a third-party or own-format file into an ImportResult
// with regenerated ids. It does not write to the store.
func (a *App) ParseImportFile(format, srcPath, password string) (*importer.ImportResult, error) {
	return importer.Parse(format, srcPath, importer.ParseOptions{Password: password})
}

// ApplyImport merges parsed connections into the existing store and saves,
// reusing existing groups by path. The saved result is broadcast via the
// existing store:connections:changed event.
func (a *App) ApplyImport(data session.ConnectionStoreData) error {
	if a.connectionStore == nil {
		return fmt.Errorf("connection store not initialized")
	}
	existing, err := a.connectionStore.Load()
	if err != nil {
		return err
	}
	merged := importer.MergeImported(existing, data)
	return a.SaveConnections(merged)
}

// TunnelStore methods

func (a *App) SaveTunnels(data session.TunnelStoreData) error {
	if a.tunnelStore == nil {
		return fmt.Errorf("tunnel store not initialized")
	}
	err := a.tunnelStore.Save(data)
	if err == nil {
		a.emit("store:tunnels:changed", data)
	}
	return err
}

func (a *App) LoadTunnels() (session.TunnelStoreData, error) {
	if a.tunnelStore == nil {
		return session.TunnelStoreData{}, fmt.Errorf("tunnel store not initialized")
	}
	return a.tunnelStore.Load()
}

// connResolver returns a resolver over the current saved connections so the
// tunnel layer can look up the exit connection and recurse its jump hosts.
func (a *App) connResolver() (session.ConnResolver, error) {
	conns, err := a.connectionStore.Load()
	if err != nil {
		return nil, err
	}
	index := make(map[string]session.ConnectionConfig, len(conns.Connections))
	for _, c := range conns.Connections {
		index[c.ID] = c
	}
	idResolve, err := a.identityResolver()
	if err != nil {
		return nil, err
	}
	return func(id string) (session.ConnectionConfig, bool) {
		c, ok := index[id]
		if !ok {
			return session.ConnectionConfig{}, false
		}
		if c.AuthType == "identity" {
			m, err := session.MaterializeIdentity(c, idResolve)
			if err != nil {
				return session.ConnectionConfig{}, false
			}
			return m, true
		}
		return c, true
	}, nil
}

// identityResolver 返回基于当前身份库的解密 resolver（镜像 connResolver）。
func (a *App) identityResolver() (session.IdentityResolver, error) {
	if a.identityStore == nil {
		return func(string) (session.Identity, bool) { return session.Identity{}, false }, nil
	}
	data, err := a.identityStore.Load()
	if err != nil {
		return nil, err
	}
	index := make(map[string]session.Identity, len(data.Identities))
	for _, id := range data.Identities {
		index[id.ID] = id
	}
	return func(id string) (session.Identity, bool) {
		ident, ok := index[id]
		return ident, ok
	}, nil
}

// materializeIdentity resolves an identity-reference config into a concrete
// password/key config. No-op for non-identity configs.
func (a *App) materializeIdentity(config session.ConnectionConfig) (session.ConnectionConfig, error) {
	if config.AuthType != "identity" {
		return config, nil
	}
	resolve, err := a.identityResolver()
	if err != nil {
		return config, err
	}
	return session.MaterializeIdentity(config, resolve)
}

// proxyResolver returns a resolver over the saved proxies (mirrors identityResolver).
func (a *App) proxyResolver() (session.ProxyResolver, error) {
	if a.proxyStore == nil {
		return func(string) (session.SocksProxy, bool) { return session.SocksProxy{}, false }, nil
	}
	data, err := a.proxyStore.Load()
	if err != nil {
		return nil, err
	}
	index := make(map[string]session.Proxy, len(data.Proxies))
	for _, p := range data.Proxies {
		index[p.ID] = p
	}
	return func(id string) (session.SocksProxy, bool) {
		p, ok := index[id]
		if !ok || !p.IsActive() {
			return session.SocksProxy{}, false
		}
		return session.SocksProxy{Kind: p.Kind, Host: p.Host, Port: p.Port, User: p.User, Pass: p.Pass}, true
	}, nil
}

// materializeProxy resolves config.ProxyId into config.Proxy. No-op when no
// proxy is set. Mirrors materializeIdentity. A proxy disabled via its enable
// toggle (issue #749) is skipped silently: the connection dials directly.
func (a *App) materializeProxy(config session.ConnectionConfig) (session.ConnectionConfig, error) {
	if config.ProxyId == "" {
		return config, nil
	}
	resolve, err := a.proxyResolver()
	if err != nil {
		return config, err
	}
	p, ok := resolve(config.ProxyId)
	if !ok {
		if name, disabled := a.proxyDisabledName(config.ProxyId); disabled {
			log.Writef("[materializeProxy] proxy %q disabled, connecting direct", name)
			return config, nil
		}
		return config, fmt.Errorf("referenced proxy not found: %s", config.ProxyId)
	}
	config.Proxy = &p
	return config, nil
}

// proxyDisabledName reports whether the referenced proxy exists but is toggled
// off, returning its name for logging.
func (a *App) proxyDisabledName(id string) (string, bool) {
	if a.proxyStore == nil {
		return "", false
	}
	data, err := a.proxyStore.Load()
	if err != nil {
		return "", false
	}
	for _, p := range data.Proxies {
		if p.ID == id {
			return p.Name, !p.IsActive()
		}
	}
	return "", false
}

// StartTunnel brings the tunnel with the given ID up and returns its state.
func (a *App) StartTunnel(id string) (session.TunnelState, error) {
	if a.tunnelService == nil || a.tunnelStore == nil || a.connectionStore == nil {
		return session.TunnelState{}, fmt.Errorf("tunnel service not initialized")
	}
	data, err := a.tunnelStore.Load()
	if err != nil {
		return session.TunnelState{}, err
	}
	var t *session.Tunnel
	for i := range data.Tunnels {
		if data.Tunnels[i].ID == id {
			t = &data.Tunnels[i]
			break
		}
	}
	if t == nil {
		return session.TunnelState{}, fmt.Errorf("tunnel %s not found", id)
	}
	resolve, err := a.connResolver()
	if err != nil {
		return session.TunnelState{}, err
	}
	st := a.tunnelService.StartTunnel(*t, resolve)
	if st.Status == session.TunnelError {
		return st, fmt.Errorf("%s", st.Error)
	}
	return st, nil
}

// StopTunnel tears down the tunnel with the given ID.
func (a *App) StopTunnel(id string) error {
	if a.tunnelService != nil {
		a.tunnelService.StopTunnel(id)
	}
	return nil
}

// ListTunnelStates returns the runtime state of every known tunnel.
func (a *App) ListTunnelStates() []session.TunnelState {
	if a.tunnelService == nil {
		return nil
	}
	return a.tunnelService.TunnelStates()
}

// autoStartTunnels starts every tunnel flagged AutoStart. Errors surface via the
// per-tunnel state event, not as a startup failure.
func (a *App) autoStartTunnels() {
	if a.tunnelService == nil || a.tunnelStore == nil || a.connectionStore == nil {
		return
	}
	data, err := a.tunnelStore.Load()
	if err != nil {
		return
	}
	resolve, err := a.connResolver()
	if err != nil {
		return
	}
	for _, t := range data.Tunnels {
		if t.AutoStart {
			a.tunnelService.StartTunnel(t, resolve)
		}
	}
}

// AI Config Store methods

func (a *App) SaveAIConfig(config store.AIConfig) error {
	if a.settingsStore == nil {
		return fmt.Errorf("settings store not initialized")
	}
	settings, err := a.settingsStore.Load()
	if err != nil {
		return fmt.Errorf("load settings: %w", err)
	}
	// Update the active model's fields
	for i := range settings.AI.Models {
		if settings.AI.Models[i].ID == settings.AI.ActiveModelID {
			settings.AI.Models[i].APIKey = config.APIKey
			settings.AI.Models[i].BaseURL = config.BaseURL
			settings.AI.Models[i].Model = config.Model
			break
		}
	}
	if err := a.settingsStore.Save(settings); err != nil {
		return err
	}
	a.triggerAutoSync()
	return nil
}

// LocalStateStore methods — sidecar visibility that stays local, never synced.

func (a *App) SaveLocalState(state store.LocalState) error {
	if a.localStateStore == nil {
		return fmt.Errorf("local state store not initialized")
	}
	return a.localStateStore.Save(state)
}

func (a *App) LoadLocalState() (store.LocalState, error) {
	if a.localStateStore == nil {
		return store.LocalState{SidebarVisible: true, AISidebarVisible: true}, nil
	}
	return a.localStateStore.Load()
}

// bgDir returns the directory holding the (local-only, never-synced)
// background image. It is rooted under the active data directory so the
// image moves with the config when the data dir is migrated. It is
// created on demand.
func (a *App) bgDir() (string, error) {
	base := a.dataDir
	if base == "" {
		var err error
		base, err = store.DefaultDataDir()
		if err != nil {
			return "", err
		}
	}
	dir := filepath.Join(base, "backgrounds")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	return dir, nil
}

var allowedBgExt = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".webp": "image/webp",
}

// SetBackgroundImage copies the chosen image into the app's backgrounds
// directory as a single fixed file (overwriting any previous one) and
// returns the stored file name. It does NOT touch local_state.json.
func (a *App) SetBackgroundImage(srcPath string) (string, error) {
	ext := strings.ToLower(filepath.Ext(srcPath))
	if _, ok := allowedBgExt[ext]; !ok {
		return "", fmt.Errorf("unsupported image type: %s", ext)
	}
	dir, err := a.bgDir()
	if err != nil {
		return "", err
	}
	for e := range allowedBgExt {
		_ = os.Remove(filepath.Join(dir, "bg"+e))
	}
	src, err := os.Open(srcPath)
	if err != nil {
		return "", err
	}
	defer src.Close()
	name := "bg" + ext
	dst, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		return "", err
	}
	defer dst.Close()
	if _, err := io.Copy(dst, src); err != nil {
		return "", err
	}
	return name, nil
}

// GetBackgroundImage reads the stored background file and returns it as a
// data URL. Returns an empty string (no error) when name is empty or the
// file is missing, so the frontend degrades gracefully.
func (a *App) GetBackgroundImage(name string) (string, error) {
	if name == "" {
		return "", nil
	}
	ext := strings.ToLower(filepath.Ext(name))
	mime, ok := allowedBgExt[ext]
	if !ok {
		return "", nil
	}
	dir, err := a.bgDir()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

// ClearBackgroundImage removes any stored background image file.
func (a *App) ClearBackgroundImage() error {
	dir, err := a.bgDir()
	if err != nil {
		return err
	}
	for e := range allowedBgExt {
		_ = os.Remove(filepath.Join(dir, "bg"+e))
	}
	return nil
}

// reloadStoresAfterSync reloads connections and settings from disk and emits
// events so the frontend refreshes after a sync pull.
func (a *App) reloadStoresAfterSync() {
	if a.connectionStore != nil {
		if data, err := a.connectionStore.Load(); err == nil {
			a.emit("store:connections:changed", data)
		}
	}
	if a.settingsStore != nil {
		if settings, err := a.settingsStore.Load(); err == nil {
			a.emit("store:settings:changed", settings)
		}
	}
	if a.quickCommandsStore != nil {
		if data, err := a.quickCommandsStore.Load(); err == nil {
			a.emit("store:quickCommands:changed", data)
		}
	}
	if a.identityStore != nil {
		if data, err := a.identityStore.Load(); err == nil {
			a.emit("store:identities:changed", data)
		}
	}
	if a.proxyStore != nil {
		if data, err := a.proxyStore.Load(); err == nil {
			a.emit("store:proxies:changed", data)
		}
	}
}

func (a *App) triggerAutoSync() {
	if a.syncService == nil || !a.syncService.IsAutoSyncEnabled() {
		return
	}
	go func() {
		result, err := a.syncService.Sync()
		if err != nil {
			log.Writef("Auto-sync failed: %v", err)
		} else if result.Direction == sync.SyncConflict {
			a.emit("sync:conflict", map[string]interface{}{
				"localTime":  result.Conflict.LocalTime.Format(time.RFC3339),
				"remoteTime": result.Conflict.RemoteTime.Format(time.RFC3339),
			})
		}
		if err == nil && result.Direction == sync.SyncPull {
			a.reloadStoresAfterSync()
		}
		a.emit("sync:completed")
	}()
}

// waitSyncReady briefly blocks on the async NewSyncService's Ready()
// channel so callers that arrive during the ~ms-scale startup window
// don't fail with "sync service not initialized" (F-407). Returns
// true once ready, false on timeout.
func (a *App) waitSyncReady(timeout time.Duration) bool {
	if a.syncService == nil {
		return false
	}
	select {
	case <-a.syncService.Ready():
		return true
	case <-time.After(timeout):
		return false
	}
}

func (a *App) SyncGetConfig() (sync.SyncConfig, error) {
	if a.syncService == nil {
		return sync.SyncConfig{}, fmt.Errorf("sync service not initialized")
	}
	if !a.waitSyncReady(time.Second) {
		return sync.SyncConfig{}, fmt.Errorf("sync service still initializing")
	}
	return a.syncService.GetConfig()
}

// SyncSaveConfig saves the sync configuration.
func (a *App) SyncSaveConfig(config sync.SyncConfig, token string) error {
	if a.syncService == nil {
		return fmt.Errorf("sync service not initialized")
	}
	if !a.waitSyncReady(time.Second) {
		return fmt.Errorf("sync service still initializing")
	}
	return a.syncService.SaveConfig(config, token)
}

// SyncNow runs an immediate sync.
func (a *App) SyncNow() (*sync.SyncResult, error) {
	if a.syncService == nil {
		return nil, fmt.Errorf("sync service not initialized")
	}
	if !a.waitSyncReady(time.Second) {
		return nil, fmt.Errorf("sync service still initializing")
	}
	result, err := a.syncService.Sync()
	if err != nil {
		return nil, err
	}
	if result.Direction == sync.SyncConflict {
		a.emit("sync:conflict", map[string]interface{}{
			"localTime":  result.Conflict.LocalTime.Format(time.RFC3339),
			"remoteTime": result.Conflict.RemoteTime.Format(time.RFC3339),
		})
	}
	if result.Direction == sync.SyncPull {
		a.reloadStoresAfterSync()
	}
	a.emit("sync:completed")
	return result, nil
}

// SyncResolveConflict resolves a sync conflict.
func (a *App) SyncResolveConflict(useLocal bool) (*sync.SyncResult, error) {
	if a.syncService == nil {
		return nil, fmt.Errorf("sync service not initialized")
	}
	if !a.waitSyncReady(time.Second) {
		return nil, fmt.Errorf("sync service still initializing")
	}
	result, err := a.syncService.ResolveConflict(useLocal)
	if err != nil {
		return nil, err
	}
	if result.Direction == sync.SyncPull {
		a.reloadStoresAfterSync()
	}
	return result, nil
}

// SyncTestConnection tests the repository connection.
func (a *App) SyncTestConnection() error {
	if a.syncService == nil {
		return fmt.Errorf("sync service not initialized")
	}
	if !a.waitSyncReady(time.Second) {
		return fmt.Errorf("sync service still initializing")
	}
	return a.syncService.TestConnection()
}

// SyncConfigureRepo sets up a new or existing sync repository.
func (a *App) SyncConfigureRepo(repoURL, username, token, masterPassword string) (*sync.SyncResult, error) {
	if a.syncService == nil {
		return nil, fmt.Errorf("sync service not initialized")
	}
	if !a.waitSyncReady(time.Second) {
		return nil, fmt.Errorf("sync service still initializing")
	}
	result, err := a.syncService.ConfigureRepo(repoURL, username, token, masterPassword)
	if err == nil {
		a.reloadStoresAfterSync()
		a.emit("sync:completed")
	}
	return result, err
}

// SyncChangePassword re-encrypts synced files with a new master password.
func (a *App) SyncChangePassword(oldPassword, newPassword string) error {
	if a.syncService == nil {
		return fmt.Errorf("sync service not initialized")
	}
	if !a.waitSyncReady(time.Second) {
		return fmt.Errorf("sync service still initializing")
	}
	return a.syncService.ChangePassword(oldPassword, newPassword)
}

// SyncVerifyPassword verifies the given password can decrypt the repo config.
func (a *App) SyncVerifyPassword(password, username, token string) error {
	if a.syncService == nil {
		return fmt.Errorf("sync service not initialized")
	}
	if !a.waitSyncReady(time.Second) {
		return fmt.Errorf("sync service still initializing")
	}
	return a.syncService.VerifySyncPassword(password, username, token)
}

// SyncDeleteRepo removes the sync repository configuration.
func (a *App) SyncDeleteRepo() error {
	if a.syncService == nil {
		return fmt.Errorf("sync service not initialized")
	}
	if !a.waitSyncReady(time.Second) {
		return fmt.Errorf("sync service still initializing")
	}
	return a.syncService.DeleteRepo()
}

func (a *App) LoadAIConfig() (store.AIConfig, error) {
	if a.settingsStore == nil {
		return store.AIConfig{}, fmt.Errorf("settings store not initialized")
	}
	settings, err := a.settingsStore.Load()
	if err != nil {
		return store.AIConfig{}, err
	}
	// Return the active model's config
	for _, m := range settings.AI.Models {
		if m.ID == settings.AI.ActiveModelID {
			return store.AIConfig{
				APIKey:  m.APIKey,
				BaseURL: m.BaseURL,
				Model:   m.Model,
			}, nil
		}
	}
	return store.AIConfig{}, nil
}

// AI Session Store methods

func (a *App) SaveAISessions(data store.AISessionData) error {
	if a.aiSessionStore == nil {
		return fmt.Errorf("AI session store not initialized")
	}
	return a.aiSessionStore.Save(data)
}

func (a *App) LoadAISessions() (store.AISessionData, error) {
	if a.aiSessionStore == nil {
		return store.AISessionData{}, fmt.Errorf("AI session store not initialized")
	}
	return a.aiSessionStore.Load()
}

// SettingsStore methods

func (a *App) SaveSettings(settings store.AppSettings) error {
	if a.settingsStore == nil {
		return fmt.Errorf("settings store not initialized")
	}
	err := a.settingsStore.Save(settings)
	if err == nil {
		a.triggerAutoSync()
	}
	return err
}

func (a *App) LoadSettings() (store.AppSettings, error) {
	if a.settingsStore == nil {
		return store.AppSettings{}, fmt.Errorf("settings store not initialized")
	}
	return a.settingsStore.Load()
}

// QuickCommandsStore methods

func (a *App) SaveQuickCommands(data store.QuickCommandData) error {
	if a.quickCommandsStore == nil {
		return fmt.Errorf("quick commands store not initialized")
	}
	err := a.quickCommandsStore.Save(data)
	if err == nil {
		a.triggerAutoSync()
	}
	return err
}

func (a *App) LoadQuickCommands() (store.QuickCommandData, error) {
	if a.quickCommandsStore == nil {
		return store.QuickCommandData{}, fmt.Errorf("quick commands store not initialized")
	}
	return a.quickCommandsStore.Load()
}

// SkillsStore methods

func (a *App) ListSkills() ([]store.SkillMeta, error) {
	if a.skillsStore == nil {
		return nil, fmt.Errorf("skills store not initialized")
	}
	return a.skillsStore.List()
}

func (a *App) GetSkillBody(name string) (string, error) {
	if a.skillsStore == nil {
		return "", fmt.Errorf("skills store not initialized")
	}
	return a.skillsStore.GetBody(name)
}

func (a *App) GetSkillFile(name, relPath string) (string, error) {
	if a.skillsStore == nil {
		return "", fmt.Errorf("skills store not initialized")
	}
	return a.skillsStore.GetSkillFile(name, relPath)
}

func (a *App) ListSkillFiles(name string) (store.SkillFileList, error) {
	if a.skillsStore == nil {
		return store.SkillFileList{}, fmt.Errorf("skills store not initialized")
	}
	return a.skillsStore.ListSkillFiles(name)
}

func (a *App) SetSkillEnabled(name string, enabled bool) error {
	if a.skillsStore == nil {
		return fmt.Errorf("skills store not initialized")
	}
	return a.skillsStore.SetEnabled(name, enabled)
}

func (a *App) SetSkillLocked(name string, locked bool) error {
	if a.skillsStore == nil {
		return fmt.Errorf("skills store not initialized")
	}
	return a.skillsStore.SetLocked(name, locked)
}

func (a *App) SetSkillSortOrder(name string, order int) error {
	if a.skillsStore == nil {
		return fmt.Errorf("skills store not initialized")
	}
	return a.skillsStore.SetSortOrder(name, order)
}

func (a *App) DeleteSkill(name string) error {
	if a.skillsStore == nil {
		return fmt.Errorf("skills store not initialized")
	}
	return a.skillsStore.Delete(name)
}

func (a *App) ImportSkillFromDir(srcDir string) (string, error) {
	if a.skillsStore == nil {
		return "", fmt.Errorf("skills store not initialized")
	}
	return a.skillsStore.ImportFromDir(srcDir)
}

func (a *App) ImportSkillFromZip(zipPath string) (string, error) {
	if a.skillsStore == nil {
		return "", fmt.Errorf("skills store not initialized")
	}
	return a.skillsStore.ImportFromZip(zipPath)
}

func (a *App) CreateSkill(name, description, body string) error {
	if a.skillsStore == nil {
		return fmt.Errorf("skills store not initialized")
	}
	return a.skillsStore.CreateSkill(name, description, body)
}

func (a *App) SaveSkill(name, description, body string) error {
	if a.skillsStore == nil {
		return fmt.Errorf("skills store not initialized")
	}
	return a.skillsStore.SaveSkill(name, description, body)
}

// CommandsStore methods

func (a *App) ListCommands() ([]store.CommandMeta, error) {
	if a.commandsStore == nil {
		return nil, fmt.Errorf("commands store not initialized")
	}
	return a.commandsStore.List()
}

func (a *App) GetCommandBody(name string) (string, error) {
	if a.commandsStore == nil {
		return "", fmt.Errorf("commands store not initialized")
	}
	return a.commandsStore.GetBody(name)
}

func (a *App) SetCommandEnabled(name string, enabled bool) error {
	if a.commandsStore == nil {
		return fmt.Errorf("commands store not initialized")
	}
	return a.commandsStore.SetEnabled(name, enabled)
}

func (a *App) SetCommandLocked(name string, locked bool) error {
	if a.commandsStore == nil {
		return fmt.Errorf("commands store not initialized")
	}
	return a.commandsStore.SetLocked(name, locked)
}

func (a *App) SetCommandSortOrder(name string, order int) error {
	if a.commandsStore == nil {
		return fmt.Errorf("commands store not initialized")
	}
	return a.commandsStore.SetSortOrder(name, order)
}

func (a *App) DeleteCommand(name string) error {
	if a.commandsStore == nil {
		return fmt.Errorf("commands store not initialized")
	}
	return a.commandsStore.Delete(name)
}

func (a *App) CreateCommand(name, description, argumentHint, body string) error {
	if a.commandsStore == nil {
		return fmt.Errorf("commands store not initialized")
	}
	return a.commandsStore.CreateCommand(name, description, argumentHint, body)
}

func (a *App) SaveCommand(name, description, argumentHint, body string) error {
	if a.commandsStore == nil {
		return fmt.Errorf("commands store not initialized")
	}
	return a.commandsStore.SaveCommand(name, description, argumentHint, body)
}

func (a *App) OpenFileDialog() (string, error) {
	return a.app.Dialog.OpenFile().SetTitle("Select File").PromptForSingleSelection()
}

// OpenPrivateKeyFile opens the private-key picker, reads the selected file's
// text and returns it for direct use with the "keyText" auth type (#720). The
// content is validated before returning; a passphrase-protected key is accepted
// (the user supplies its passphrase separately) but content that doesn't look
// like a PEM private key is rejected with an immediate error.
func (a *App) OpenPrivateKeyFile() (string, error) {
	path, err := a.app.Dialog.OpenFile().SetTitle("Select Private Key").PromptForSingleSelection()
	if err != nil {
		return "", err
	}
	if path == "" {
		// Picker cancelled — nothing to import.
		return "", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read private key: %w", err)
	}
	content := string(data)
	if err := validatePrivateKeyText(content); err != nil {
		return "", err
	}
	return content, nil
}

// validatePrivateKeyText parses content as a private key when possible. An
// OpenSSH/RSA/EC/DSA PEM parsed without a passphrase is valid. Content that
// clearly isn't a private key is rejected up front; content that looks like a
// PEM private key but requires a passphrase to crack is accepted, since the key
// passphrase is entered separately on the form.
func validatePrivateKeyText(content string) error {
	data := []byte(content)
	if _, err := ssh.ParsePrivateKey(data); err == nil {
		return nil
	}
	// Parse can only succeed for an unencrypted key; a passphrase-protected key
	// aborts at the decryption step while still being a valid PEM private key,
	// so accept anything that carries the private-key envelope and reject
	// content that never looked like a key to begin with.
	if !looksLikePrivateKeyPEM(data) {
		return fmt.Errorf("所选文件不是有效的私钥（未识别到 PEM 私钥头）")
	}
	return nil
}

// looksLikePrivateKeyPEM reports whether data begins with a PEM private-key
// envelope (-----BEGIN ... PRIVATE KEY-----), whitespace-insensitive.
func looksLikePrivateKeyPEM(data []byte) bool {
	head := strings.ToUpper(string(bytes.TrimSpace(data)))
	const prefix = "-----BEGIN "
	if !strings.HasPrefix(head, prefix) || !strings.Contains(head, "PRIVATE KEY-----") {
		return false
	}
	return true
}

// OpenFileDialogFiltered is like OpenFileDialog but restricts the picker to
// a single extension filter (e.g. for importing a specific file format).
func (a *App) OpenFileDialogFiltered(title, filterDisplayName, filterPattern string) (string, error) {
	return a.app.Dialog.OpenFileWithOptions(&application.OpenFileDialogOptions{
		Title: title,
		Filters: []application.FileFilter{
			{DisplayName: filterDisplayName, Pattern: filterPattern},
		},
	}).PromptForSingleSelection()
}

func (a *App) OpenMultipleFilesDialog() ([]string, error) {
	return a.app.Dialog.OpenFile().SetTitle("Select Files").PromptForMultipleSelection()
}

func (a *App) OpenDirectoryDialog() (string, error) {
	return a.app.Dialog.OpenFile().
		SetTitle("Select Directory").
		CanChooseDirectories(true).
		CanChooseFiles(false).
		PromptForSingleSelection()
}

func (a *App) SaveFileDialog(defaultName string) (string, error) {
	return a.app.Dialog.SaveFileWithOptions(&application.SaveFileDialogOptions{
		Title:    "Save File",
		Filename: defaultName,
	}).PromptForSingleSelection()
}

// SaveFileDialogFiltered is like SaveFileDialog but restricts the picker to
// a single extension filter (e.g. for exporting a specific file format).
func (a *App) SaveFileDialogFiltered(title, defaultName, filterDisplayName, filterPattern string) (string, error) {
	return a.app.Dialog.SaveFileWithOptions(&application.SaveFileDialogOptions{
		Title:    title,
		Filename: defaultName,
		Filters: []application.FileFilter{
			{DisplayName: filterDisplayName, Pattern: filterPattern},
		},
	}).PromptForSingleSelection()
}

func (a *App) GetDesktopPath() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(homeDir, "Desktop"), nil
}

func (a *App) GetPlatform() string {
	return goruntime.GOOS
}

func (a *App) GetAllFonts() ([]platform.FontInfo, error) {
	return platform.GetAllFonts()
}

func (a *App) OnConnectionsChanged(callback func(session.ConnectionStoreData)) {
	a.app.Event.On("store:connections:changed", func(e *application.CustomEvent) {
		if data, ok := e.Data.(session.ConnectionStoreData); ok {
			callback(data)
		}
	})
}

// SessionManager methods

func (a *App) CreateSession(sessionType string, config session.ConnectionConfig) (*session.SessionInfo, error) {
	if a.sessionManager == nil {
		return nil, fmt.Errorf("session manager not initialized")
	}
	log.Writef("[CreateSession] type=%s, dbType=%s, host=%s, port=%d, user=%s, dbName=%s, name=%s",
		sessionType, config.DBType, config.Host, config.Port, config.User, config.DBName, config.Name)
	// Defensive credential fallback: the frontend may hold a connection
	// snapshot taken before passwords were filled (or a stale copy from an
	// older session). If the password is stored in the OS keychain, resolve
	// it synchronously so the session never prompts for a password it
	// already has. No-op when Password is already set, no store is wired,
	// the keychain has no entry, or the config carries no connection ID.
	if config.Password == "" && config.ID != "" && a.connectionStore != nil {
		if pw, err := a.connectionStore.EnsurePassword(config.ID); err == nil && pw != "" {
			config.Password = pw
		}
	}
	// Resolve an identity reference into a concrete password/key config
	// before the session manager dials.
	if config.AuthType == "identity" {
		mc, err := a.materializeIdentity(config)
		if err != nil {
			return nil, err
		}
		config = mc
	}
	// Resolve a proxy reference into a concrete proxy config so SSH-family
	// first-hop dials route through it.
	mc, err := a.materializeProxy(config)
	if err != nil {
		return nil, err
	}
	config = mc
	s, err := a.sessionManager.Create(sessionType, config)
	if err != nil {
		log.Writef("[CreateSession] manager.Create failed: %v", err)
		return nil, err
	}
	log.Writef("[CreateSession] session created, id=%s", s.ID())
	// Record the LogOnConnect preference synchronously so the frontend's
	// subsequent RegisterSessionForPanel can consult it — the actual
	// Connect() goroutine may not have run yet at Register time.
	if setter, ok := s.(interface{ SetLogOnConnect(bool) }); ok {
		setter.SetLogOnConnect(config.LogOnConnect)
	}
	// Stash the initial terminal size the frontend measured BEFORE
	// calling CreateSession. Connect() (called async below) reads it via
	// getInitialSize() and uses it for PTY sizing — so the remote shell
	// and Claude Code see the actual xterm cols from the first byte, not
	// the default 80x24 that would otherwise be in use until the late
	// SessionResize arrives.
	if config.InitialCols > 0 && config.InitialRows > 0 {
		if sz, ok := s.(interface{ SetPendingSize(int, int) }); ok {
			sz.SetPendingSize(config.InitialCols, config.InitialRows)
		}
	}
	// Apply terminal character encoding. No-op for utf-8/empty.
	if ssh, ok := s.(*session.SSHSession); ok {
		ssh.SetEncoding(config.Encoding)
	}
	if telnet, ok := s.(*session.TelnetSession); ok {
		telnet.SetEncoding(config.Encoding)
	}
	if serial, ok := s.(*session.SerialSession); ok {
		serial.SetEncoding(config.Encoding)
	}
	if mosh, ok := s.(*session.MoshSession); ok {
		mosh.SetEncoding(config.Encoding)
	}
	if local, ok := s.(*session.LocalSession); ok {
		local.SetEncoding(config.Encoding)
	}

	// Apply serial config; connection itself is handled by the async goroutine
	// below (same pattern as SSH/Local). Calling serialSess.Connect here as
	// well would open the port a second time in the goroutine and immediately
	// fail with "Serial port busy" once the first handle is still live.
	if serialSess, ok := s.(*session.SerialSession); ok {
		var sb serial.StopBits
		switch config.SerialStopBits {
		case 1.5:
			sb = serial.OnePointFiveStopBits
		case 2:
			sb = serial.TwoStopBits
		default:
			sb = serial.OneStopBit
		}

		parityMap := map[string]serial.Parity{
			"none":  serial.NoParity,
			"odd":   serial.OddParity,
			"even":  serial.EvenParity,
			"mark":  serial.MarkParity,
			"space": serial.SpaceParity,
		}
		par, ok := parityMap[strings.ToLower(config.SerialParity)]
		if !ok {
			par = serial.NoParity
		}

		dataBits := config.SerialDataBits
		if dataBits == 0 {
			dataBits = 8
		}

		serialSess.SetSerialConfig(session.SerialConfig{
			PortName: config.SerialPort,
			BaudRate: config.SerialBaudRate,
			DataBits: dataBits,
			StopBits: sb,
			Parity:   par,
		})
	}

	// SFTP concurrency limit
	if sessionType == "sftp" {
		if sftp, ok := s.(*session.SFTPSession); ok {
			n := config.SftpMaxConcurrency
			if n <= 0 {
				n = 5
			}
			sftp.SetMaxConcurrency(n)
		}
	}

	// Set parent HWND for RDP sessions
	if rdp, ok := s.(*session.RDPSession); ok {
		rdp.SetParentHwnd(a.mainHwnd)
		// Notify the frontend when the user exits native full screen so it can
		// resume position sync.
		rdp.SetOnFullScreenExit(func() {
			a.emit("rdp:fullscreen-exit", s.ID())
		})
	}

	s.SetOnDataCallback(func(data []byte) {
		a.emit("session:data", map[string]interface{}{
			"id":   s.ID(),
			"data": string(data),
		})
	})

	s.SetOnBinaryCallback(func(data []byte) {
		a.emit("session:binary", map[string]interface{}{
			"id":   s.ID(),
			"data": base64.StdEncoding.EncodeToString(data),
		})
	})

	s.SetOnStatusChangeCallback(func(status session.SessionStatus) {
		payload := map[string]interface{}{
			"id":     s.ID(),
			"status": status,
		}
		// For RDP sessions, include client area screen coordinates so the
		// frontend can position the overlay window without fragile browser APIs.
		if status == session.StatusConnected {
			if rdp, ok := s.(*session.RDPSession); ok {
				cx, cy, cw, ch := rdp.ClientAreaScreenRect()
				payload["clientX"] = cx
				payload["clientY"] = cy
				payload["clientW"] = cw
				payload["clientH"] = ch
				// The RDP ActiveX (and any credential/security dialog it owns) can
				// push uniTerm behind other windows during connect. Raise the main
				// window once the session is up so it stays visible above unrelated
				// windows. No-op on non-Windows; harmless if already in front.
				a.bringMainWindowToFront()
			}
			// Attach proxyAddr for VNC and SPICE sessions
			if vnc, ok := s.(*session.VNCSession); ok {
				payload["proxyAddr"] = vnc.ProxyAddr()
			}
			if spice, ok := s.(*session.SPICESession); ok {
				payload["proxyAddr"] = spice.ProxyAddr()
			}
			// Attach remoteOS for SSH sessions so the AI agent can distinguish
			// Windows OpenSSH (cmd/PowerShell) from Unix-like shells. Empty for
			// non-Windows or undetermined servers.
			if sshSess, ok := s.(*session.SSHSession); ok {
				if remoteOS := sshSess.RemoteOS(); remoteOS != "" {
					payload["remoteOS"] = remoteOS
				}
			}
		}

		a.emit("session:status", payload)
	})

	// Database, Redis, and MongoDB sessions connect synchronously so
	// errors are returned to the frontend try/catch.
	if sessionType == "database" || sessionType == "redis" || sessionType == "mongodb" {
		// Set up jump-host tunnel before connecting, so database/redis/mongo
		// sessions ride the tunnel just like other session types.
		if err := a.setupJumpHostTunnel(s.ID(), sessionType, &config); err != nil {
			_ = a.sessionManager.Close(s.ID())
			return nil, err
		}
		log.Writef("[CreateSession] connecting %s session synchronously...", sessionType)
		if err := s.Connect(config); err != nil {
			log.Writef("[CreateSession] %s connect failed: %v", sessionType, err)
			// Clean up any tunnel that was set up for this session.
			if a.tunnelService != nil {
				a.tunnelService.Stop(s.ID())
			}
			_ = a.sessionManager.Close(s.ID())
			return nil, fmt.Errorf("%s connect failed: %w", sessionType, err)
		}
		log.Writef("[CreateSession] %s session connected successfully, id=%s", sessionType, s.ID())
	} else if sessionType == "x11-desktop" {
		// x11-desktop uses its own X11DesktopConnect entry point; the
		// generic Connect goroutine must never call s.Connect() here.
	} else if sessionType == "ssh" || sessionType == "local" || sessionType == "telnet" || sessionType == "mosh" || sessionType == "serial" {
		// Terminal session types that mount xterm: defer Connect until
		// SessionStart is called after the frontend measures real cols/rows.
		// Without this gap Claude Code draws tables at the 80x24 default
		// before SessionResize propagates the real width, and the borders
		// drift across output batches.
	} else if sessionType == "vnc" || sessionType == "spice" {
		// VNC and SPICE start a local WebSocket↔TCP proxy synchronously
		// so the proxyAddr is available when CreateSession returns.
		// Avoids a race between the goroutine's session:status emit and
		// the frontend's CreateSession IPC response.
		if err := a.setupJumpHostTunnel(s.ID(), sessionType, &config); err != nil {
			_ = a.sessionManager.Close(s.ID())
			return nil, err
		}
		if err := s.Connect(config); err != nil {
			if a.tunnelService != nil {
				a.tunnelService.Stop(s.ID())
			}
			_ = a.sessionManager.Close(s.ID())
			return nil, fmt.Errorf("%s connect failed: %w", sessionType, err)
		}
	} else {
		// Non-terminal sessions (sftp, monitor, ftp, smb, webdav, s3, rdp)
		// connect immediately.
		a.launchConnectGoroutine(s, sessionType, config)
	}

	info := &session.SessionInfo{
		ID:     s.ID(),
		Type:   s.Type(),
		Title:  s.Title(),
		Status: s.Status(),
	}
	// VNC and SPICE expose a local WebSocket↔TCP proxy; return its address
	// in the CreateSession result so the frontend can mount the RFB/SPICE
	// client without racing the session:status event (whose 'connected'
	// emission happens inside s.Connect, before the frontend sets the
	// session id / stores the proxy addr — see SESSION regression).
	if vnc, ok := s.(*session.VNCSession); ok {
		info.ProxyAddr = vnc.ProxyAddr()
	}
	if spice, ok := s.(*session.SPICESession); ok {
		info.ProxyAddr = spice.ProxyAddr()
	}
	return info, nil
}

// launchConnectGoroutine starts the async Connect path. CreateSession
// skips it for terminal session types (ssh, local, telnet, mosh, serial,
// x11-desktop) — those instead drive the connection via SessionStart after
// the frontend measures cols/rows.
func (a *App) launchConnectGoroutine(s session.Session, sessionType string, config session.ConnectionConfig) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Writef("session %s connect panic: %v\n%s", s.ID(), r, string(debug.Stack()))
			}
		}()

		// ── SSH Tunnel (jump host) ──────────────────────────────
		// Set up the local-port-forward through the jump host BEFORE
		// any dial / pre-check, then point config at the local listener
		// so the subsequent s.Connect() (and the RDP pre-check below)
		// ride the tunnel. This block lives here — not in CreateSession —
		// because terminal session types defer their Connect until
		// SessionStart, by which point any config rewrite from
		// CreateSession would be discarded along with the local config
		// copy.
		if err := a.setupJumpHostTunnel(s.ID(), sessionType, &config); err != nil {
			a.failSessionConnect(s, err)
			return
		}
		// ── End SSH Tunnel ──────────────────────────────────────

		// RDP TCP pre-check: fail fast before creating the ActiveX window.
		if sessionType == "rdp" {
			port := config.Port
			if port <= 0 {
				port = 3389
			}
			addr := net.JoinHostPort(config.Host, strconv.Itoa(port))
			tcpConn, tcpErr := net.DialTimeout("tcp", addr, 5*time.Second)
			if tcpErr != nil {
				log.Writef("[launchConnect] RDP TCP pre-check to %s failed: %v", addr, tcpErr)
				a.failSessionConnect(s, fmt.Errorf("Cannot reach %s: %v", addr, tcpErr))
				return
			}
			tcpConn.Close()
			log.Writef("[launchConnect] RDP TCP pre-check to %s succeeded", addr)
		}

		if err := s.Connect(config); err != nil {
			a.failSessionConnect(s, err)
		}
	}()
}

// failSessionConnect is the shared error path inside launchConnectGoroutine
// for tunnel setup, RDP pre-check, and s.Connect failures. It surfaces the
// error to both terminal (session:data) and non-terminal (session:status)
// listeners and tears down any half-started tunnel + the session itself.
func (a *App) failSessionConnect(s session.Session, err error) {
	log.Writef("session %s connect error: %v", s.ID(), err)
	if a.tunnelService != nil {
		a.tunnelService.Stop(s.ID())
	}
	if a.ctx != nil {
		a.emit("session:status", map[string]interface{}{
			"id":           s.ID(),
			"status":       "error",
			"errorMessage": err.Error(),
		})
		a.emit("session:data", map[string]interface{}{
			"id":   s.ID(),
			"data": fmt.Sprintf("\r\n\x1b[31m[Connection failed: %v]\x1b[0m\r\nPress Enter to retry...\r\n", err),
		})
	}
	if a.sessionManager != nil {
		_ = a.sessionManager.Close(s.ID())
	}
}

// setupJumpHostTunnel establishes an SSH jump-host tunnel for the given
// session config. When config.TunnelSSHConnID is set, it opens a local
// port-forward through the referenced SSH connection and rewrites
// config.Host/Port to point at the local listener so the subsequent
// Connect call rides the tunnel.
// Returns nil when no tunnel is configured or when setup succeeds.
func (a *App) setupJumpHostTunnel(sessionID string, sessionType string, config *session.ConnectionConfig) error {
	if config.TunnelSSHConnID == "" {
		return nil
	}
	if a.tunnelService == nil || a.connectionStore == nil {
		return fmt.Errorf("tunnel prerequisites not initialized")
	}
	data, err := a.connectionStore.Load()
	if err != nil {
		return fmt.Errorf("load connections for tunnel: %w", err)
	}
	var tunnelSSHConfig *session.ConnectionConfig
	for _, c := range data.Connections {
		if c.ID == config.TunnelSSHConnID {
			tunnelSSHConfig = &c
			break
		}
	}
	if tunnelSSHConfig == nil {
		return fmt.Errorf("tunnel SSH connection not found: %s", config.TunnelSSHConnID)
	}

	// Resolve an identity reference into a concrete password/key config
	// before handing the jump host to the tunnel service. Identity is
	// authoritative from the 密钥库(identity store); inline credentials
	// never override it.
	wasIdentity := tunnelSSHConfig.AuthType == "identity"
	if tunnelSSHConfig.AuthType == "identity" {
		m, err := a.materializeIdentity(*tunnelSSHConfig)
		if err != nil {
			return err
		}
		tunnelSSHConfig = &m
	}

	// Defensive credential fallback for the jump host: the freshly loaded
	// config already has passwords filled synchronously (populatePasswords),
	// but resolve from the keychain anyway if it is somehow still empty.
	if tunnelSSHConfig.Password == "" && tunnelSSHConfig.ID != "" {
		if pw, err := a.connectionStore.EnsurePassword(tunnelSSHConfig.ID); err == nil && pw != "" {
			tunnelSSHConfig.Password = pw
		}
	}

	// Inline tunnel credentials (ephemeral prompt "connect" without saving)
	// only fill gaps — they never override already-resolved values, and
	// never touch an identity-resolved config.
	if !wasIdentity {
		if config.TunnelSSHUser != "" && tunnelSSHConfig.User == "" {
			tunnelSSHConfig.User = config.TunnelSSHUser
		}
		if config.TunnelSSHPassword != "" && tunnelSSHConfig.Password == "" {
			tunnelSSHConfig.Password = config.TunnelSSHPassword
		}
	}

	// VNC/SPICE use libvirt display numbers (port < 100 → 5900+N).
	targetPort := config.Port
	if sessionType == "vnc" || sessionType == "spice" {
		if targetPort <= 0 {
			targetPort = 5900
		} else if targetPort < 100 {
			targetPort += 5900
		}
	}
	localPort, err := a.tunnelService.Start(sessionID, *tunnelSSHConfig, config.Host, targetPort, config.Proxy)
	if err != nil {
		return fmt.Errorf("tunnel start: %w", err)
	}
	log.Writef("[tunnel] established for session=%s via ssh=%s, localPort=%d",
		sessionID, config.TunnelSSHConnID, localPort)
	config.Host = "127.0.0.1"
	config.Port = localPort
	config.Proxy = nil // proxy was consumed by the jump-host dial; local dial is direct
	return nil
}

// SessionStart triggers the actual Connect() for terminal sessions
// (ssh, local, telnet, mosh, serial) whose Connect was deferred by
// CreateSession. The frontend calls this AFTER mounting the xterm
// terminal and measuring the real cols/rows, so the PTY is created at
// the correct dimensions from the first byte — no 80x24 default phase
// where Claude Code can draw tables at the wrong column count.
func (a *App) SessionStart(sessionID string, config session.ConnectionConfig) error {
	if a.sessionManager == nil {
		return fmt.Errorf("session manager not initialized")
	}
	s, ok := a.sessionManager.Get(sessionID)
	if !ok {
		return fmt.Errorf("session %s not found", sessionID)
	}
	// Re-stash the latest measured size in case the deferred config
	// carries the real cols/rows the frontend discovered after mount.
	if config.InitialCols > 0 && config.InitialRows > 0 {
		s.SetPendingSize(config.InitialCols, config.InitialRows)
	}
	// Terminal session types defer Connect() until SessionStart, and the
	// frontend passes a fresh config here. If that fresh config still has
	// an empty password (e.g. the Pinia store holds a snapshot from before
	// keychain passwords were filled), resolve it from the OS keychain now
	// so the user is not prompted for a password that is already stored.
	if config.Password == "" && config.ID != "" && a.connectionStore != nil {
		if pw, err := a.connectionStore.EnsurePassword(config.ID); err == nil && pw != "" {
			config.Password = pw
		}
	}
	// Resolve an identity reference into a concrete password/key config
	// before launching the connect goroutine.
	if config.AuthType == "identity" {
		mc, err := a.materializeIdentity(config)
		if err != nil {
			return err
		}
		config = mc
	}
	// Resolve a proxy reference into a concrete proxy config so SSH-family
	// first-hop dials route through it.
	mc, err := a.materializeProxy(config)
	if err != nil {
		return err
	}
	config = mc
	a.launchConnectGoroutine(s, config.Type, config)
	return nil
}

func (a *App) CloseSession(sessionID string) error {
	if a.sessionManager == nil {
		return fmt.Errorf("session manager not initialized")
	}
	if a.tunnelService != nil {
		a.tunnelService.Stop(sessionID)
	}
	return a.sessionManager.Close(sessionID)
}

func (a *App) ListSessions() []session.SessionInfo {
	if a.sessionManager == nil {
		return []session.SessionInfo{}
	}
	return a.sessionManager.List()
}

func (a *App) SessionWrite(sessionID string, data string) error {
	if a.sessionManager == nil {
		return fmt.Errorf("session manager not initialized")
	}
	s, ok := a.sessionManager.Get(sessionID)
	if !ok {
		return fmt.Errorf("session not found: %s", sessionID)
	}
	return s.Write([]byte(data))
}

func (a *App) SessionResize(sessionID string, cols, rows int) error {
	if a.sessionManager == nil {
		return fmt.Errorf("session manager not initialized")
	}
	s, ok := a.sessionManager.Get(sessionID)
	if !ok {
		return fmt.Errorf("session not found: %s", sessionID)
	}
	return s.Resize(cols, rows)
}

func (a *App) SessionStartZmodem(sessionID string) error {
	if a.sessionManager == nil {
		return fmt.Errorf("session manager not initialized")
	}
	s, ok := a.sessionManager.Get(sessionID)
	if !ok {
		return fmt.Errorf("session not found: %s", sessionID)
	}
	s.SetZmodemMode(true)
	return nil
}

func (a *App) SessionEndZmodem(sessionID string) error {
	if a.sessionManager == nil {
		return fmt.Errorf("session manager not initialized")
	}
	s, ok := a.sessionManager.Get(sessionID)
	if !ok {
		return fmt.Errorf("session not found: %s", sessionID)
	}
	s.SetZmodemMode(false)
	return nil
}

func (a *App) SessionWriteBinary(sessionID string, base64Data string) error {
	if a.sessionManager == nil {
		return fmt.Errorf("session manager not initialized")
	}
	s, ok := a.sessionManager.Get(sessionID)
	if !ok {
		return fmt.Errorf("session not found: %s", sessionID)
	}
	data, err := base64.StdEncoding.DecodeString(base64Data)
	if err != nil {
		return fmt.Errorf("decode base64: %w", err)
	}
	return s.Write(data)
}

func (a *App) ReadFileBase64(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read file: %w", err)
	}
	return base64.StdEncoding.EncodeToString(data), nil
}

func (a *App) FileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("stat file: %w", err)
	}
	if info.IsDir() {
		return 0, fmt.Errorf("path is a directory: %s", path)
	}
	return info.Size(), nil
}

func (a *App) ReadFileChunkBase64(path string, offset int64, length int64) (string, error) {
	if offset < 0 {
		return "", fmt.Errorf("offset must be non-negative")
	}
	if length <= 0 {
		return "", fmt.Errorf("length must be positive")
	}

	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("stat file: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("path is a directory: %s", path)
	}
	if offset >= info.Size() {
		return "", nil
	}
	if remaining := info.Size() - offset; length > remaining {
		length = remaining
	}

	buf := make([]byte, length)
	n, err := f.ReadAt(buf, offset)
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("read file chunk: %w", err)
	}
	return base64.StdEncoding.EncodeToString(buf[:n]), nil
}

func (a *App) WriteFileBase64(path string, base64Data string) error {
	data, err := base64.StdEncoding.DecodeString(base64Data)
	if err != nil {
		return fmt.Errorf("decode base64: %w", err)
	}
	return os.WriteFile(path, data, 0644)
}

func (a *App) AppendFileBase64(path string, base64Data string, offset int64) error {
	data, err := base64.StdEncoding.DecodeString(base64Data)
	if err != nil {
		return fmt.Errorf("decode base64: %w", err)
	}

	flag := os.O_CREATE | os.O_WRONLY
	if offset == 0 {
		flag |= os.O_TRUNC
	} else {
		flag |= os.O_APPEND
	}

	f, err := os.OpenFile(path, flag, 0644)
	if err != nil {
		return fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat file: %w", err)
	}
	if info.Size() != offset {
		return fmt.Errorf("append offset mismatch: expected %d, got %d", offset, info.Size())
	}

	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("write file: %w", err)
	}
	return nil
}

func (a *App) RDPSetPosition(sessionID string, x, y, w, h int) error {
	if a.sessionManager == nil {
		return fmt.Errorf("session manager not initialized")
	}
	s, ok := a.sessionManager.Get(sessionID)
	if !ok {
		return fmt.Errorf("session not found: %s", sessionID)
	}
	rdp, ok := s.(*session.RDPSession)
	if !ok {
		return fmt.Errorf("session is not RDP")
	}
	rdp.SetPosition(x, y, w, h)
	return nil
}

func (a *App) RDPShow(sessionID string) error {
	if a.sessionManager == nil {
		return fmt.Errorf("session manager not initialized")
	}
	s, ok := a.sessionManager.Get(sessionID)
	if !ok {
		return fmt.Errorf("session not found: %s", sessionID)
	}
	rdp, ok := s.(*session.RDPSession)
	if !ok {
		return fmt.Errorf("session is not RDP")
	}
	rdp.Show()
	return nil
}

// RDPSetFullScreen toggles the ActiveX control's built-in full-screen mode.
func (a *App) RDPSetFullScreen(sessionID string, full bool) error {
	if a.sessionManager == nil {
		return fmt.Errorf("session manager not initialized")
	}
	s, ok := a.sessionManager.Get(sessionID)
	if !ok {
		return fmt.Errorf("session not found: %s", sessionID)
	}
	rdp, ok := s.(*session.RDPSession)
	if !ok {
		return fmt.Errorf("session is not RDP")
	}
	rdp.SetFullScreen(full)
	return nil
}

func (a *App) RDPHide(sessionID string) error {
	if a.sessionManager == nil {
		return fmt.Errorf("session manager not initialized")
	}
	s, ok := a.sessionManager.Get(sessionID)
	if !ok {
		return fmt.Errorf("session not found: %s", sessionID)
	}
	rdp, ok := s.(*session.RDPSession)
	if !ok {
		return fmt.Errorf("session is not RDP")
	}
	rdp.Hide()
	return nil
}

// RDPSnapshot captures the RDP session's current frame as a base64-encoded PNG.
// The frontend shows it as a frozen background in .rdp-area while the RDP window
// is hidden under an overlay (menu/dialog), so the area shows a snapshot instead
// of a black placeholder.
func (a *App) RDPSnapshot(sessionID string) (string, error) {
	if a.sessionManager == nil {
		return "", fmt.Errorf("session manager not initialized")
	}
	s, ok := a.sessionManager.Get(sessionID)
	if !ok {
		return "", fmt.Errorf("session not found: %s", sessionID)
	}
	rdp, ok := s.(*session.RDPSession)
	if !ok {
		return "", fmt.Errorf("session is not RDP")
	}
	return rdp.Snapshot()
}

func (a *App) RDPInvalidate(sessionID string) error {
	if a.sessionManager == nil {
		return fmt.Errorf("session manager not initialized")
	}
	s, ok := a.sessionManager.Get(sessionID)
	if !ok {
		return fmt.Errorf("session not found: %s", sessionID)
	}
	rdp, ok := s.(*session.RDPSession)
	if !ok {
		return fmt.Errorf("session is not RDP")
	}
	rdp.Invalidate()
	return nil
}

// MonitorSession methods

func (a *App) getMonitorSession(sessionID string) (*session.MonitorSession, error) {
	if a.sessionManager == nil {
		return nil, fmt.Errorf("session manager not initialized")
	}
	s, ok := a.sessionManager.Get(sessionID)
	if !ok {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}
	ms, ok := s.(*session.MonitorSession)
	if !ok {
		return nil, fmt.Errorf("session is not a monitor session: %s", sessionID)
	}
	return ms, nil
}

func (a *App) SetMonitorActiveTab(sessionID string, tab string) error {
	ms, err := a.getMonitorSession(sessionID)
	if err != nil {
		return err
	}
	ms.SetActiveTab(tab)
	return nil
}

func (a *App) SetMonitorPaused(sessionID string, paused bool) error {
	ms, err := a.getMonitorSession(sessionID)
	if err != nil {
		return err
	}
	ms.SetPaused(paused)
	return nil
}

func (a *App) GetProcessDetail(sessionID string, pid int) (map[string]interface{}, error) {
	ms, err := a.getMonitorSession(sessionID)
	if err != nil {
		return nil, err
	}
	return ms.GetProcessDetail(pid)
}

func (a *App) KillProcess(sessionID string, pid int, signal string) error {
	ms, err := a.getMonitorSession(sessionID)
	if err != nil {
		return err
	}
	return ms.KillProcess(pid, signal)
}

func (a *App) GetPorts(sessionID string) ([]session.PortInfo, error) {
	ms, err := a.getMonitorSession(sessionID)
	if err != nil {
		return nil, err
	}
	return ms.GetPorts()
}

func (a *App) GetDisks(sessionID string) ([]session.DiskInfo, error) {
	ms, err := a.getMonitorSession(sessionID)
	if err != nil {
		return nil, err
	}
	return ms.GetDisks()
}

func (a *App) GetNetworkCards(sessionID string) ([]session.NetCardInfo, error) {
	ms, err := a.getMonitorSession(sessionID)
	if err != nil {
		return nil, err
	}
	return ms.GetNetworkCards()
}

type AppInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

func (a *App) GetAppInfo() AppInfo {
	return AppInfo{
		Name:    "Carrear's Terminal",
		Version: Version,
	}
}

// RelaunchApp spawns a fresh instance, then quits the current one so settings
// that are fixed at startup (e.g. the window title bar) can take effect. The
// new process is started first; a delay lets it finish spawning and raise its
// own window to the foreground (see bringMainWindowToFront) before this instance
// exits — while this process is still the foreground process, which is what
// grants the new one set-foreground permission on Windows.
func (a *App) RelaunchApp() {
	if err := a.relaunchProcess(); err != nil {
		log.Writef("relaunch failed: %v", err)
	}
	go func() {
		time.Sleep(800 * time.Millisecond)
		a.app.Quit()
	}()
}

func (a *App) CheckForUpdate(source string) (*update.UpdateInfo, error) {
	return update.Check(Version, source)
}

func (a *App) SaveTerminalHistory(entries []store.HistoryEntry) error {
	if a.terminalHistoryStore == nil {
		return fmt.Errorf("terminal history store not initialized")
	}
	return a.terminalHistoryStore.Save(entries)
}

func (a *App) LoadTerminalHistory() ([]store.HistoryEntry, error) {
	if a.terminalHistoryStore == nil {
		return []store.HistoryEntry{}, fmt.Errorf("terminal history store not initialized")
	}
	return a.terminalHistoryStore.Load()
}

func (a *App) DeleteTerminalHistoryEntry(ids []string) error {
	if a.terminalHistoryStore == nil {
		return fmt.Errorf("terminal history store not initialized")
	}
	return a.terminalHistoryStore.DeleteByIDs(ids)
}

// RecentStore methods

func (a *App) RecordRecentConnection(connId string) {
	if a.recentStore == nil {
		return
	}
	a.recentStore.Record(connId)
}

func (a *App) GetRecentConnections() []string {
	if a.recentStore == nil {
		return []string{}
	}
	return a.recentStore.GetAll()
}

// ChatCompletion streams the Anthropic API response via SSE, emitting Wails
// events for each token while collecting the full message. It returns the
// complete message JSON when the stream ends (backward-compatible).
func (a *App) ChatCompletion(apiKey, baseURL, model string, requestJSON string, protocol string, userAgent string) (string, error) {
	// Parse the incoming request body (always Anthropic format from frontend)
	var reqBody map[string]interface{}
	if err := json.Unmarshal([]byte(requestJSON), &reqBody); err != nil {
		return "", fmt.Errorf("invalid request JSON: %w", err)
	}

	if userAgent == "" {
		userAgent = "uniTerm"
	}

	switch protocol {
	case "openai":
		return a.chatCompletionOpenAI(apiKey, baseURL, model, reqBody, userAgent)
	case "responses":
		return a.chatCompletionResponses(apiKey, baseURL, model, reqBody, userAgent)
	}
	return a.chatCompletionAnthropic(apiKey, baseURL, model, reqBody, userAgent)
}

// F-306: typed SSE envelope for Anthropic Messages events. Variant
// fields stay as json.RawMessage so we only decode the few fields the
// handler actually reads per event type.
type anthropicStreamEvent struct {
	Type         string          `json:"type"`
	Index        int             `json:"index"`
	ContentBlock json.RawMessage `json:"content_block"`
	Delta        json.RawMessage `json:"delta"`
	Message      json.RawMessage `json:"message"`
	Usage        json.RawMessage `json:"usage"`
	Error        json.RawMessage `json:"error"`
}

type anthropicDelta struct {
	Type        string `json:"type"`
	Text        string `json:"text"`
	PartialJSON string `json:"partial_json"`
}

type anthropicMessageRole struct {
	Role string `json:"role"`
}

type anthropicStopDelta struct {
	StopReason string `json:"stop_reason"`
}

// F-320: typed payloads for the ai:* Wails events. Replacing the
// per-token `map[string]interface{}` literal with a fixed struct saves
// the alloc per event; the json.Marshal on the Wails side now writes
// the same JSON shape (lowercase keys) so the frontend contract is
// unchanged.
type aiTokenEvent struct {
	Text  string `json:"text"`
	Index int    `json:"index"`
}

type aiBlockStartEvent struct {
	Index        int                    `json:"index"`
	ContentBlock map[string]interface{} `json:"content_block"`
}

type aiContentBlockStopEvent struct {
	Index int `json:"index"`
}

type aiInputJsonDeltaEvent struct {
	PartialJSON string `json:"partial_json"`
}

type aiMessageStartEvent struct {
	Role string `json:"role"`
}

type aiDoneEvent struct {
	Message    map[string]interface{} `json:"message"`
	Usage      map[string]interface{} `json:"usage,omitempty"`
	StopReason string                 `json:"stop_reason"`
}

// chatCompletionAnthropic handles the native Anthropic Messages API with SSE streaming.
func (a *App) chatCompletionAnthropic(apiKey, baseURL, model string, reqBody map[string]interface{}, userAgent string) (string, error) {
	reqBody["stream"] = true

	modifiedJSON, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal modified request: %w", err)
	}

	// Anthropic base URL conventionally omits /v1 (client appends /v1/messages).
	// Tolerate legacy configs that already include the /v1 suffix.
	base := strings.TrimRight(baseURL, "/")
	var url string
	if strings.HasSuffix(base, "/v1") {
		url = base + "/messages"
	} else {
		url = base + "/v1/messages"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Second)
	defer cancel()

	// F-308: atomic pointer swap so overlapping ChatCompletion calls
	// don't clobber each other's cancel function.
	myCancel := cancel
	a.chatCancel.Store(&myCancel)
	defer func() {
		a.chatCancel.CompareAndSwap(&myCancel, nil)
	}()

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(modifiedJSON))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("anthropic-beta", "prompt-caching-2024-07-31")
	req.Header.Set("User-Agent", userAgent)

	client := &http.Client{Timeout: 0}
	res, err := client.Do(req)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("AI_REQUEST_TIMEOUT")
		}
		return "", err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		// F-305: cap error-body reads at 64 KiB.
		body, _ := io.ReadAll(io.LimitReader(res.Body, 64*1024))
		return "", fmt.Errorf("HTTP %d: %s", res.StatusCode, string(body))
	}

	var contentBlocks []map[string]interface{}
	var currentBlock map[string]interface{}
	var messageRole string
	var usage map[string]interface{}
	currentBlockIndex := -1

	scanner := bufio.NewScanner(res.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		dataStr := line[6:]

		var event map[string]interface{}
		if err := json.Unmarshal([]byte(dataStr), &event); err != nil {
			continue
		}

		eventType, _ := event["type"].(string)

		switch eventType {
		case "message_start":
			if msg, ok := event["message"].(map[string]interface{}); ok {
				messageRole, _ = msg["role"].(string)
			}

		case "content_block_start":
			currentBlockIndex++
			if block, ok := event["content_block"].(map[string]interface{}); ok {
				currentBlock = block
				a.emit("ai:block_start", map[string]interface{}{
					"index":         currentBlockIndex,
					"content_block": block,
				})
			}

		case "content_block_delta":
			delta, _ := event["delta"].(map[string]interface{})
			deltaType, _ := delta["type"].(string)

			if deltaType == "text_delta" {
				text, _ := delta["text"].(string)
				if currentBlock != nil {
					if currentBlock["text"] == nil {
						currentBlock["text"] = ""
					}
					currentBlock["text"] = currentBlock["text"].(string) + text
				}
				a.emit("ai:token", map[string]interface{}{
					"text":  text,
					"index": currentBlockIndex,
				})
			}
			if deltaType == "input_json_delta" && currentBlock != nil {
				partial, _ := delta["partial_json"].(string)
				if currentBlock["input"] == nil || fmt.Sprintf("%T", currentBlock["input"]) != "string" {
					currentBlock["input"] = ""
				}
				if s, ok := currentBlock["input"].(string); ok {
					currentBlock["input"] = s + partial
				}
			}

		case "content_block_stop":
			if currentBlock != nil {
				if blockType, _ := currentBlock["type"].(string); blockType == "tool_use" {
					if inputStr, ok := currentBlock["input"].(string); ok && inputStr != "" {
						var inputObj map[string]interface{}
						if err := json.Unmarshal([]byte(inputStr), &inputObj); err == nil {
							currentBlock["input"] = inputObj
						}
					}
				}
				contentBlocks = append(contentBlocks, currentBlock)
				currentBlock = nil
			}

		case "message_delta":
			if u, ok := event["usage"].(map[string]interface{}); ok {
				usage = u
			}
			if delta, ok := event["delta"].(map[string]interface{}); ok {
				if stopReason, ok := delta["stop_reason"].(string); ok {
					a.emit("ai:done", map[string]interface{}{
						"message": map[string]interface{}{
							"role":    messageRole,
							"content": contentBlocks,
						},
						"usage":       usage,
						"stop_reason": stopReason,
					})
				}
			}

		case "message_stop":
			fullMessage := map[string]interface{}{
				"role":    messageRole,
				"content": contentBlocks,
			}
			resultJSON, err := json.Marshal(fullMessage)
			if err != nil {
				return "", fmt.Errorf("marshal full message: %w", err)
			}
			return string(resultJSON), nil

		case "error":
			errData, _ := event["error"].(map[string]interface{})
			errMsg, _ := errData["message"].(string)
			return "", fmt.Errorf("stream error: %s", errMsg)
		}
	}

	if err := scanner.Err(); err != nil {
		return "", err
	}

	if len(contentBlocks) > 0 {
		fullMessage := map[string]interface{}{
			"role":    messageRole,
			"content": contentBlocks,
		}
		resultJSON, _ := json.Marshal(fullMessage)
		return string(resultJSON), nil
	}

	return "", fmt.Errorf("stream ended without message_stop")
}

// marshalAnthropicFinalMessage encodes a final message using a pooled
// *bytes.Buffer to avoid per-turn allocator churn in heavy sessions.
func marshalAnthropicFinalMessage(msg map[string]interface{}) ([]byte, error) {
	buf := finalMsgPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer finalMsgPool.Put(buf)
	enc := json.NewEncoder(buf)
	if err := enc.Encode(msg); err != nil {
		return nil, err
	}
	// json.Encoder always appends a trailing newline; trim it.
	out := buf.Bytes()
	if n := len(out); n > 0 && out[n-1] == '\n' {
		out = out[:n-1]
	}
	return out, nil
}

var finalMsgPool = stdsync.Pool{
	New: func() any {
		b := &bytes.Buffer{}
		b.Grow(4 * 1024)
		return b
	},
}

func anthropicToolToOpenAI(t map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{
		"type": "function",
		"function": map[string]interface{}{
			"name":        t["name"],
			"description": t["description"],
			"parameters":  t["input_schema"],
		},
	}
}

// convertAnthropicMessageToOpenAI converts one Anthropic-format message to OpenAI format.
func convertAnthropicMessageToOpenAI(msg map[string]interface{}) []map[string]interface{} {
	role, _ := msg["role"].(string)
	content := msg["content"]

	var results []map[string]interface{}

	switch role {
	case "user":
		out := map[string]interface{}{"role": "user"}
		if contentStr, ok := content.(string); ok {
			out["content"] = contentStr
		} else if contentBlocks, ok := content.([]interface{}); ok {
			for _, block := range contentBlocks {
				if b, ok := block.(map[string]interface{}); ok {
					if bType, _ := b["type"].(string); bType == "text" {
						out["content"] = b["text"]
					}
					if bType, _ := b["type"].(string); bType == "tool_result" {
						toolMsg := map[string]interface{}{
							"role":         "tool",
							"tool_call_id": b["tool_use_id"],
							"content":      toString(b["content"]),
						}
						results = append(results, toolMsg)
					}
				}
			}
		}
		// Emit tool messages first, then any text user message. An OpenAI-format
		// assistant message with tool_calls must be immediately followed by the
		// matching tool messages; a user text block placed before them triggers a
		// 400 "insufficient tool messages following tool_calls" error.
		if _, hasContent := out["content"]; hasContent {
			results = append(results, out)
		}

	case "assistant":
		out := map[string]interface{}{"role": "assistant"}
		var toolCalls []map[string]interface{}
		if contentStr, ok := content.(string); ok {
			out["content"] = contentStr
		} else if contentBlocks, ok := content.([]interface{}); ok {
			for _, block := range contentBlocks {
				if b, ok := block.(map[string]interface{}); ok {
					if bType, _ := b["type"].(string); bType == "text" {
						out["content"] = b["text"]
					}
					if bType, _ := b["type"].(string); bType == "tool_use" {
						argsStr := "{}"
						if input, ok := b["input"]; ok {
							argsBytes, _ := json.Marshal(input)
							argsStr = string(argsBytes)
						}
						toolCalls = append(toolCalls, map[string]interface{}{
							"id":   b["id"],
							"type": "function",
							"function": map[string]interface{}{
								"name":      b["name"],
								"arguments": argsStr,
							},
						})
					}
				}
			}
		}
		if len(toolCalls) > 0 {
			out["tool_calls"] = toolCalls
		}
		results = append([]map[string]interface{}{out}, results...)

	default:
		out := map[string]interface{}{"role": role}
		if contentStr, ok := content.(string); ok {
			out["content"] = contentStr
		}
		results = append([]map[string]interface{}{out}, results...)
	}

	return results
}

func toString(v interface{}) string {
	switch val := v.(type) {
	case string:
		return val
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

// F-306: typed SSE shapes for OpenAI Chat Completions. Only the few
// fields the loop reads (delta.content, delta.tool_calls[], choice.finish_reason)
// get decoded; the rest is discarded by the json decoder.
type openaiDeltaToolCall struct {
	Index    *int   `json:"index,omitempty"`
	ID       string `json:"id,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function"`
}

type openaiStreamDelta struct {
	Content   string                `json:"content"`
	ToolCalls []openaiDeltaToolCall `json:"tool_calls"`
}

type openaiStreamChoice struct {
	Delta        openaiStreamDelta `json:"delta"`
	FinishReason string            `json:"finish_reason"`
}

type openaiStreamEvent struct {
	Choices []openaiStreamChoice `json:"choices"`
}

// chatCompletionOpenAI converts the Anthropic-format request to OpenAI,
// calls the OpenAI Chat Completions API with SSE streaming, and converts
// the response back to Anthropic format so the frontend sees no difference.
func (a *App) chatCompletionOpenAI(apiKey, baseURL, model string, reqBody map[string]interface{}, userAgent string) (string, error) {
	url := strings.TrimRight(baseURL, "/") + "/chat/completions"

	// --- Build OpenAI-format request body ---
	openaiBody := map[string]interface{}{
		"model":      model,
		"stream":     true,
		"max_tokens": reqBody["max_tokens"],
	}

	// Convert tools
	if tools, ok := reqBody["tools"].([]interface{}); ok {
		var oaiTools []map[string]interface{}
		for _, t := range tools {
			if tm, ok := t.(map[string]interface{}); ok {
				oaiTools = append(oaiTools, anthropicToolToOpenAI(tm))
			}
		}
		if len(oaiTools) > 0 {
			openaiBody["tools"] = oaiTools
		}
	}

	// Convert messages + system
	var oaiMessages []map[string]interface{}
	if system, ok := reqBody["system"].(string); ok && system != "" {
		oaiMessages = append(oaiMessages, map[string]interface{}{
			"role":    "system",
			"content": system,
		})
	}
	if msgs, ok := reqBody["messages"].([]interface{}); ok {
		for _, m := range msgs {
			if mm, ok := m.(map[string]interface{}); ok {
				converted := convertAnthropicMessageToOpenAI(mm)
				oaiMessages = append(oaiMessages, converted...)
			}
		}
	}
	openaiBody["messages"] = oaiMessages

	requestJSON, err := json.Marshal(openaiBody)
	if err != nil {
		return "", fmt.Errorf("marshal openai request: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Second)
	defer cancel()

	// F-308: register our cancel in the App-level pointer and only
	// clear it on the way out if no one replaced us. The previous code
	// stored a single context.CancelFunc under a mutex and unconditionally
	// nil'd it on defer; when two ChatCompletion calls overlapped, call A's
	// defer wiped call B's cancel and CancelChatStream became a no-op for B.
	myCancel := cancel
	a.chatCancel.Store(&myCancel)
	defer func() {
		// CAS the slot back to nil, but only if it still points at our
		// own cancel — a newer call may have already taken over the slot.
		a.chatCancel.CompareAndSwap(&myCancel, nil)
	}()

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(requestJSON))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("User-Agent", userAgent)

	client := a.llmHTTPClient()
	res, err := client.Do(req)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("AI_REQUEST_TIMEOUT")
		}
		return "", err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		// F-305: cap the error-body read at 64 KiB so a hostile or
		// buggy upstream returning a multi-GB error body can't OOM
		// the Go process.
		body, _ := io.ReadAll(io.LimitReader(res.Body, 64*1024))
		return "", fmt.Errorf("HTTP %d: %s", res.StatusCode, string(body))
	}

	// --- Parse OpenAI SSE stream, emit Anthropic-format events ---
	var contentBlocks []map[string]interface{}
	var currentBlock map[string]interface{}
	var messageRole = "assistant"
	currentBlockIndex := -1
	activeToolCalls := make(map[int]map[string]interface{}) // index -> accumulating tool_call
	// F-307: per-block text and input buffers so accumulation is O(n)
	// instead of O(n²) string concat per token. Flushed to the block
	// map on content_block_stop / finish_reason.
	var currentTextBuf, currentInputBuf bytes.Buffer
	// Per-tool input buffer so each tool_call's argument concat stays
	// O(n). Keyed by the tool's index — multiple tool_calls can run
	// in parallel (one per idx).
	toolInputBufs := make(map[int]*bytes.Buffer)

	scanner := bufio.NewScanner(res.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	flushCurrentTextBlock := func() {
		if currentBlock == nil {
			return
		}
		if t, _ := currentBlock["type"].(string); t == "text" && currentTextBuf.Len() > 0 {
			currentBlock["text"] = currentTextBuf.String()
		}
		contentBlocks = append(contentBlocks, currentBlock)
		currentBlock = nil
		currentTextBuf.Reset()
	}

	// Emit message_start at the beginning
	// F-320: typed payload (frontend reads event.message.role).
	a.emit("ai:message_start", aiMessageStartEvent{
		Role: "assistant",
	})

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		dataStr := line[6:]

		if strings.TrimSpace(dataStr) == "[DONE]" {
			// Emit content_block_stop for any open block
			if currentBlock != nil {
				flushCurrentTextBlock()
				a.emit("ai:content_block_stop", aiContentBlockStopEvent{
					Index: currentBlockIndex,
				})
			}
			// Close any open tool_use blocks
			for idx, tc := range activeToolCalls {
				contentBlocks = append(contentBlocks, tc)
				a.emit("ai:content_block_stop", aiContentBlockStopEvent{
					Index: idx,
				})
			}
			activeToolCalls = make(map[int]map[string]interface{})

			// Emit message_delta and message_stop
			// F-320: typed payload.
			a.emit("ai:done", aiDoneEvent{
				Message: map[string]interface{}{
					"role":    messageRole,
					"content": contentBlocks,
				},
				StopReason: "end_turn",
			})

			fullMessage := map[string]interface{}{
				"role":    messageRole,
				"content": contentBlocks,
			}
			resultJSON, _ := json.Marshal(fullMessage)
			return string(resultJSON), nil
		}

		var ev openaiStreamEvent
		if err := json.Unmarshal([]byte(dataStr), &ev); err != nil {
			continue
		}
		if len(ev.Choices) == 0 {
			continue
		}
		choice := ev.Choices[0]
		delta := choice.Delta

		// Handle text content
		if delta.Content != "" {
			if currentBlock == nil || currentBlock["type"] != "text" {
				// Close previous block if any
				if currentBlock != nil {
					flushCurrentTextBlock()
					a.emit("ai:content_block_stop", aiContentBlockStopEvent{
						Index: currentBlockIndex,
					})
				}
				currentBlockIndex++
				currentBlock = map[string]interface{}{
					"type": "text",
					"text": "",
				}
				currentTextBuf.Reset()
				// F-320: typed payload.
				a.emit("ai:block_start", aiBlockStartEvent{
					Index:        currentBlockIndex,
					ContentBlock: currentBlock,
				})
			}
			currentTextBuf.WriteString(delta.Content)
			// F-320: typed struct + dropped unused fields — see
			// chatCompletionAnthropic for rationale.
			a.emit("ai:token", aiTokenEvent{
				Text:  delta.Content,
				Index: currentBlockIndex,
			})
		}

		// Handle tool_calls in delta
		for _, tc := range delta.ToolCalls {
			if tc.Index == nil {
				continue
			}
			idx := *tc.Index

			if _, exists := activeToolCalls[idx]; !exists {
				// Close current text block if open
				if currentBlock != nil {
					flushCurrentTextBlock()
					a.emit("ai:content_block_stop", aiContentBlockStopEvent{
						Index: currentBlockIndex,
					})
				}
				currentBlockIndex++
				activeToolCalls[idx] = map[string]interface{}{
					"type":  "tool_use",
					"id":    tc.ID,
					"name":  "",
					"input": "",
				}
				// F-320: typed payload.
				a.emit("ai:block_start", aiBlockStartEvent{
					Index: currentBlockIndex,
					ContentBlock: map[string]interface{}{
						"type": "tool_use",
						"id":   tc.ID,
					},
				})
			}

			atc := activeToolCalls[idx]
			if tc.Function.Name != "" {
				atc["name"] = tc.Function.Name
			}
			if args := tc.Function.Arguments; args != "" {
				// F-307: append to a per-tool *bytes.Buffer instead of
				// string concat (O(n²) over a long tool-args stream).
				buf, ok := toolInputBufs[idx]
				if !ok {
					buf = &bytes.Buffer{}
					toolInputBufs[idx] = buf
				}
				buf.WriteString(args)
				// F-320: typed payload.
				a.emit("ai:input_json_delta", aiInputJsonDeltaEvent{
					PartialJSON: args,
				})
			}
		}

		// Handle finish_reason on the choice level
		finishReason := choice.FinishReason
		if finishReason != "" && finishReason != "null" {
			// Close any open text block
			if currentBlock != nil {
				flushCurrentTextBlock()
				a.emit("ai:content_block_stop", aiContentBlockStopEvent{
					Index: currentBlockIndex,
				})
			}
			// Close tool_use blocks and parse their input JSON
			for idx, tc := range activeToolCalls {
				// F-307: prefer the per-tool buffer over the
				// possibly-empty tc["input"] string.
				if buf, ok := toolInputBufs[idx]; ok && buf.Len() > 0 {
					inputStr := buf.String()
					var inputObj map[string]interface{}
					if err := json.Unmarshal([]byte(inputStr), &inputObj); err == nil {
						tc["input"] = inputObj
					} else {
						tc["input"] = inputStr
					}
				} else if inputStr, ok := tc["input"].(string); ok && inputStr != "" {
					var inputObj map[string]interface{}
					if err := json.Unmarshal([]byte(inputStr), &inputObj); err == nil {
						tc["input"] = inputObj
					}
				}
				contentBlocks = append(contentBlocks, tc)
				a.emit("ai:content_block_stop", aiContentBlockStopEvent{
					Index: idx,
				})
			}
			activeToolCalls = make(map[int]map[string]interface{})
			toolInputBufs = nil
			currentInputBuf.Reset()

			stopReason := "end_turn"
			if finishReason == "tool_calls" {
				stopReason = "tool_use"
			} else if finishReason == "length" {
				stopReason = "max_tokens"
			} else if finishReason == "stop" {
				stopReason = "end_turn"
			}

			// F-320: typed payload.
			a.emit("ai:done", aiDoneEvent{
				Message: map[string]interface{}{
					"role":    messageRole,
					"content": contentBlocks,
				},
				StopReason: stopReason,
			})

			fullMessage := map[string]interface{}{
				"role":    messageRole,
				"content": contentBlocks,
			}
			resultJSON, _ := json.Marshal(fullMessage)
			return string(resultJSON), nil
		}
	}

	if err := scanner.Err(); err != nil {
		return "", err
	}

	if len(contentBlocks) > 0 || len(activeToolCalls) > 0 {
		for _, tc := range activeToolCalls {
			contentBlocks = append(contentBlocks, tc)
		}
		fullMessage := map[string]interface{}{
			"role":    messageRole,
			"content": contentBlocks,
		}
		resultJSON, _ := json.Marshal(fullMessage)
		return string(resultJSON), nil
	}

	return "", fmt.Errorf("stream ended without completion")
}

// anthropicToolToResponses converts an Anthropic tool definition to the
// Responses API function format (flat, unlike Chat Completions' nested form).
func anthropicToolToResponses(t map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{
		"type":        "function",
		"name":        t["name"],
		"description": t["description"],
		"parameters":  t["input_schema"],
	}
}

// convertAnthropicMessageToResponses converts one Anthropic-format message to
// Responses API input items. Text turns become message items with
// input_text/output_text; tool_use becomes function_call; tool_result becomes
// function_call_output.
func convertAnthropicMessageToResponses(msg map[string]interface{}) []map[string]interface{} {
	role, _ := msg["role"].(string)
	content := msg["content"]

	var results []map[string]interface{}

	textType := "input_text"
	if role == "assistant" {
		textType = "output_text"
	}

	if contentStr, ok := content.(string); ok {
		if contentStr != "" {
			results = append(results, map[string]interface{}{
				"role": role,
				"content": []map[string]interface{}{
					{"type": textType, "text": contentStr},
				},
			})
		}
		return results
	}

	contentBlocks, ok := content.([]interface{})
	if !ok {
		return results
	}

	var textParts []map[string]interface{}
	for _, block := range contentBlocks {
		b, ok := block.(map[string]interface{})
		if !ok {
			continue
		}
		switch b["type"] {
		case "text":
			if txt, _ := b["text"].(string); txt != "" {
				textParts = append(textParts, map[string]interface{}{"type": textType, "text": txt})
			}
		case "tool_use":
			argsStr := "{}"
			if input, ok := b["input"]; ok {
				argsBytes, _ := json.Marshal(input)
				argsStr = string(argsBytes)
			}
			results = append(results, map[string]interface{}{
				"type":      "function_call",
				"call_id":   b["id"],
				"name":      b["name"],
				"arguments": argsStr,
			})
		case "tool_result":
			results = append(results, map[string]interface{}{
				"type":    "function_call_output",
				"call_id": b["tool_use_id"],
				"output":  toString(b["content"]),
			})
		}
	}

	if len(textParts) > 0 {
		msgItem := map[string]interface{}{"role": role, "content": textParts}
		if role == "assistant" {
			results = append([]map[string]interface{}{msgItem}, results...)
		} else {
			results = append(results, msgItem)
		}
	}

	return results
}

// F-306: typed SSE shapes for OpenAI Responses events. The wrapper
// captures the discriminator + output_index; nested item fields are
// decoded lazily per branch so we skip the ~99% of fields the loop
// discards.
type responsesStreamItem struct {
	Type   string `json:"type"`
	CallID string `json:"call_id"`
	Name   string `json:"name"`
}

type responsesStreamEvent struct {
	Type        string          `json:"type"`
	OutputIndex int             `json:"output_index"`
	Item        json.RawMessage `json:"item"`
	Delta       string          `json:"delta"`
}

// chatCompletionResponses converts the Anthropic-format request to the OpenAI
// Responses API, calls /responses with SSE streaming, and converts the response
// events back to Anthropic-format events so the frontend sees no difference.
// Stateless: full history is sent as `input` each turn; reasoning items are ignored.
func (a *App) chatCompletionResponses(apiKey, baseURL, model string, reqBody map[string]interface{}, userAgent string) (string, error) {
	url := strings.TrimRight(baseURL, "/") + "/responses"

	// --- Build Responses-format request body ---
	respBody := map[string]interface{}{
		"model":  model,
		"stream": true,
	}
	if mt, ok := reqBody["max_tokens"]; ok {
		respBody["max_output_tokens"] = mt
	}
	if system, ok := reqBody["system"].(string); ok && system != "" {
		respBody["instructions"] = system
	}

	if tools, ok := reqBody["tools"].([]interface{}); ok {
		var respTools []map[string]interface{}
		for _, t := range tools {
			if tm, ok := t.(map[string]interface{}); ok {
				respTools = append(respTools, anthropicToolToResponses(tm))
			}
		}
		if len(respTools) > 0 {
			respBody["tools"] = respTools
		}
	}

	var input []map[string]interface{}
	if msgs, ok := reqBody["messages"].([]interface{}); ok {
		for _, m := range msgs {
			if mm, ok := m.(map[string]interface{}); ok {
				input = append(input, convertAnthropicMessageToResponses(mm)...)
			}
		}
	}
	respBody["input"] = input

	requestJSON, err := json.Marshal(respBody)
	if err != nil {
		return "", fmt.Errorf("marshal responses request: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Second)
	defer cancel()

	// F-308: register our cancel in the App-level pointer and only
	// clear it on the way out if no one replaced us. The previous code
	// stored a single context.CancelFunc under a mutex and unconditionally
	// nil'd it on defer; when two ChatCompletion calls overlapped, call A's
	// defer wiped call B's cancel and CancelChatStream became a no-op for B.
	myCancel := cancel
	a.chatCancel.Store(&myCancel)
	defer func() {
		// CAS the slot back to nil, but only if it still points at our
		// own cancel — a newer call may have already taken over the slot.
		a.chatCancel.CompareAndSwap(&myCancel, nil)
	}()

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(requestJSON))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("User-Agent", userAgent)

	client := a.llmHTTPClient()
	res, err := client.Do(req)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("AI_REQUEST_TIMEOUT")
		}
		return "", err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		// F-305: cap the error-body read at 64 KiB so a hostile or
		// buggy upstream returning a multi-GB error body can't OOM
		// the Go process.
		body, _ := io.ReadAll(io.LimitReader(res.Body, 64*1024))
		return "", fmt.Errorf("HTTP %d: %s", res.StatusCode, string(body))
	}

	// --- Parse Responses SSE stream, emit Anthropic-format events ---
	var contentBlocks []map[string]interface{}
	// Map Responses output_index -> our content block index / accumulating block.
	blockByOutputIdx := make(map[int]map[string]interface{})
	idxByOutputIdx := make(map[int]int)
	nextBlockIndex := 0
	// F-307: parallel maps of *bytes.Buffer so text/input accumulation
	// is O(n) instead of O(n²) string concat per token. Outputs may run
	// in parallel (different output_index) so a single shared buffer
	// doesn't work — keep one per output_index.
	textBufs := make(map[int]*bytes.Buffer)
	inputBufs := make(map[int]*bytes.Buffer)

	scanner := bufio.NewScanner(res.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	// F-320: typed payload.
	a.emit("ai:message_start", aiMessageStartEvent{
		Role: "assistant",
	})

	finish := func(stopReason string) (string, error) {
		fullMessage := map[string]interface{}{
			"role":    "assistant",
			"content": contentBlocks,
		}
		resultJSON, err := json.Marshal(fullMessage)
		if err != nil {
			return "", fmt.Errorf("marshal final message: %w", err)
		}
		// F-320: typed payload with json.RawMessage so the
		// already-marshaled message bytes pass through untouched.
		a.emit("ai:done", struct {
			Message    json.RawMessage `json:"message"`
			StopReason string          `json:"stop_reason"`
		}{
			Message:    json.RawMessage(resultJSON),
			StopReason: stopReason,
		})
		return string(resultJSON), nil
	}

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		dataStr := line[6:]
		if strings.TrimSpace(dataStr) == "[DONE]" {
			continue
		}

		var ev responsesStreamEvent
		if err := json.Unmarshal([]byte(dataStr), &ev); err != nil {
			continue
		}

		switch ev.Type {
		case "response.output_item.added":
			var item responsesStreamItem
			if err := json.Unmarshal(ev.Item, &item); err != nil {
				continue
			}
			switch item.Type {
			case "message":
				block := map[string]interface{}{"type": "text", "text": ""}
				blockByOutputIdx[ev.OutputIndex] = block
				idxByOutputIdx[ev.OutputIndex] = nextBlockIndex
				// F-320: typed payload.
				a.emit("ai:block_start", aiBlockStartEvent{
					Index:        nextBlockIndex,
					ContentBlock: block,
				})
				nextBlockIndex++
			case "function_call":
				block := map[string]interface{}{
					"type":  "tool_use",
					"id":    item.CallID,
					"name":  item.Name,
					"input": "",
				}
				blockByOutputIdx[ev.OutputIndex] = block
				idxByOutputIdx[ev.OutputIndex] = nextBlockIndex
				// F-320: typed payload.
				a.emit("ai:block_start", aiBlockStartEvent{
					Index: nextBlockIndex,
					ContentBlock: map[string]interface{}{
						"type": "tool_use",
						"id":   item.CallID,
						"name": item.Name,
					},
				})
				nextBlockIndex++
			}

		case "response.output_text.delta":
			block := blockByOutputIdx[ev.OutputIndex]
			if block == nil {
				continue
			}
			if ev.Delta == "" {
				continue
			}
			// F-307: append to per-block *bytes.Buffer instead of
			// O(n²) string concatenation. Flushed on output_item.done.
			buf, ok := textBufs[ev.OutputIndex]
			if !ok {
				buf = &bytes.Buffer{}
				textBufs[ev.OutputIndex] = buf
			}
			buf.WriteString(ev.Delta)
			// F-320: typed struct + dropped unused fields — see
			// chatCompletionAnthropic for rationale.
			a.emit("ai:token", aiTokenEvent{
				Text:  ev.Delta,
				Index: idxByOutputIdx[ev.OutputIndex],
			})

		case "response.function_call_arguments.delta":
			block := blockByOutputIdx[ev.OutputIndex]
			if block == nil {
				continue
			}
			if ev.Delta == "" {
				continue
			}
			buf, ok := inputBufs[ev.OutputIndex]
			if !ok {
				buf = &bytes.Buffer{}
				inputBufs[ev.OutputIndex] = buf
			}
			buf.WriteString(ev.Delta)
			// F-320: typed payload.
			a.emit("ai:input_json_delta", aiInputJsonDeltaEvent{
				PartialJSON: ev.Delta,
			})

		case "response.output_item.done":
			block := blockByOutputIdx[ev.OutputIndex]
			if block == nil {
				continue
			}
			// F-307: flush per-block buffers once into the block map.
			if buf, ok := textBufs[ev.OutputIndex]; ok {
				if buf.Len() > 0 {
					block["text"] = buf.String()
				}
				delete(textBufs, ev.OutputIndex)
			}
			if buf, ok := inputBufs[ev.OutputIndex]; ok {
				if buf.Len() > 0 {
					inputStr := buf.String()
					if block["type"] == "tool_use" {
						var inputObj map[string]interface{}
						if json.Unmarshal([]byte(inputStr), &inputObj) == nil {
							block["input"] = inputObj
						} else {
							block["input"] = map[string]interface{}{}
						}
					} else {
						block["input"] = inputStr
					}
				}
				delete(inputBufs, ev.OutputIndex)
			}
			contentBlocks = append(contentBlocks, block)
			// F-320: typed payload.
			a.emit("ai:content_block_stop", aiContentBlockStopEvent{
				Index: idxByOutputIdx[ev.OutputIndex],
			})
			delete(blockByOutputIdx, ev.OutputIndex)

		case "response.completed":
			stopReason := "end_turn"
			for _, b := range contentBlocks {
				if b["type"] == "tool_use" {
					stopReason = "tool_use"
					break
				}
			}
			return finish(stopReason)

		case "response.failed", "error":
			// Marshal the typed event back out for the error message; the
			// caller doesn't need the original map shape.
			body, _ := json.Marshal(ev)
			return "", fmt.Errorf("responses stream error: %s", string(body))
		}
	}

	if err := scanner.Err(); err != nil {
		return "", err
	}

	if len(contentBlocks) > 0 {
		return finish("end_turn")
	}

	return "", fmt.Errorf("stream ended without completion")
}

// CancelChatStream cancels the currently active ChatCompletion stream.
func (a *App) CancelChatStream() {
	if c := a.chatCancel.Load(); c != nil {
		(*c)()
	}
}

// ModelInfo represents a model entry from the /v1/models response.
type ModelInfo struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

// FetchModels fetches the available model list. openai/responses hit an
// OpenAI-compatible /models endpoint with a Bearer token; anthropic hits
// /v1/models with the x-api-key + anthropic-version headers, mirroring
// chatCompletionAnthropic's URL and auth handling.
func (a *App) FetchModels(apiKey, baseURL, protocol string) ([]ModelInfo, error) {
	base := strings.TrimRight(baseURL, "/")

	var url string
	if protocol == "anthropic" {
		// Base URL conventionally omits /v1; tolerate legacy configs with it.
		if strings.HasSuffix(base, "/v1") {
			url = base + "/models"
		} else {
			url = base + "/v1/models"
		}
	} else {
		url = base + "/models"
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	if protocol == "anthropic" {
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")
	} else {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	req.Header.Set("User-Agent", "uniTerm")

	// F-208: share the same transport as the LLM clients so the model
	// list call also benefits from the keep-alive pool; the request
	// itself carries its own 10s deadline via the per-request context.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req = req.WithContext(ctx)
	res, err := a.llmHTTPClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", res.StatusCode, string(body))
	}

	var result struct {
		Data []ModelInfo `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse models response: %w", err)
	}
	return result.Data, nil
}

// FrontendLog writes a frontend log message to the application log file.
// This is the canonical interface for the frontend to persist debug/audit
// messages alongside backend logs.
func (a *App) FrontendLog(tag string, message string) {
	_ = log.Init()
	log.Writef("[%s] %s", tag, message)
}

// GetDefaultShell returns the system's default shell path for local terminals.
func (a *App) GetDefaultShell() string {
	switch goruntime.GOOS {
	case "windows":
		if _, err := exec.LookPath("pwsh.exe"); err == nil {
			return "pwsh.exe"
		}
		if _, err := exec.LookPath("powershell.exe"); err == nil {
			return "powershell.exe"
		}
		// Prefer explicit Git for Windows paths over WSL bash to avoid
		// WSL relay errors when no Linux distribution is installed.
		for _, p := range []string{
			`C:\Program Files\Git\bin\bash.exe`,
			`C:\Program Files (x86)\Git\bin\bash.exe`,
		} {
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
		if _, err := exec.LookPath("bash.exe"); err == nil {
			return "bash.exe"
		}
		return "cmd.exe"
	default:
		if shell := os.Getenv("SHELL"); shell != "" {
			return shell
		}
		if _, err := exec.LookPath("bash"); err == nil {
			return "bash"
		}
		return "sh"
	}
}

// ListSerialPorts returns available serial port names.
func (a *App) ListSerialPorts() ([]string, error) {
	return session.ListSerialPorts()
}

// \u2500\u2500 Session output log \u2500\u2500

// SessionLogInfo describes the current session-log state for a panel.
// Path is "" when Enabled is false.
type SessionLogInfo struct {
	Enabled bool   `json:"enabled"`
	Path    string `json:"path"`
}

// RegisterSessionForPanel binds a session to a panel and, if the panel
// already has an active log, attaches the log writer to the session so
// output starts landing in the log immediately. The frontend calls this
// right after CreateSession succeeds, and on every reconnect.
//
// On the first Register for a panel (i.e. not a reconnect), if the
// session was created from a connection with LogOnConnect=true, the
// log is enabled automatically. Later Registers for the same panel
// never re-trigger — the user's manual stop is respected across
// reconnects for the life of the panel.
func (a *App) RegisterSessionForPanel(sessionID, panelID string) {
	if sessionID == "" || panelID == "" {
		return
	}
	a.panelLogMu.Lock()
	a.sessionToPanel[sessionID] = panelID
	logger := a.panelLogs[panelID]
	autoTriggered := a.panelAutoTriggered[panelID]
	a.panelLogMu.Unlock()

	// Existing logger (reconnect case): rewire writer, don't re-enable.
	if logger != nil {
		a.installWriter(sessionID, logger)
		return
	}

	// First Register for this panel: check LogOnConnect and auto-enable.
	if !autoTriggered {
		a.panelLogMu.Lock()
		a.panelAutoTriggered[panelID] = true
		a.panelLogMu.Unlock()
		if a.sessionWantsAutoLog(sessionID) {
			// EnableSessionOutputLog handles the writer install internally.
			if _, err := a.EnableSessionOutputLog(panelID, ""); err != nil {
				log.Writef("[RegisterSessionForPanel] auto-enable log failed: %v", err)
			}
		}
	}
}

// sessionWantsAutoLog reports whether the session was created from a
// connection that opted in to LogOnConnect. Returns false for missing
// or non-terminal sessions.
func (a *App) sessionWantsAutoLog(sessionID string) bool {
	if a.sessionManager == nil {
		return false
	}
	s, ok := a.sessionManager.Get(sessionID)
	if !ok {
		return false
	}
	if q, ok := s.(interface{ AutoLogOnConnect() bool }); ok {
		return q.AutoLogOnConnect()
	}
	return false
}

// UnregisterSession clears the session\u2192panel binding and detaches any
// writer from the session. The logger itself is unaffected: it stays on
// the panel, waiting for the next session (reconnect) to register.
func (a *App) UnregisterSession(sessionID string) {
	if sessionID == "" {
		return
	}
	cancelExternalEdits(sessionID)
	a.panelLogMu.Lock()
	delete(a.sessionToPanel, sessionID)
	a.panelLogMu.Unlock()
	a.installWriter(sessionID, nil)
}

// installWriter finds the given session and installs (or clears) the
// output-log writer callback. Non-terminal session types silently
// ignore the request.
func (a *App) installWriter(sessionID string, logger *session.OutputLogger) {
	if a.sessionManager == nil {
		return
	}
	s, ok := a.sessionManager.Get(sessionID)
	if !ok {
		return
	}
	setter, ok := s.(interface{ SetOutputLogWriter(func([]byte)) })
	if !ok {
		return
	}
	if logger == nil {
		setter.SetOutputLogWriter(nil)
		return
	}
	setter.SetOutputLogWriter(logger.WriteOutput)
}

// panelLogTitle picks the filename base for a panel's log. Uses the
// current session's Title if available, otherwise a short synthetic
// name derived from panelID.
func (a *App) panelLogTitle(panelID string) (name, protocol string) {
	a.panelLogMu.Lock()
	var sessionID string
	for sid, pid := range a.sessionToPanel {
		if pid == panelID {
			sessionID = sid
			break
		}
	}
	a.panelLogMu.Unlock()
	if sessionID != "" && a.sessionManager != nil {
		if s, ok := a.sessionManager.Get(sessionID); ok {
			return s.Title(), s.Type()
		}
	}
	suffix := panelID
	if len(suffix) > 8 {
		suffix = suffix[:8]
	}
	return "panel_" + suffix, "session"
}

// EnableSessionOutputLog starts writing terminal output for the given
// panel to a .log file. If dir is empty, the default session log
// directory is used. Returns the final path after sanitization and
// same-second collision suffixing.
//
// The log is bound to the panel, not the session \u2014 so a reconnect
// (which creates a fresh session under the same panel) keeps writing
// to the same file.
func (a *App) EnableSessionOutputLog(panelID, dir string) (string, error) {
	if panelID == "" {
		return "", fmt.Errorf("panelID required")
	}
	// When the caller didn't pin a directory, fall back to the user's
	// configured override; if that is also empty, OutputLogger.Enable
	// will pick the OS default.
	if dir == "" {
		a.customLogDirMu.RLock()
		dir = a.customLogDir
		a.customLogDirMu.RUnlock()
	}
	name, protocol := a.panelLogTitle(panelID)

	a.panelLogMu.Lock()
	logger := a.panelLogs[panelID]
	if logger == nil {
		logger = &session.OutputLogger{}
		a.panelLogs[panelID] = logger
	}
	// Find any session currently bound to this panel so we can wire the
	// writer while we still hold the lock (avoids a race with concurrent
	// register/unregister calls).
	var sessionID string
	for sid, pid := range a.sessionToPanel {
		if pid == panelID {
			sessionID = sid
			break
		}
	}
	a.panelLogMu.Unlock()

	path, err := logger.Enable(dir, name, protocol)
	if err != nil {
		return "", err
	}
	if sessionID != "" {
		a.installWriter(sessionID, logger)
	}
	return path, nil
}

// DisableSessionOutputLog closes the log file for the given panel,
// writes a footer banner, detaches the writer from any active session,
// and drops the panel's logger. Idempotent.
func (a *App) DisableSessionOutputLog(panelID string) error {
	if panelID == "" {
		return nil
	}
	a.panelLogMu.Lock()
	logger := a.panelLogs[panelID]
	delete(a.panelLogs, panelID)
	var sessionID string
	for sid, pid := range a.sessionToPanel {
		if pid == panelID {
			sessionID = sid
			break
		}
	}
	a.panelLogMu.Unlock()
	if sessionID != "" {
		a.installWriter(sessionID, nil)
	}
	if logger != nil {
		logger.Disable()
	}
	return nil
}

// GetSessionOutputLogInfo returns the current log state for a panel.
// Returns zero value when the panel has no active log.
func (a *App) GetSessionOutputLogInfo(panelID string) SessionLogInfo {
	if panelID == "" {
		return SessionLogInfo{}
	}
	a.panelLogMu.Lock()
	logger := a.panelLogs[panelID]
	a.panelLogMu.Unlock()
	if logger == nil {
		return SessionLogInfo{}
	}
	return SessionLogInfo{Enabled: logger.Enabled(), Path: logger.Path()}
}

// OpenPathInExplorer reveals the given file in the platform file
// manager. On Windows uses `explorer /select,<path>`; macOS uses
// `open -R`; Linux uses `xdg-open <dir>` (no selection semantic in
// xdg-open, so the parent directory is opened).
func (a *App) OpenPathInExplorer(path string) error {
	if path == "" {
		return fmt.Errorf("empty path")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	isDir := false
	if info, err := os.Stat(abs); err == nil {
		isDir = info.IsDir()
	}
	switch goruntime.GOOS {
	case "windows":
		// explorer.exe returns exit code 1 on success; ignore Run's error.
		if isDir {
			_ = exec.Command("explorer", abs).Run()
		} else {
			_ = exec.Command("explorer", "/select,", abs).Run()
		}
		return nil
	case "darwin":
		if isDir {
			return exec.Command("open", abs).Run()
		}
		return exec.Command("open", "-R", abs).Run()
	default:
		if isDir {
			return exec.Command("xdg-open", abs).Run()
		}
		return exec.Command("xdg-open", filepath.Dir(abs)).Run()
	}
}

// SetDefaultSessionLogDir installs a user-configured override for the
// directory used by new session logs. Empty clears the override and
// restores the OS default. Existing log files are not migrated; the
// change only affects logs enabled after this call.
func (a *App) SetDefaultSessionLogDir(dir string) {
	a.customLogDirMu.Lock()
	a.customLogDir = dir
	a.customLogDirMu.Unlock()
}

// GetDefaultSessionLogDir returns the directory a fresh session log
// would land in: the user's override if set, otherwise the OS default
// (~/Documents/uniTerm/logs on all platforms). Used by the settings UI
// to show the current default path as a placeholder.
func (a *App) GetDefaultSessionLogDir() string {
	a.customLogDirMu.RLock()
	custom := a.customLogDir
	a.customLogDirMu.RUnlock()
	if custom != "" {
		return custom
	}
	return session.DefaultSessionLogDir()
}

// ── Database methods ──

func (a *App) dbSession(sessionID string) (*session.DatabaseSession, error) {
	s, ok := a.sessionManager.Get(sessionID)
	if !ok {
		log.Writef("[dbSession] session not found: %s", sessionID)
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}
	ds, ok := s.(*session.DatabaseSession)
	if !ok {
		log.Writef("[dbSession] session is not a database session: %s (type=%s)", sessionID, s.Type())
		return nil, fmt.Errorf("session is not a database session: %s", sessionID)
	}
	return ds, nil
}

func (a *App) dbProvider(sessionID string) (*session.DatabaseSession, database.Provider, error) {
	ds, err := a.dbSession(sessionID)
	if err != nil {
		return nil, nil, err
	}
	p, err := database.NewProvider(ds.DBType())
	if err != nil {
		return nil, nil, err
	}
	return ds, p, nil
}

func (a *App) redisSession(sessionID string) (*session.RedisSession, error) {
	s, ok := a.sessionManager.Get(sessionID)
	if !ok {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}
	rs, ok := s.(*session.RedisSession)
	if !ok {
		return nil, fmt.Errorf("session is not a redis session: %s (type=%s)", sessionID, s.Type())
	}
	return rs, nil
}

func (a *App) mongoSession(sessionID string) (*session.MongoSession, error) {
	s, ok := a.sessionManager.Get(sessionID)
	if !ok {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}
	ms, ok := s.(*session.MongoSession)
	if !ok {
		return nil, fmt.Errorf("session is not a mongodb session: %s (type=%s)", sessionID, s.Type())
	}
	return ms, nil
}

func (a *App) esSession(sessionID string) (*session.ElasticsearchSession, error) {
	s, ok := a.sessionManager.Get(sessionID)
	if !ok {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}
	es, ok := s.(*session.ElasticsearchSession)
	if !ok {
		return nil, fmt.Errorf("session is not an elasticsearch session: %s (type=%s)", sessionID, s.Type())
	}
	return es, nil
}

// ── Redis methods ──

func (a *App) RedisPing(sessionID string) error {
	rs, err := a.redisSession(sessionID)
	if err != nil {
		return err
	}
	return rs.Ping()
}

func (a *App) RedisSwitchDB(sessionID string, idx int) error {
	rs, err := a.redisSession(sessionID)
	if err != nil {
		return err
	}
	return rs.SwitchDB(idx)
}

func (a *App) RedisScanKeys(sessionID string, pattern string, cursor uint64, count int64) (*session.ScanResult, error) {
	rs, err := a.redisSession(sessionID)
	if err != nil {
		return nil, err
	}
	return rs.ScanKeys(pattern, cursor, count)
}

func (a *App) RedisGetKeyInfo(sessionID string, key string) (*session.RedisKeyInfo, error) {
	rs, err := a.redisSession(sessionID)
	if err != nil {
		return nil, err
	}
	return rs.GetKeyInfo(key)
}

func (a *App) RedisDBSize(sessionID string) (int64, error) {
	rs, err := a.redisSession(sessionID)
	if err != nil {
		return 0, err
	}
	return rs.DBSize()
}

func (a *App) RedisKeyspaceInfo(sessionID string) (map[int]int64, error) {
	rs, err := a.redisSession(sessionID)
	if err != nil {
		return nil, err
	}
	return rs.KeyspaceInfo()
}

func (a *App) RedisDeleteKey(sessionID string, key string) error {
	rs, err := a.redisSession(sessionID)
	if err != nil {
		return err
	}
	return rs.DeleteKey(key)
}

func (a *App) RedisKeyExists(sessionID string, key string) (bool, error) {
	rs, err := a.redisSession(sessionID)
	if err != nil {
		return false, err
	}
	return rs.KeyExists(key)
}

func (a *App) RedisGetKeyTTL(sessionID string, key string) (int64, error) {
	rs, err := a.redisSession(sessionID)
	if err != nil {
		return -2, err
	}
	return rs.GetKeyTTL(key)
}

func (a *App) RedisSetKeyTTL(sessionID string, key string, seconds int64) error {
	rs, err := a.redisSession(sessionID)
	if err != nil {
		return err
	}
	return rs.SetKeyTTL(key, seconds)
}

func (a *App) RedisGetString(sessionID string, key string) (string, error) {
	rs, err := a.redisSession(sessionID)
	if err != nil {
		return "", err
	}
	return rs.GetString(key)
}

func (a *App) RedisSetString(sessionID string, key string, value string) error {
	rs, err := a.redisSession(sessionID)
	if err != nil {
		return err
	}
	return rs.SetString(key, value)
}

func (a *App) RedisGetHashAll(sessionID string, key string) ([]session.FieldEntry, error) {
	rs, err := a.redisSession(sessionID)
	if err != nil {
		return nil, err
	}
	return rs.GetHashAll(key)
}

func (a *App) RedisHashSet(sessionID string, key string, field string, value string) error {
	rs, err := a.redisSession(sessionID)
	if err != nil {
		return err
	}
	return rs.HashSet(key, field, value)
}

func (a *App) RedisHashDel(sessionID string, key string, fields []string) (int64, error) {
	rs, err := a.redisSession(sessionID)
	if err != nil {
		return 0, err
	}
	return rs.HashDel(key, fields)
}

func (a *App) RedisGetListRange(sessionID string, key string, start int64, stop int64) ([]string, error) {
	rs, err := a.redisSession(sessionID)
	if err != nil {
		return nil, err
	}
	return rs.GetListRange(key, start, stop)
}

func (a *App) RedisListPush(sessionID string, key string, direction string, values []string) (int64, error) {
	rs, err := a.redisSession(sessionID)
	if err != nil {
		return 0, err
	}
	return rs.ListPush(key, direction, values)
}

func (a *App) RedisListPop(sessionID string, key string, direction string) (string, error) {
	rs, err := a.redisSession(sessionID)
	if err != nil {
		return "", err
	}
	return rs.ListPop(key, direction)
}

func (a *App) RedisListSet(sessionID string, key string, index int64, value string) error {
	rs, err := a.redisSession(sessionID)
	if err != nil {
		return err
	}
	return rs.ListSet(key, index, value)
}

func (a *App) RedisListRemove(sessionID string, key string, value string, count int64) (int64, error) {
	rs, err := a.redisSession(sessionID)
	if err != nil {
		return 0, err
	}
	return rs.ListRemove(key, value, count)
}

func (a *App) RedisGetSetAll(sessionID string, key string) ([]string, error) {
	rs, err := a.redisSession(sessionID)
	if err != nil {
		return nil, err
	}
	return rs.GetSetAll(key)
}

func (a *App) RedisSetAdd(sessionID string, key string, members []string) (int64, error) {
	rs, err := a.redisSession(sessionID)
	if err != nil {
		return 0, err
	}
	return rs.SetAdd(key, members)
}

func (a *App) RedisSetRemove(sessionID string, key string, members []string) (int64, error) {
	rs, err := a.redisSession(sessionID)
	if err != nil {
		return 0, err
	}
	return rs.SetRemove(key, members)
}

func (a *App) RedisGetSortedSetRange(sessionID string, key string, min string, max string) ([]session.ScoredMember, error) {
	rs, err := a.redisSession(sessionID)
	if err != nil {
		return nil, err
	}
	return rs.GetSortedSetRange(key, min, max)
}

func (a *App) RedisZSetAdd(sessionID string, key string, members []session.ScoredMember) (int64, error) {
	rs, err := a.redisSession(sessionID)
	if err != nil {
		return 0, err
	}
	return rs.ZSetAdd(key, members)
}

func (a *App) RedisZSetRemove(sessionID string, key string, members []string) (int64, error) {
	rs, err := a.redisSession(sessionID)
	if err != nil {
		return 0, err
	}
	return rs.ZSetRemove(key, members)
}

// ── MongoDB methods ──

func (a *App) MongoPing(sessionID string) error {
	ms, err := a.mongoSession(sessionID)
	if err != nil {
		return err
	}
	return ms.Ping()
}

func (a *App) MongoListDatabases(sessionID string) ([]string, error) {
	ms, err := a.mongoSession(sessionID)
	if err != nil {
		return nil, err
	}
	return ms.ListDatabases()
}

func (a *App) MongoListCollections(sessionID string, dbName string) ([]string, error) {
	ms, err := a.mongoSession(sessionID)
	if err != nil {
		return nil, err
	}
	return ms.ListCollections(dbName)
}

func (a *App) MongoFind(sessionID string, dbName string, collection string, filterJSON string, skip int64, limit int64) (*session.MongoQueryResult, error) {
	ms, err := a.mongoSession(sessionID)
	if err != nil {
		return nil, err
	}
	return ms.Find(dbName, collection, filterJSON, skip, limit)
}

func (a *App) MongoGetDocument(sessionID string, dbName string, collection string, docID string) (string, error) {
	ms, err := a.mongoSession(sessionID)
	if err != nil {
		return "", err
	}
	return ms.GetDocument(dbName, collection, docID)
}

func (a *App) MongoInsertOne(sessionID string, dbName string, collection string, docJSON string) (string, error) {
	ms, err := a.mongoSession(sessionID)
	if err != nil {
		return "", err
	}
	return ms.InsertOne(dbName, collection, docJSON)
}

func (a *App) MongoUpdateOne(sessionID string, dbName string, collection string, filterJSON string, updateJSON string) error {
	ms, err := a.mongoSession(sessionID)
	if err != nil {
		return err
	}
	return ms.UpdateOne(dbName, collection, filterJSON, updateJSON)
}

func (a *App) MongoDeleteOne(sessionID string, dbName string, collection string, filterJSON string) error {
	ms, err := a.mongoSession(sessionID)
	if err != nil {
		return err
	}
	return ms.DeleteOne(dbName, collection, filterJSON)
}

func (a *App) MongoListIndexes(sessionID string, dbName string, collection string) ([]session.MongoIndexInfo, error) {
	ms, err := a.mongoSession(sessionID)
	if err != nil {
		return nil, err
	}
	return ms.ListIndexes(dbName, collection)
}

func (a *App) MongoCreateCollection(sessionID string, dbName string, collection string) error {
	ms, err := a.mongoSession(sessionID)
	if err != nil {
		return err
	}
	return ms.CreateCollection(dbName, collection)
}

func (a *App) MongoDropCollection(sessionID string, dbName string, collection string) error {
	ms, err := a.mongoSession(sessionID)
	if err != nil {
		return err
	}
	return ms.DropCollection(dbName, collection)
}

func (a *App) MongoDropDatabase(sessionID string, dbName string) error {
	ms, err := a.mongoSession(sessionID)
	if err != nil {
		return err
	}
	return ms.DropDatabase(dbName)
}

func (a *App) MongoCreateIndex(sessionID string, dbName string, collection string, name string, keys []string, unique bool) error {
	ms, err := a.mongoSession(sessionID)
	if err != nil {
		return err
	}
	return ms.CreateIndex(dbName, collection, name, keys, unique)
}

func (a *App) MongoDropIndex(sessionID string, dbName string, collection string, name string) error {
	ms, err := a.mongoSession(sessionID)
	if err != nil {
		return err
	}
	return ms.DropIndex(dbName, collection, name)
}

// ── Elasticsearch methods ──

func (a *App) EsPing(sessionID string) error {
	es, err := a.esSession(sessionID)
	if err != nil {
		return err
	}
	return es.Ping()
}

func (a *App) EsClusterInfo(sessionID string) (*session.EsClusterInfo, error) {
	es, err := a.esSession(sessionID)
	if err != nil {
		return nil, err
	}
	return es.ClusterInfo()
}

func (a *App) EsClusterHealth(sessionID string) (*session.EsClusterHealth, error) {
	es, err := a.esSession(sessionID)
	if err != nil {
		return nil, err
	}
	return es.ClusterHealth()
}

func (a *App) EsNodesStats(sessionID string) ([]session.EsNodeSummary, error) {
	es, err := a.esSession(sessionID)
	if err != nil {
		return nil, err
	}
	return es.NodesStats()
}

func (a *App) EsListIndices(sessionID string) ([]session.EsIndexInfo, error) {
	es, err := a.esSession(sessionID)
	if err != nil {
		return nil, err
	}
	return es.ListIndices()
}

func (a *App) EsGetMapping(sessionID string, index string) (string, error) {
	es, err := a.esSession(sessionID)
	if err != nil {
		return "", err
	}
	return es.GetMapping(index)
}

func (a *App) EsGetSettings(sessionID string, index string) (string, error) {
	es, err := a.esSession(sessionID)
	if err != nil {
		return "", err
	}
	return es.GetSettings(index)
}

func (a *App) EsSearch(sessionID string, index string, bodyJSON string, from int, size int) (*session.EsSearchResult, error) {
	es, err := a.esSession(sessionID)
	if err != nil {
		return nil, err
	}
	return es.Search(index, bodyJSON, from, size)
}

func (a *App) EsGetDoc(sessionID string, index string, id string) (string, error) {
	es, err := a.esSession(sessionID)
	if err != nil {
		return "", err
	}
	return es.GetDoc(index, id)
}

func (a *App) EsIndexDoc(sessionID string, index string, id string, docJSON string) (string, error) {
	es, err := a.esSession(sessionID)
	if err != nil {
		return "", err
	}
	return es.IndexDoc(index, id, docJSON)
}

func (a *App) EsUpdateDoc(sessionID string, index string, id string, docJSON string) error {
	es, err := a.esSession(sessionID)
	if err != nil {
		return err
	}
	return es.UpdateDoc(index, id, docJSON)
}

func (a *App) EsDeleteDoc(sessionID string, index string, id string) error {
	es, err := a.esSession(sessionID)
	if err != nil {
		return err
	}
	return es.DeleteDoc(index, id)
}

func (a *App) EsCreateIndex(sessionID string, index string, bodyJSON string) error {
	es, err := a.esSession(sessionID)
	if err != nil {
		return err
	}
	return es.CreateIndex(index, bodyJSON)
}

func (a *App) EsDeleteIndex(sessionID string, index string) error {
	es, err := a.esSession(sessionID)
	if err != nil {
		return err
	}
	return es.DeleteIndex(index)
}

func (a *App) EsOpenIndex(sessionID string, index string) error {
	es, err := a.esSession(sessionID)
	if err != nil {
		return err
	}
	return es.OpenIndex(index)
}

func (a *App) EsCloseIndex(sessionID string, index string) error {
	es, err := a.esSession(sessionID)
	if err != nil {
		return err
	}
	return es.CloseIndex(index)
}

func (a *App) EsRest(sessionID string, method string, path string, bodyJSON string) (*session.EsRestResult, error) {
	es, err := a.esSession(sessionID)
	if err != nil {
		return nil, err
	}
	return es.Rest(method, path, bodyJSON)
}

func (a *App) GetDatabases(sessionID string) ([]string, error) {
	ds, p, err := a.dbProvider(sessionID)
	if err != nil {
		return nil, err
	}
	dbs, err := p.GetDatabases(ds.DB())
	if err != nil {
		log.Writef("[GetDatabases] failed: %v", err)
	}
	return dbs, err
}

func (a *App) GetTables(sessionID string, dbName string) ([]database.TableInfo, error) {
	ds, p, err := a.dbProvider(sessionID)
	if err != nil {
		return nil, err
	}
	tables, err := p.GetTables(ds.DB(), dbName)
	if err != nil {
		log.Writef("[GetTables] failed: %v", err)
		return nil, err
	}
	sort.Slice(tables, func(i, j int) bool {
		return tables[i].Name < tables[j].Name
	})
	return tables, nil
}

func (a *App) GetTableSchema(sessionID string, dbName string, tableName string) (*database.SchemaResult, error) {
	ds, p, err := a.dbProvider(sessionID)
	if err != nil {
		return nil, err
	}
	return p.GetTableSchema(ds.DB(), dbName, tableName)
}

func (a *App) CreateDatabase(sessionID string, dbName string) error {
	ds, p, err := a.dbProvider(sessionID)
	if err != nil {
		return err
	}
	return p.CreateDatabase(ds.DB(), dbName)
}

func (a *App) DropDatabase(sessionID string, dbName string) error {
	ds, p, err := a.dbProvider(sessionID)
	if err != nil {
		return err
	}
	return p.DropDatabase(ds.DB(), dbName)
}

func (a *App) CreateTable(sessionID string, dbName string, tableName string) error {
	ds, p, err := a.dbProvider(sessionID)
	if err != nil {
		return err
	}
	return p.CreateTable(ds.DB(), dbName, tableName)
}

func (a *App) DropTable(sessionID string, dbName string, tableName string) error {
	ds, p, err := a.dbProvider(sessionID)
	if err != nil {
		return err
	}
	return p.DropTable(ds.DB(), dbName, tableName)
}

func (a *App) DropView(sessionID string, dbName string, viewName string) error {
	ds, p, err := a.dbProvider(sessionID)
	if err != nil {
		return err
	}
	return p.DropView(ds.DB(), dbName, viewName)
}

func (a *App) TruncateTable(sessionID string, dbName string, tableName string) error {
	ds, p, err := a.dbProvider(sessionID)
	if err != nil {
		return err
	}
	return p.TruncateTable(ds.DB(), dbName, tableName)
}

func (a *App) ExecuteQuery(sessionID string, dbName string, sql string) (*database.QueryResult, error) {
	ds, p, err := a.dbProvider(sessionID)
	if err != nil {
		return nil, err
	}
	return database.ExecuteQuery(p, ds.DB(), dbName, sql)
}

func (a *App) ExecuteStatement(sessionID string, dbName string, sql string) (*database.ExecResult, error) {
	ds, p, err := a.dbProvider(sessionID)
	if err != nil {
		return nil, err
	}
	return database.ExecuteStatement(p, ds.DB(), dbName, sql)
}

func (a *App) DBDefaultTableQuery(sessionID string, dbName string, tableName string, limit int, offset int) (string, error) {
	_, p, err := a.dbProvider(sessionID)
	if err != nil {
		return "", err
	}
	if limit <= 0 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	return p.DefaultTableQuery(dbName, tableName, limit, offset), nil
}

func (a *App) DBPagedTableQuery(sessionID string, dbName string, tableName string, limit int, offset int) (string, error) {
	_, p, err := a.dbProvider(sessionID)
	if err != nil {
		return "", err
	}
	return p.PagedTableQuery(dbName, tableName, limit, offset), nil
}

func (a *App) ExecuteSQLScript(sessionID string, dbName string, script string) (*database.ScriptResult, error) {
	ds, p, err := a.dbProvider(sessionID)
	if err != nil {
		return nil, err
	}
	return database.ExecuteScript(p, ds.DB(), dbName, script)
}

func (a *App) DumpTable(sessionID string, dbName string, tableName string, withStructure bool, withData bool) (string, error) {
	ds, p, err := a.dbProvider(sessionID)
	if err != nil {
		return "", err
	}
	return p.DumpTable(ds.DB(), dbName, tableName, database.DumpOptions{Structure: withStructure, Data: withData})
}

func (a *App) DBInsertRow(sessionID string, dbName string, tableName string, values map[string]any) error {
	ds, p, err := a.dbProvider(sessionID)
	if err != nil {
		return err
	}
	return p.InsertRow(ds.DB(), dbName, tableName, values)
}

func (a *App) DBUpdateRow(sessionID string, dbName string, tableName string, set map[string]any, where map[string]any) error {
	ds, p, err := a.dbProvider(sessionID)
	if err != nil {
		return err
	}
	return p.UpdateRow(ds.DB(), dbName, tableName, set, where)
}

func (a *App) DBDeleteRow(sessionID string, dbName string, tableName string, where map[string]any) error {
	ds, p, err := a.dbProvider(sessionID)
	if err != nil {
		return err
	}
	return p.DeleteRow(ds.DB(), dbName, tableName, where)
}

func (a *App) AddColumn(sessionID string, dbName string, tableName string, col database.ColumnDef) error {
	ds, p, err := a.dbProvider(sessionID)
	if err != nil {
		return err
	}
	return p.AddColumn(ds.DB(), dbName, tableName, col)
}

func (a *App) ModifyColumn(sessionID string, dbName string, tableName string, col database.ColumnDef) error {
	ds, p, err := a.dbProvider(sessionID)
	if err != nil {
		return err
	}
	return p.ModifyColumn(ds.DB(), dbName, tableName, col)
}

func (a *App) DropColumn(sessionID string, dbName string, tableName string, colName string) error {
	ds, p, err := a.dbProvider(sessionID)
	if err != nil {
		return err
	}
	return p.DropColumn(ds.DB(), dbName, tableName, colName)
}

func (a *App) AddIndex(sessionID string, dbName string, tableName string, idx database.IndexDef) error {
	ds, p, err := a.dbProvider(sessionID)
	if err != nil {
		return err
	}
	return p.AddIndex(ds.DB(), dbName, tableName, idx)
}

func (a *App) DropIndexOp(sessionID string, dbName string, tableName string, idxName string, isPrimary bool, autoIncCols []string) error {
	ds, p, err := a.dbProvider(sessionID)
	if err != nil {
		return err
	}
	return p.DropIndex(ds.DB(), dbName, tableName, idxName, isPrimary, autoIncCols)
}

func (a *App) GetDBCapabilities(sessionID string) (database.DBCapabilities, error) {
	_, p, err := a.dbProvider(sessionID)
	if err != nil {
		return nil, err
	}
	return database.MergeCapabilities(p.GetCapabilities()), nil
}

// ─── Kubernetes ────────────────────────────────────────────────

// K8sContextInfo 是前端可见的 context 元信息。
type K8sContextInfo = k8s.ContextInfo

// K8sListContexts 解析给定 kubeconfig 内容并返回 context 列表。
// source 为文件路径或 YAML 内联字符串；根据 sourceIsPath 区分。
func (a *App) K8sListContexts(source string, sourceIsPath bool) ([]K8sContextInfo, error) {
	raw, err := readKubeconfigSource(source, sourceIsPath)
	if err != nil {
		return nil, err
	}
	return a.k8sManager.ListContexts(raw)
}

// K8sConnect 建立到 kubeconfig 中指定 context 的连接，返回 connID。
// 若 tunnelSSHConnID 非空，会先起一条 SSH 隧道，把到 apiserver 的 TCP 拨号
// 劫持到本地 loopback；TLS 校验仍按 kubeconfig 里的原 hostname 走。
func (a *App) K8sConnect(source string, sourceIsPath bool, contextName string,
	tunnelSSHConnID, tunnelSSHUser, tunnelSSHPassword string) (string, error) {
	raw, err := readKubeconfigSource(source, sourceIsPath)
	if err != nil {
		return "", err
	}

	if tunnelSSHConnID == "" {
		return a.k8sManager.Connect(a.ctx, raw, contextName)
	}

	// ── SSH Tunnel for K8s (reuses the shared jump-host tunnel logic) ──
	// 从 kubeconfig 解出 apiserver host/port 作为隧道目标
	kc, err := k8s.ParseBytes(raw)
	if err != nil {
		return "", fmt.Errorf("kubeconfig: %w", err)
	}
	ctxName := contextName
	if ctxName == "" {
		ctxName = kc.CurrentContext
	}
	ctxEntry, ok := kc.Contexts[ctxName]
	if !ok {
		return "", fmt.Errorf("context %q not found", ctxName)
	}
	cluster, ok := kc.Clusters[ctxEntry.Cluster]
	if !ok {
		return "", fmt.Errorf("cluster %q not found", ctxEntry.Cluster)
	}
	targetHost, targetPort, err := k8s.ParseServerAddr(cluster.Server)
	if err != nil {
		return "", fmt.Errorf("parse apiserver url: %w", err)
	}

	// 用同一个 key 既做 K8s connID 也做 TunnelService 的 sessionID，
	// 方便 Disconnect 时的 onClose 回调直接 Stop 同名隧道。
	tunnelKey := uuid.New().String()
	tunnelConfig := session.ConnectionConfig{
		TunnelSSHConnID:   tunnelSSHConnID,
		TunnelSSHUser:     tunnelSSHUser,
		TunnelSSHPassword: tunnelSSHPassword,
		Host:              targetHost,
		Port:              targetPort,
	}
	// 与其它连接类型共享同一段隧道逻辑：按认证类型解析跳板机凭据并拉起隧道。
	if err := a.setupJumpHostTunnel(tunnelKey, "k8s", &tunnelConfig); err != nil {
		return "", err
	}
	localPort := tunnelConfig.Port

	var dialer net.Dialer
	dialOverride := func(ctx context.Context, _ /*network*/, _ /*addr*/ string) (net.Conn, error) {
		return dialer.DialContext(ctx, "tcp", k8s.LocalAddr(localPort))
	}
	onClose := func() {
		a.tunnelService.Stop(tunnelKey)
	}

	connID, err := a.k8sManager.ConnectWith(raw, contextName, k8s.ConnectOptions{
		PresetID:     tunnelKey,
		DialOverride: dialOverride,
		OnClose:      onClose,
	})
	if err != nil {
		a.tunnelService.Stop(tunnelKey)
		return "", err
	}
	return connID, nil
}

func (a *App) K8sDisconnect(connID string) {
	a.k8sManager.Disconnect(connID)
}

// K8sResponse 是前端可见的 REST 响应。Body 为 JSON 原文字符串。
type K8sResponse struct {
	Status int    `json:"status"`
	Body   string `json:"body"`
}

func (a *App) K8sRequest(connID, method, path, body, contentType string) (K8sResponse, error) {
	status, out, err := a.k8sManager.Request(a.ctx, connID, method, path, []byte(body), contentType)
	if err != nil {
		return K8sResponse{}, err
	}
	return K8sResponse{Status: status, Body: string(out)}, nil
}

func (a *App) K8sStartWatch(connID, path string) (string, error) {
	return a.k8sManager.StartWatch(connID, path)
}

func (a *App) K8sStopWatch(watchID string) {
	a.k8sManager.StopWatch(watchID)
}

func (a *App) K8sStartLogStream(connID, namespace, pod, container string, tailLines int, timestamps, previous bool) (string, error) {
	return a.k8sManager.StartLogStream(connID, namespace, pod, container, tailLines, timestamps, previous)
}

func (a *App) K8sStopLogStream(streamID string) {
	a.k8sManager.StopLogStream(streamID)
}

func (a *App) K8sExecSession(connID, namespace, pod, container string) (*session.SessionInfo, error) {
	if a.k8sManager == nil {
		return nil, fmt.Errorf("k8s manager not initialized")
	}
	// initial size fallback; real size arrives via Resize after the frontend mounts xterm
	wsConn, err := a.k8sManager.DialExec(connID, namespace, pod, container, 80, 24)
	if err != nil {
		return nil, err
	}
	id := uuid.New().String()
	sess := session.NewK8sExecSession(id, wsConn)
	sess.SetOnDataCallback(func(data []byte) {
		a.emit("session:data", map[string]interface{}{
			"id":   sess.ID(),
			"data": string(data),
		})
	})
	sess.SetOnStatusChangeCallback(func(status session.SessionStatus) {
		a.emit("session:status", map[string]interface{}{
			"id":     sess.ID(),
			"status": status,
		})
	})
	a.sessionManager.Add(sess)
	return &session.SessionInfo{ID: id, Type: "k8s-exec", Title: pod, Status: session.StatusConnected}, nil
}

func readKubeconfigSource(source string, sourceIsPath bool) ([]byte, error) {
	if !sourceIsPath {
		return []byte(source), nil
	}
	if len(source) > 1 && source[0] == '~' {
		home, err := os.UserHomeDir()
		if err == nil {
			source = filepath.Join(home, source[1:])
		}
	}
	return os.ReadFile(source)
}

// ─── Container ─────────────────────────────────────────────────

// ContainerConnect 打开容器连接：解析配置、按 transport 建 Local 或 SSH runner。
// SSH 传输时若被引用连接配了跳板机（TunnelSSHConnID），先起本地转发隧道，
// 与 CreateSession 的单层隧道行为一致。
func (a *App) ContainerConnect(connectionID string) error {
	if a.containerManager == nil || a.connectionStore == nil {
		return fmt.Errorf("container manager not initialized")
	}
	data, err := a.connectionStore.Load()
	if err != nil {
		return err
	}
	var cfg *session.ConnectionConfig
	for _, c := range data.Connections {
		if c.ID == connectionID {
			cfg = &c
			break
		}
	}
	if cfg == nil {
		return fmt.Errorf("connection not found: %s", connectionID)
	}
	if cfg.Type != "container" {
		return fmt.Errorf("connection %s is not a container connection", connectionID)
	}
	rt := container.Runtime(cfg.ContainerRuntime)

	if cfg.ContainerTransport == "local" {
		return a.containerManager.ConnectLocal(connectionID, rt, "")
	}

	var sshCfg *session.ConnectionConfig
	for _, c := range data.Connections {
		if c.ID == cfg.ContainerSSHConnID {
			sshCfg = &c
			break
		}
	}
	if sshCfg == nil {
		return fmt.Errorf("referenced SSH connection missing: %s", cfg.ContainerSSHConnID)
	}

	// Resolve an identity reference into a concrete password/key config
	// before handing the SSH runner its connection config.
	if sshCfg.AuthType == "identity" {
		m, err := a.materializeIdentity(*sshCfg)
		if err != nil {
			return err
		}
		sshCfg = &m
	}

	// 跳板机：复用统一的 setupJumpHostTunnel（与其它连接类型同一段逻辑）。
	// 它会按认证类型解析被引用跳板机连接的凭据并拉起隧道，同时改写
	// sshCfg.Host/Port 指向本地监听口。
	hasTunnel := sshCfg.TunnelSSHConnID != ""
	if hasTunnel {
		if err := a.setupJumpHostTunnel(connectionID, "container", sshCfg); err != nil {
			return err
		}
	}
	if err := a.containerManager.ConnectSSH(connectionID, rt, "", *sshCfg); err != nil {
		if hasTunnel && a.tunnelService != nil {
			a.tunnelService.Stop(connectionID) // 与其它连接一致：连接失败时回收隧道
		}
		return err
	}
	return nil
}

func (a *App) ContainerDisconnect(connectionID string) {
	a.containerManager.Disconnect(connectionID)
	if a.tunnelService != nil {
		a.tunnelService.Stop(connectionID) // 无同名隧道时为 no-op
	}
}

// X11DesktopConnect starts an x11-desktop session: looks up the saved
// connection config (which carries its own SSH credentials), opens an
// SSH connection with X11 forwarding, and runs the chosen desktop
// command on the remote host. connectionID and sessionID are distinct
// UUIDs (the connection is the user's saved record; the session is the
// live runtime object created via CreateSession).
func (a *App) X11DesktopConnect(connectionID string, sessionID string) error {
	if a.connectionStore == nil || a.sessionManager == nil {
		return fmt.Errorf("connection store or session manager not initialized")
	}
	data, err := a.connectionStore.Load()
	if err != nil {
		return err
	}
	var cfg *session.ConnectionConfig
	for i := range data.Connections {
		if data.Connections[i].ID == connectionID {
			cfg = &data.Connections[i]
			break
		}
	}
	if cfg == nil {
		return fmt.Errorf("connection not found: %s", connectionID)
	}
	if cfg.Type != "x11-desktop" {
		return fmt.Errorf("connection %s is not an x11-desktop", connectionID)
	}

	// Resolve an identity reference into a concrete password/key config
	// before handing the connection to the X11 dialer.
	if cfg.AuthType == "identity" {
		m, err := a.materializeIdentity(*cfg)
		if err != nil {
			return err
		}
		cfg = &m
	}

	sess, ok := a.sessionManager.Get(sessionID)
	if !ok {
		return fmt.Errorf("session not found: %s", sessionID)
	}
	x11Sess, ok := sess.(*session.X11DesktopSession)
	if !ok {
		return fmt.Errorf("session %s is not x11-desktop", sessionID)
	}
	if err := x11Sess.ConnectX11Desktop(*cfg); err != nil {
		return err
	}
	return nil
}

func (a *App) ContainerList(connectionID string) ([]container.Container, error) {
	p, err := a.containerManager.Provider(connectionID)
	if err != nil {
		return nil, err
	}
	return p.List(a.ctx)
}

func (a *App) ContainerInspect(connectionID, containerID string) (container.InspectResult, error) {
	p, err := a.containerManager.Provider(connectionID)
	if err != nil {
		return container.InspectResult{}, err
	}
	return p.Inspect(a.ctx, containerID)
}

func (a *App) ContainerAction(connectionID, containerID, action string) error {
	p, err := a.containerManager.Provider(connectionID)
	if err != nil {
		return err
	}
	return p.Action(a.ctx, containerID, action)
}

func (a *App) ContainerRename(connectionID, containerID, newName string) error {
	p, err := a.containerManager.Provider(connectionID)
	if err != nil {
		return err
	}
	return p.Rename(a.ctx, containerID, newName)
}

func (a *App) ContainerStats(connectionID string) ([]container.Stats, error) {
	p, err := a.containerManager.Provider(connectionID)
	if err != nil {
		return nil, err
	}
	return p.Stats(a.ctx)
}

func (a *App) ContainerImages(connectionID string) ([]container.Image, error) {
	p, err := a.containerManager.Provider(connectionID)
	if err != nil {
		return nil, err
	}
	return p.Images(a.ctx)
}

func (a *App) ContainerRemoveImage(connectionID, imageID string) error {
	p, err := a.containerManager.Provider(connectionID)
	if err != nil {
		return err
	}
	return p.RemoveImage(a.ctx, imageID)
}

func (a *App) ContainerCreate(connectionID string, opts container.CreateOptions) error {
	p, err := a.containerManager.Provider(connectionID)
	if err != nil {
		return err
	}
	return p.Create(a.ctx, opts)
}

func (a *App) ContainerNamespaces(connectionID string) ([]string, error) {
	p, err := a.containerManager.Provider(connectionID)
	if err != nil {
		return nil, err
	}
	return p.Namespaces(a.ctx)
}

func (a *App) ContainerSetNamespace(connectionID, ns string) error {
	return a.containerManager.SetNamespace(connectionID, ns)
}

func (a *App) ContainerStartLogs(connectionID, containerID string, tail int, timestamps bool) (string, error) {
	return a.containerManager.StartLogStream(connectionID, containerID, tail, timestamps)
}

func (a *App) ContainerStartPull(connectionID, image string) (string, error) {
	return a.containerManager.StartPullStream(connectionID, image)
}

func (a *App) ContainerStopStream(streamID string) {
	a.containerManager.StopStream(streamID)
}

func (a *App) ContainerExecSession(connectionID, containerID, shell string) (*session.SessionInfo, error) {
	if a.containerManager == nil {
		return nil, fmt.Errorf("container manager not initialized")
	}
	// initial size fallback; real size arrives via Resize after the frontend mounts xterm
	pty, err := a.containerManager.Exec(connectionID, containerID, shell, 80, 24)
	if err != nil {
		return nil, err
	}
	id := uuid.New().String()
	sess := session.NewContainerExecSession(id, pty)
	sess.SetOnDataCallback(func(data []byte) {
		a.emit("session:data", map[string]interface{}{
			"id":   sess.ID(),
			"data": string(data),
		})
	})
	sess.SetOnStatusChangeCallback(func(status session.SessionStatus) {
		a.emit("session:status", map[string]interface{}{
			"id":     sess.ID(),
			"status": status,
		})
	})
	a.sessionManager.Add(sess)
	return &session.SessionInfo{ID: id, Type: "container-exec", Title: containerID, Status: session.StatusConnected}, nil
}
