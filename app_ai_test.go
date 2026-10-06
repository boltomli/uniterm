package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestLLMProxyEnvFallback verifies that llmProxy resolves proxies declared
// via the HTTP_PROXY/HTTPS_PROXY environment variables (go-ieproxy prefers
// explicit env vars over the OS system proxy config).
//
// Direct-connection and system-proxy branches are intentionally not unit
// tested: they depend on the host's real system proxy state and on net/http's
// process-wide env-proxy cache, so they cannot be made hermetic.
func TestLLMProxyEnvFallback(t *testing.T) {
	t.Setenv("HTTP_PROXY", "127.0.0.1:7890")
	t.Setenv("HTTPS_PROXY", "127.0.0.1:7891")
	t.Setenv("NO_PROXY", "")

	// Force a fresh resolution so the cached func sees the test env.
	llmProxyMu.Lock()
	llmProxyFn = nil
	llmProxyMu.Unlock()

	req, err := http.NewRequest("GET", "https://api.openai.com/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	u, err := llmProxy(req)
	if err != nil {
		t.Fatal(err)
	}
	if u == nil || u.Host != "127.0.0.1:7891" {
		t.Fatalf("want https proxy 127.0.0.1:7891, got %v", u)
	}

	req, err = http.NewRequest("GET", "http://example.com/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	u, err = llmProxy(req)
	if err != nil {
		t.Fatal(err)
	}
	if u == nil || u.Host != "127.0.0.1:7890" {
		t.Fatalf("want http proxy 127.0.0.1:7890, got %v", u)
	}
}

// sseContentType is the media type SSE endpoints answer with.
const sseContentType = "text/event-stream"

// TestChatCompletionOpenAI_ReasoningContent verifies that delta.reasoning_content
// chunks from OpenAI-compatible reasoning models (DeepSeek-R1, QwQ, ...) are
// accumulated into a thinking block that survives in the final message, with
// the text block following it, so the frontend can persist and display it.
func TestChatCompletionOpenAI_ReasoningContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", sseContentType)
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"Let me think\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\" about this.\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"The answer\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\" is 42.\"},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	reqBody := map[string]interface{}{
		"max_tokens": 1024,
		"messages":   []interface{}{map[string]interface{}{"role": "user", "content": "hi"}},
	}
	result, err := (&App{}).chatCompletionOpenAI("k", srv.URL, "deepseek-r1", reqBody, "test", srv.Client())
	if err != nil {
		t.Fatal(err)
	}

	var msg struct {
		Role    string `json:"role"`
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Thinking string `json:"thinking"`
		} `json:"content"`
	}
	if err := json.Unmarshal([]byte(result), &msg); err != nil {
		t.Fatalf("unmarshal %q: %v", result, err)
	}
	if len(msg.Content) != 2 {
		t.Fatalf("want 2 blocks, got %d: %s", len(msg.Content), result)
	}
	if msg.Content[0].Type != "thinking" || msg.Content[0].Thinking != "Let me think about this." {
		t.Fatalf("thinking block wrong: %+v", msg.Content[0])
	}
	if msg.Content[1].Type != "text" || msg.Content[1].Text != "The answer is 42." {
		t.Fatalf("text block wrong: %+v", msg.Content[1])
	}
}

// TestChatCompletionAnthropic_ThinkingBlocks verifies extended-thinking
// support: thinking_delta chunks accumulate into the thinking block, the
// signature_delta is preserved (required when replaying the block back to
// the API), and interleaved text blocks still arrive after it.
func TestChatCompletionAnthropic_ThinkingBlocks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", sseContentType)
		fmt.Fprint(w, "data: {\"type\":\"message_start\",\"message\":{\"role\":\"assistant\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"Hmm, \"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"let me see.\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"sig123\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"42.\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"content_block_stop\",\"index\":1}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"message_stop\"}\n\n")
	}))
	defer srv.Close()

	reqBody := map[string]interface{}{
		"max_tokens": 1024,
		"messages":   []interface{}{map[string]interface{}{"role": "user", "content": "hi"}},
	}
	result, err := (&App{}).chatCompletionAnthropic("k", srv.URL, "claude", reqBody, "test", srv.Client())
	if err != nil {
		t.Fatal(err)
	}

	var msg struct {
		Content []struct {
			Type      string `json:"type"`
			Text      string `json:"text"`
			Thinking  string `json:"thinking"`
			Signature string `json:"signature"`
		} `json:"content"`
	}
	if err := json.Unmarshal([]byte(result), &msg); err != nil {
		t.Fatalf("unmarshal %q: %v", result, err)
	}
	if len(msg.Content) != 2 {
		t.Fatalf("want 2 blocks, got %d: %s", len(msg.Content), result)
	}
	if msg.Content[0].Type != "thinking" || msg.Content[0].Thinking != "Hmm, let me see." {
		t.Fatalf("thinking block wrong: %+v", msg.Content[0])
	}
	if msg.Content[0].Signature != "sig123" {
		t.Fatalf("signature not preserved: %+v", msg.Content[0])
	}
	if msg.Content[1].Type != "text" || msg.Content[1].Text != "42." {
		t.Fatalf("text block wrong: %+v", msg.Content[1])
	}
}
