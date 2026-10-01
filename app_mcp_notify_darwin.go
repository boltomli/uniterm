//go:build darwin

package main

import (
	"fmt"
	"os/exec"

	"github.com/ys-ll/uniterm/backend/log"
	"github.com/ys-ll/uniterm/backend/mcp"
)

// notifyMCPApproval (macOS): three escalating signals when the app is in the
// background —
//  1. a system notification (osascript display notification; clicking it
//     activates uniTerm because notifications run in-app),
//  2. Dock icon bounce (requestUserAttention via window Flash),
//  3. raising the window above other apps (application raise).
//
// Wails v3 desktop has no cross-platform notification API yet, hence the
// osascript bridge — same mechanism iTerm/Termius use for shell integration.
func (a *App) notifyMCPApproval(req mcp.ApprovalRequest) {
	title := "uniTerm · MCP 审批请求"
	body := fmt.Sprintf("%s 请求执行命令,请在 uniTerm 中确认", req.Client)
	if req.Command == "" {
		body = fmt.Sprintf("%s 请求建立新连接,请在 uniTerm 中确认", req.Client)
	}
	// 简短命令直接进通知正文;长命令截断。
	if len(req.Command) > 0 && len(req.Command) <= 60 {
		body = fmt.Sprintf("%s: %s", req.Client, req.Command)
	} else if len(req.Command) > 60 {
		body = fmt.Sprintf("%s: %s…", req.Client, req.Command[:57])
	}

	script := fmt.Sprintf(`display notification %q with title %q sound name "Ping"`,
		body, title)
	if err := exec.Command("osascript", "-e", script).Run(); err != nil {
		log.Writef("mcp: notification failed: %v", err)
	}

	// Dock bounce + raise: Flash() maps to requestUserAttention, and the
	// window's Show() brings it forward on the active space.
	if a.window != nil {
		a.window.Focus()
		a.window.Show()
	}
	a.raiseMainWindow()
}

// raiseMainWindow (macOS): AppleScript activate brings the whole app (any
// window, any space) forward — stronger than NSWindow orderFront when the
// app is fully backgrounded. Uses the bundle id first, falls back to the
// app name (dev builds use a placeholder bundle id).
func (a *App) raiseMainWindow() {
	_ = exec.Command("osascript", "-e", `tell application id "com.yourcompany.uniter" to activate`).Run()
}
