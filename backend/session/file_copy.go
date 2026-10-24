package session

import "os"

// copyFile copies src to dst through the shared stream copier (which
// nil-guards the task, returns write errors, and honours cancellation).
// Cross-platform by design: sftp_session's symlink materialisation calls it
// on every OS, so it must NOT live in the windows-only wsl_file_session.go
// (it used to, which broke GOOS=linux/darwin builds).
func copyFile(src, dst string, task *TransferTask) error {
	if err := transferCtxErr(task); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	// The shared stream copy nil-guards the task (this loop used to panic on
	// waitIfPaused with the nil task WSL Copy/Move pass) and returns write
	// errors and cancellation instead of copying to the end.
	cpErr := copyDownloadStream(out, in, task, nil)
	closeErr := out.Close()
	if cpErr != nil {
		return cpErr
	}
	return closeErr
}
