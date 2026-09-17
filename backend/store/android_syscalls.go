package store

import "os"

func androidTempDir() string { return os.TempDir() }

func mkdirAllAndroid(dir string) error { return os.MkdirAll(dir, 0o755) }
