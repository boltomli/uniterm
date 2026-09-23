package session

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestConnectionConfigAgentForwardingJSON(t *testing.T) {
	b, err := json.Marshal(ConnectionConfig{AgentForwarding: true})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if !strings.Contains(string(b), `"agentForwarding":true`) {
		t.Fatalf("json.Marshal() = %s, want agentForwarding=true", b)
	}

	var cfg ConnectionConfig
	if err := json.Unmarshal([]byte(`{"agentForwarding":true}`), &cfg); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if !cfg.AgentForwarding {
		t.Fatal("json.Unmarshal() did not restore agentForwarding")
	}
}