package session

import (
	"strings"
	"testing"
)

// TestProbeFTPRequiredUsesServerName: the probe built its TLS config without
// ServerName, so a verified FTPS handshake failed with Go's
// "either ServerName or InsecureSkipVerify must be specified" — users then
// turned verification off. After the fix the handshake must be attempted
// (failing against a plaintext-only server with a transport error instead).
func TestProbeFTPRequiredUsesServerName(t *testing.T) {
	fs := newFakeFTP(t, func(f *fakeFTPConn) {
		if err := f.send("220 fake FTP server ready"); err != nil {
			return
		}
		line, err := f.read()
		if err != nil {
			return
		}
		if strings.HasPrefix(line, "AUTH TLS") {
			_ = f.send("234 AUTH TLS OK")
		}
		// Close without speaking TLS: the client handshake must fail as a
		// transport error, never as a missing-ServerName config error.
	})
	_, err := probeFTP(ConnectionConfig{
		Type: "ftp", Host: "127.0.0.1", Port: fs.port(),
		User: "u", Password: "p", FtpEncryption: "required",
	})
	if err == nil {
		t.Fatal("probe against plaintext server must fail")
	}
	if strings.Contains(err.Error(), "either ServerName or InsecureSkipVerify") {
		t.Errorf("probe TLS config is missing ServerName: %v", err)
	}
}

// TestProbeFTPAutoRefusesDowngradeOnTampering: the probe's auto mode downgraded
// to plaintext on any dial error, sending PASS in the clear even when the
// server rejected AUTH TLS yet advertised it in FEAT (the STARTTLS-stripping
// shape). It must refuse and never send credentials.
func TestProbeFTPAutoRefusesDowngradeOnTampering(t *testing.T) {
	fs := newFakeFTP(t,
		ftpAuthTLSReject(500), // conn 1: explicit-TLS dial, AUTH TLS refused
		ftpPlainServer(ftpPlainOpts{feat: []string{"AUTH TLS", "MLST size;type;"}}), // conn 2: FEAT probe still advertises AUTH TLS
	)
	_, err := probeFTP(ConnectionConfig{
		Type: "ftp", Host: "127.0.0.1", Port: fs.port(),
		User: "u", Password: "p", FtpEncryption: "auto",
	})
	if err == nil {
		t.Fatal("probe must refuse the downgrade: AUTH TLS rejected yet FEAT advertises it")
	}
	if !strings.Contains(err.Error(), "refusing plaintext downgrade") {
		t.Errorf("probe error should explain the refusal, got: %v", err)
	}
	for _, cmd := range fs.transcript() {
		if strings.HasPrefix(cmd, "PASS ") {
			t.Errorf("credentials sent in clear despite tampering shape: %q", cmd)
		}
	}
}
