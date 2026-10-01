package container

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestLocalRunnerRun(t *testing.T) {
	r := NewLocalRunner()
	var out []byte
	var err error
	if runtime.GOOS == "windows" {
		out, err = r.Run(context.Background(), []string{"cmd", "/c", "echo", "hello"})
	} else {
		out, err = r.Run(context.Background(), []string{"echo", "hello"})
	}
	if err != nil {
		t.Fatal(err)
	}
	if string(out) == "" {
		t.Fatal("empty output")
	}
}

// 失败且 stderr 为空时，错误文本退回 stdout（wslc 的 exec 失败即如此），
// 上层才能据此给出「镜像内没有 shell」这类可读提示。
func TestLocalRunnerErrorFallsBackToStdout(t *testing.T) {
	r := NewLocalRunner()
	var argv []string
	if runtime.GOOS == "windows" {
		argv = []string{"cmd", "/c", "echo boom && exit 7"}
	} else {
		argv = []string{"sh", "-c", "echo boom && exit 7"}
	}
	_, err := r.Run(context.Background(), argv)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error should carry stdout text, got %v", err)
	}
}

func TestResolveBinaryNotFound(t *testing.T) {
	if _, err := resolveBinary("definitely-not-exist-bin-xyz"); err == nil {
		t.Fatal("expected error")
	}
}

func TestLocalLineStreamWaitCloseNoDeadlock(t *testing.T) {
	r := NewLocalRunner()
	var argv []string
	if runtime.GOOS == "windows" {
		argv = []string{"cmd", "/c", "echo", "hi"}
	} else {
		argv = []string{"echo", "hi"}
	}
	stream, err := r.RunStream(context.Background(), argv)
	if err != nil {
		t.Fatal(err)
	}
	for range stream.Lines() {
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		err1 := stream.Wait()
		err2 := stream.Wait()
		if err1 != err2 {
			t.Errorf("Wait not idempotent: %v vs %v", err1, err2)
		}
		_ = stream.Close()
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("deadlock")
	}
}
