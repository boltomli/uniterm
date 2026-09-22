//go:build !production

package main

// Dev builds lock under a distinct ID so they never contend with an installed
// release binary holding the release lock. With the shared release ID the dev
// process would detect the release instance on startup, notify it and exit
// before wails3 dev could attach to it.
const singleInstanceID = "uniterm-gui-dev"
