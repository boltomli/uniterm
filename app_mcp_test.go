package main

import (
	"fmt"
	"strings"
	"testing"
)

func TestMcpNotifyStrings(t *testing.T) {
	en := mcpNotifyStringsFor("en")
	zh := mcpNotifyStringsFor("zh-CN")
	if en.Title != "uniTerm · MCP approval request" {
		t.Errorf("en title = %q", en.Title)
	}
	if !strings.Contains(zh.BodyConnect, "请求建立新连接") {
		t.Errorf("zh connect template = %q", zh.BodyConnect)
	}
	if want := fmt.Sprintf(zh.BodyConnect, "cc"); mcpNotifyBody(zh, "cc", "") != want {
		t.Errorf("zh connect body = %q, want %q", mcpNotifyBody(zh, "cc", ""), want)
	}
	if got := mcpNotifyBody(en, "cc", "ls -la"); got != "cc: ls -la" {
		t.Errorf("short command preview = %q", got)
	}
	long := string(make([]byte, 100))
	if got := mcpNotifyBody(en, "cc", long); !strings.HasSuffix(got, "…") {
		t.Errorf("long command should end with ellipsis, got %q", got)
	}
	if mcpNotifyStringsFor("xx-unknown") != mcpNotifyLabels["en"] {
		t.Errorf("unknown language must fall back to English")
	}
}
