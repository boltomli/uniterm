package main

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/ys-ll/uniterm/backend/store"
)

// testCipher mirrors the store tests' fakeCipherStore: an "always
// encrypted" PasswordStore so the model keys are real "secrets" for the
// save path (a bare nil store refuses to persist non-empty keys).
type testCipher struct{}

func (testCipher) Encrypt(plaintext string) (string, error) {
	return "enc:v1:" + base64.StdEncoding.EncodeToString([]byte(plaintext)), nil
}
func (testCipher) Decrypt(encoded string) (string, error) {
	if !strings.HasPrefix(encoded, "enc:v1:") {
		return encoded, nil
	}
	b, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(encoded, "enc:v1:"))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ── F-09a: API keys stay Go-side ────────────────────────────────────────────

func TestMaskAndPreserveAIModelKeys(t *testing.T) {
	stored := []store.AIModelConfig{
		{ID: "m1", Name: "one", APIKey: "sk-real"},
		{ID: "m2", Name: "two", APIKey: ""},
	}

	masked := maskAIModelKeys(stored)
	if masked[0].APIKey != MaskedAPIKey || masked[1].APIKey != "" {
		t.Fatalf("maskAIModelKeys = %+v", masked)
	}
	// The input slice must not be mutated (Load may share backing arrays).
	if stored[0].APIKey != "sk-real" {
		t.Fatalf("maskAIModelKeys mutated its input: %+v", stored)
	}

	// Masked and empty both mean "unchanged": the stored key survives.
	in := []store.AIModelConfig{
		{ID: "m1", Name: "one", APIKey: MaskedAPIKey},
		{ID: "m1", Name: "one-new", APIKey: "sk-new"}, // explicit replacement
		{ID: "m2", Name: "two", APIKey: MaskedAPIKey}, // stored empty → stays empty
		{ID: "m3", Name: "brand-new", APIKey: ""},     // new model, nothing stored
	}
	merged := preserveAIModelKeys(in, stored)
	if merged[0].APIKey != "sk-real" {
		t.Fatalf("masked key not restored: %+v", merged[0])
	}
	if merged[1].APIKey != "sk-new" {
		t.Fatalf("typed key must replace the stored one: %+v", merged[1])
	}
	if merged[2].APIKey != "" {
		t.Fatalf("empty stored key restored as %q", merged[2].APIKey)
	}
	if merged[3].APIKey != "" {
		t.Fatalf("brand-new model key = %q, want empty", merged[3].APIKey)
	}
}

// The LoadSettings binding masks keys on the way out; the on-disk copy keeps
// the plaintext.
func TestLoadSettingsMasksAPIKeys(t *testing.T) {
	dir := t.TempDir()
	ss, err := store.NewSettingsStore(dir)
	if err != nil {
		t.Fatalf("settings store: %v", err)
	}
	ss.SetPasswordStore(testCipher{})
	st, err := ss.Load()
	if err != nil {
		t.Fatalf("Load defaults: %v", err)
	}
	st.AI.Models = []store.AIModelConfig{{ID: "m1", Name: "one", APIKey: "sk-real", BaseURL: "https://x", Model: "m", Protocol: "openai"}}
	if err := ss.Save(st); err != nil {
		t.Fatalf("seed save: %v", err)
	}

	a := &App{settingsStore: ss}
	got, err := a.LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	if got.AI.Models[0].APIKey != MaskedAPIKey {
		t.Fatalf("renderer-visible key = %q, want %q", got.AI.Models[0].APIKey, MaskedAPIKey)
	}

	// On-disk copy still holds the plaintext key.
	raw, err := ss.Load()
	if err != nil {
		t.Fatalf("raw Load: %v", err)
	}
	if raw.AI.Models[0].APIKey != "sk-real" {
		t.Fatalf("stored key = %q, want plaintext preserved", raw.AI.Models[0].APIKey)
	}
}

