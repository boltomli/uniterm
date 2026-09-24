package importer

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ys-ll/uniterm/backend/session"
)

func sampleStore() session.ConnectionStoreData {
	return session.ConnectionStoreData{
		Groups: []session.ConnectionGroup{{ID: "g1", Name: "prod"}},
		Connections: []session.ConnectionConfig{
			{ID: "c1", Name: "db", Type: "ssh", Host: "10.0.0.5", Port: 22, User: "root",
				AuthType: "password", Password: "hunter2", GroupId: strptr("g1")},
		},
	}
}

func TestExportUnitermWithoutPasswordClearsPassword(t *testing.T) {
	b, err := ExportUniterm(sampleStore(), "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	var f utmFile
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if f.Encrypted {
		t.Fatal("expected encrypted=false")
	}
	if f.Connections[0].Password != "" {
		t.Fatalf("password not cleared: %q", f.Connections[0].Password)
	}
}

func TestUnitermRoundTripWithPassword(t *testing.T) {
	b, err := ExportUniterm(sampleStore(), "pass123")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	res, err := parseUniterm(b, ParseOptions{Password: "pass123"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(res.Connections) != 1 || res.Connections[0].Password != "hunter2" {
		t.Fatalf("password not restored: %+v", res.Connections)
	}
	if res.Connections[0].ID == "c1" {
		t.Fatalf("connection id not regenerated")
	}
	if res.Groups[0].ID == "g1" {
		t.Fatalf("group id not regenerated")
	}
}

func TestUnitermEncryptedNoKDFFails(t *testing.T) {
	body := []byte(`{"format":"uniterm","version":1,"encrypted":true,"groups":[],"connections":[]}`)
	if _, err := parseUniterm(body, ParseOptions{Password: "pass"}); err == nil {
		t.Fatal("expected error for encrypted file missing kdf block")
	}
}

func TestExportUnitermEncryptsKeyPassphrase(t *testing.T) {
	data := session.ConnectionStoreData{
		Connections: []session.ConnectionConfig{
			{ID: "c1", Name: "db", Type: "ssh", Host: "h", Port: 22, User: "root",
				AuthType: "key", KeyPath: "/k", Password: "keypass"},
		},
	}
	b, err := ExportUniterm(data, "pass123")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	var f utmFile
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if p := f.Connections[0].Password; p == "keypass" || !strings.HasPrefix(p, "enc:v1:") {
		t.Fatalf("key passphrase not encrypted: %q", p)
	}
	res, err := parseUniterm(b, ParseOptions{Password: "pass123"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if res.Connections[0].Password != "keypass" {
		t.Fatalf("key passphrase not restored: %q", res.Connections[0].Password)
	}
}

func TestUnitermWrongPasswordFails(t *testing.T) {
	b, _ := ExportUniterm(sampleStore(), "pass123")
	if _, err := parseUniterm(b, ParseOptions{Password: "wrong"}); err == nil {
		t.Fatal("expected error for wrong password")
	}
	if _, err := parseUniterm(b, ParseOptions{}); err == nil {
		t.Fatal("expected error for missing password")
	}
}

// TestExportUnitermWithoutPasswordClearsK8sAndPostLoginScript: an unencrypted
// export must not carry the inline kubeconfig (cluster credentials) or the
// post-login script — both are cleared alongside the passwords.
func TestExportUnitermWithoutPasswordClearsK8sAndPostLoginScript(t *testing.T) {
	data := session.ConnectionStoreData{
		Connections: []session.ConnectionConfig{{
			ID: "c1", Name: "cluster", Type: "k8s", Host: "10.0.0.5", Port: 6443,
			AuthType: "password", Password: "hunter2",
			K8sConfigInline: "apiVersion: v1\nusers: [{name: admin, user: {token: kube-token-xyz}}]",
			PostLoginScript: "kubectl config use-context prod",
		}},
	}
	b, err := ExportUniterm(data, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	var f utmFile
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if f.Encrypted {
		t.Fatal("expected encrypted=false")
	}
	c := f.Connections[0]
	if c.Password != "" {
		t.Fatalf("password not cleared: %q", c.Password)
	}
	if c.K8sConfigInline != "" {
		t.Fatalf("K8sConfigInline not cleared: %q", c.K8sConfigInline)
	}
	if c.PostLoginScript != "" {
		t.Fatalf("PostLoginScript not cleared: %q", c.PostLoginScript)
	}
	if strings.Contains(string(b), "kube-token-xyz") || strings.Contains(string(b), "kubectl config use-context") {
		t.Fatalf("cleared secret still present in export:\n%s", b)
	}
}

// TestExportUnitermEncryptsK8sAndPostLoginScript: with a password both fields
// are encrypted in the export and restored by the import round trip.
func TestExportUnitermEncryptsK8sAndPostLoginScript(t *testing.T) {
	const kubeconfig = "apiVersion: v1\nusers: [{name: admin, user: {token: kube-token-xyz}}]"
	const script = "kubectl config use-context prod"
	data := session.ConnectionStoreData{
		Connections: []session.ConnectionConfig{{
			ID: "c1", Name: "cluster", Type: "k8s", Host: "10.0.0.5", Port: 6443,
			AuthType: "password", Password: "hunter2",
			K8sConfigInline: kubeconfig,
			PostLoginScript: script,
		}},
	}
	b, err := ExportUniterm(data, "pass123")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if strings.Contains(string(b), "kube-token-xyz") || strings.Contains(string(b), script) {
		t.Fatalf("plaintext secret in encrypted export:\n%s", b)
	}
	var f utmFile
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	c := f.Connections[0]
	if !strings.HasPrefix(c.K8sConfigInline, "enc:v1:") || c.K8sConfigInline == kubeconfig {
		t.Fatalf("K8sConfigInline not encrypted: %q", c.K8sConfigInline)
	}
	if !strings.HasPrefix(c.PostLoginScript, "enc:v1:") || c.PostLoginScript == script {
		t.Fatalf("PostLoginScript not encrypted: %q", c.PostLoginScript)
	}

	res, err := parseUniterm(b, ParseOptions{Password: "pass123"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if res.Connections[0].K8sConfigInline != kubeconfig {
		t.Fatalf("K8sConfigInline not restored: %q", res.Connections[0].K8sConfigInline)
	}
	if res.Connections[0].PostLoginScript != script {
		t.Fatalf("PostLoginScript not restored: %q", res.Connections[0].PostLoginScript)
	}
}
