//go:build production

package main

// singleInstanceID is the single-instance lock key for release builds.
// Dev builds (no production tag, i.e. wails3 dev / plain go build) derive
// theirs from a separate file so a running installed release never swallows
// the dev process — see singleinstance_dev.go.
const singleInstanceID = "uniterm-gui"