// A settings save coming from the renderer (masked keys) must never wipe or
// persist the mask.
func TestSaveSettingsPreservesStoredAPIKeys(t *testing.T) {
	dir := t.TempDir()
	ss, err := store.NewSettingsStore(dir)
	if err != nil {
		t.Fatalf("settings store: %v", err)
	}
	ss.SetPasswordStore(testCipher{})
	st, err := ss.Load()
	if err != nil {
		t.Fatalf("Load defaults: %v", err)
	}
	st.AI.Models = []store.AIModelConfig{{ID: "m1", Name: "one", APIKey: "sk-real", BaseURL: "https://x", Model: "m", Protocol: "openai"}}
	if err := ss.Save(st); err != nil {
		t.Fatalf("seed save: %v", err)
	}
	a := &App{settingsStore: ss}

	// Round-trip through the renderer: masked key comes back → stored key kept.
	seen, err := a.LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	if err := a.SaveSettings(seen); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	raw, _ := ss.Load()
	if raw.AI.Models[0].APIKey != "sk-real" {
		t.Fatalf("masked round-trip wiped the key: %q", raw.AI.Models[0].APIKey)
	}

	// An explicitly typed new key replaces the stored one.
	seen.AI.Models[0].APIKey = "sk-typed"
	if err := a.SaveSettings(seen); err != nil {
		t.Fatalf("SaveSettings(new key): %v", err)
	}
	raw, _ = ss.Load()
	if raw.AI.Models[0].APIKey != "sk-typed" {
		t.Fatalf("typed key not stored: %q", raw.AI.Models[0].APIKey)
	}

	// An empty key also means "unchanged" — saves can never wipe keys.
	seen.AI.Models[0].APIKey = ""
	if err := a.SaveSettings(seen); err != nil {
		t.Fatalf("SaveSettings(empty key): %v", err)
	}
	raw, _ = ss.Load()
	if raw.AI.Models[0].APIKey != "sk-typed" {
		t.Fatalf("empty key wiped the stored one: %q", raw.AI.Models[0].APIKey)
	}
}

func TestLoadSaveAIModelsMaskAndPreserve(t *testing.T) {
	dir := t.TempDir()
	acs, err := store.NewAIConfigStore(dir)
	if err != nil {
		t.Fatalf("ai config store: %v", err)
	}
	acs.SetPasswordStore(testCipher{})
	if err := acs.Save(store.AIStoreData{Models: []store.AIModelConfig{
		{ID: "m1", Name: "one", APIKey: "sk-real", BaseURL: "https://x", Model: "m", Protocol: "openai"},
	}}); err != nil {
		t.Fatalf("seed ai.json: %v", err)
	}
	a := &App{aiConfigStore: acs}

	got, err := a.LoadAIModels()
	if err != nil {
		t.Fatalf("LoadAIModels: %v", err)
	}
	if got.Models[0].APIKey != MaskedAPIKey {
		t.Fatalf("renderer-visible ai.json key = %q, want %q", got.Models[0].APIKey, MaskedAPIKey)
	}

	if err := a.SaveAIModels(got); err != nil {
		t.Fatalf("SaveAIModels: %v", err)
	}
	raw, _ := acs.Load()
	if raw.Models[0].APIKey != "sk-real" {
		t.Fatalf("masked save wiped the ai.json key: %q", raw.Models[0].APIKey)
	}
}

func TestResolveAPIKey(t *testing.T) {
	dir := t.TempDir()
	ss, err := store.NewSettingsStore(dir)
	if err != nil {
		t.Fatalf("settings store: %v", err)
	}
	ss.SetPasswordStore(testCipher{})
	st, _ := ss.Load()
	st.AI.Models = []store.AIModelConfig{{ID: "m1", Name: "one", APIKey: "sk-real"}}
	if err := ss.Save(st); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := &App{settingsStore: ss}

	// A typed key wins verbatim (unsaved model tests).
	if k, err := a.resolveAPIKey("", "sk-typed"); err != nil || k != "sk-typed" {
		t.Fatalf("typed key = %q, %v", k, err)
	}
	// The masked marker resolves the stored key.
	if k, err := a.resolveAPIKey("m1", MaskedAPIKey); err != nil || k != "sk-real" {
		t.Fatalf("masked key = %q, %v", k, err)
	}
	// An empty key resolves too.
	if k, err := a.resolveAPIKey("m1", ""); err != nil || k != "sk-real" {
		t.Fatalf("empty key = %q, %v", k, err)
	}
	// Unknown model + mask → readable error, no fallback to some other key.
	if _, err := a.resolveAPIKey("nope", MaskedAPIKey); err == nil || !strings.Contains(err.Error(), "API key not configured") {
		t.Fatalf("unknown model: %v", err)
	}

	// ChatCompletion refuses before dialing when nothing is stored.
	if _, err := a.ChatCompletion("nope", MaskedAPIKey, "https://api.openai.com/v1", "m", "{}", "openai", "", "", "", ""); err == nil || !strings.Contains(err.Error(), "API key not configured") {
		t.Fatalf("ChatCompletion without a key: %v", err)
	}
	if _, err := a.FetchModels("nope", MaskedAPIKey, "https://api.openai.com/v1", "openai", ""); err == nil || !strings.Contains(err.Error(), "API key not configured") {
		t.Fatalf("FetchModels without a key: %v", err)
	}
}
