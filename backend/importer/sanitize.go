package importer

import "github.com/ys-ll/uniterm/backend/session"

// SanitizeImported clears fields of a ConnectionConfig that execute without
// further UI confirmation when a connection is opened. An imported file is an
// untrusted boundary: post-login automation must not ride along silently.
// shellPath is intentionally kept (visible in the connection form; elevated
// spawning is constrained by the admin broker's shell allowlist).
func SanitizeImported(data *session.ConnectionStoreData) {
	if data == nil {
		return
	}
	for i := range data.Connections {
		c := &data.Connections[i]
		c.PostLoginScript = ""
		c.PostLoginExpectSteps = nil
		// The X11 desktop custom command is passed to sshd verbatim (it runs
		// through /bin/sh -c on the remote) — same silent-exec risk class as
		// the post-login script. Clearing it also makes a "custom" desktop
		// type fail safely at connect time ("custom command is empty").
		c.X11DesktopCustomCmd = ""
	}
	// data.Groups (session.ConnectionGroup: id/name/parentId only) carries no
	// execution fields, so that slice is left untouched.
}
