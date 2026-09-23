package session

import (
	"bytes"
	"net"
	"os/exec"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestOSC7ScannerSplitAcrossChunks(t *testing.T) {
	var sc osc7Scanner
	// partial sequence in first chunk, rest in second
	cwd1, cleaned1, found1 := sc.Feed([]byte("hello\x1b]7;file://myhost/ho"))
	cwd2, cleaned2, found2 := sc.Feed([]byte("me/user\x1b\\world"))
	if found1 || cwd1 != "" {
		t.Fatal("must not report before terminator")
	}
	if !found2 || cwd2 != "/home/user" {
		t.Fatalf("cwd=%q found=%v", cwd2, found2)
	}
	combined := string(cleaned1) + string(cleaned2)
	if combined != "helloworld" {
		t.Fatalf("cleaned = %q, want helloworld", combined)
	}
	if bytes.Contains([]byte(combined), []byte("\x1b]7;")) {
		t.Fatal("cleaned output still contains the OSC-7 sequence")
	}
}

func TestOSC7ScannerSplitTerminator(t *testing.T) {
	var sc osc7Scanner
	cwd1, _, found1 := sc.Feed([]byte("\x1b]7;file://h/a\x1b"))
	cwd2, cleaned2, found2 := sc.Feed([]byte("\\next"))
	if found1 || cwd1 != "" {
		t.Fatal("must not report before terminator")
	}
	if !found2 || cwd2 != "/a" {
		t.Fatalf("cwd=%q found=%v", cwd2, found2)
	}
	if string(cleaned2) != "next" {
		t.Fatalf("cleaned = %q, want next", cleaned2)
	}
}

func TestOSC7UrlDecoding(t *testing.T) {
	var sc osc7Scanner
	cwd, _, found := sc.Feed([]byte("\x1b]7;file://h/space%20dir\x07"))
	if !found || cwd != "/space dir" {
		t.Fatalf("cwd=%q found=%v", cwd, found)
	}
}

func TestOSC7ScannerEmptyHostBEL(t *testing.T) {
	var sc osc7Scanner
	// BEL-terminated sequence with an empty host part (file:///...)
	cwd, cleaned, found := sc.Feed([]byte("pre\x1b]7;file:///home/x\x07post"))
	if !found || cwd != "/home/x" {
		t.Fatalf("cwd=%q found=%v", cwd, found)
	}
	if string(cleaned) != "prepost" {
		t.Fatalf("cleaned = %q, want prepost", cleaned)
	}
}

// Reproduced in the field (dev log): real prompts emit the ST-terminated OSC-7
// immediately followed by a BEL-terminated OSC-0 title sequence. The scanner
// must terminate the OSC-7 payload at the FIRST terminator (ST here), not at
// the later BEL — swallowing "\x1b\\" plus the whole title into the payload
// produced a garbage cwd and disabled path following entirely.
func TestOSC7ScannerSTTerminatedBeforeFollowingOSC0(t *testing.T) {
	var sc osc7Scanner
	input := "\x1b]7;file:///root\x1b\\\x1b]0;root@localhost:~\x07"
	cwd, cleaned, found := sc.Feed([]byte(input))
	if !found || cwd != "/root" {
		t.Fatalf("cwd=%q found=%v, want /root", cwd, found)
	}
	if !strings.Contains(string(cleaned), "\x1b]0;root@localhost:~\x07") {
		t.Fatalf("following OSC-0 title must pass through to the display: %q", cleaned)
	}
	if strings.Contains(string(cleaned), "\x1b]7;") {
		t.Fatal("cleaned output still contains the OSC-7 sequence")
	}
}

func TestBuildStartupCwdHook(t *testing.T) {
	tests := []struct {
		name     string
		shell    string
		wantOK   bool
		mustHave []string
	}{
		{
			name:   "bash",
			shell:  "/bin/bash",
			wantOK: true,
			mustHave: []string{
				"__uniterm_osc7",
				`printf '\r\033[2K'`,
				`printf '\033]7777;uniterm-ok\007'`,
				"stty echo",
			},
		},
		{
			name:   "zsh",
			shell:  "/usr/bin/zsh",
			wantOK: true,
			mustHave: []string{
				"__uniterm_osc7",
				"precmd_functions",
				`printf '\r\033[2K'`,
				`printf '\033]7777;uniterm-ok\007'`,
				"stty echo",
			},
		},
		{
			name:   "fish",
			shell:  "/usr/bin/fish",
			wantOK: true,
			mustHave: []string{
				"__uniterm_osc7",
				`printf '\r\e[2K'`,
				`printf '\e]7777;uniterm-ok\a'`,
				"stty echo",
			},
		},
		{name: "unsupported ksh", shell: "/usr/bin/ksh", wantOK: false},
		{name: "windows cmd", shell: "cmd", wantOK: false},
		{name: "windows backslash path", shell: `C:\Program Files\Git\bin\bash.exe`, wantOK: false},
		{name: "empty", shell: "", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := buildStartupCwdHook(tt.shell)
			if ok != tt.wantOK {
				t.Fatalf("buildStartupCwdHook(%q) ok = %v, want %v", tt.shell, ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if !strings.HasPrefix(got, " ") || !strings.HasSuffix(got, "\n") {
				t.Fatalf("snippet must start with a space and end with a newline: %q", got)
			}
			if !strings.Contains(got, "__uniterm_osc7") {
				t.Fatalf("snippet must define the __uniterm_osc7 hook: %q", got)
			}
			for _, want := range tt.mustHave {
				if !strings.Contains(got, want) {
					t.Fatalf("snippet for %s must contain %q: %q", tt.shell, want, got)
				}
			}
		})
	}
}

// Echo must be restored BEFORE the ready marker is printed: a missing stty
// then leaves the marker unsent, so the blind fallback restores echo instead
// of a confirmed hook leaving the terminal permanently silent. The line
// clear (erasing a prompt printed before the injection landed, so the shell's
// post-hook prompt renders exactly once) must also precede the marker.
func TestStartupCwdHookRestoresEchoBeforeMarker(t *testing.T) {
	for _, shell := range []string{"/bin/bash", "/usr/bin/zsh", "/usr/bin/fish"} {
		hook, ok := buildStartupCwdHook(shell)
		if !ok {
			t.Fatalf("shell %q must be supported", shell)
		}
		if strings.Index(hook, "stty echo") > strings.Index(hook, "7777;uniterm-ok") {
			t.Fatalf("stty echo must run before the ready marker for %s: %q", shell, hook)
		}
		if strings.Index(hook, "[2K") > strings.Index(hook, "7777;uniterm-ok") {
			t.Fatalf("line clear must run before the ready marker for %s: %q", shell, hook)
		}
	}
}

func TestGeneratedStartupCwdHookHasValidBashSyntax(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not available for generated-script syntax checks")
	}
	hook, ok := buildStartupCwdHook("/bin/bash")
	if !ok {
		t.Fatal("bash must be supported")
	}
	cmd := exec.Command(bash, "-n")
	cmd.Stdin = strings.NewReader(hook)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("startup cwd hook has invalid bash syntax: %v: %s\n%s", err, out, hook)
	}
}

func TestHookReadyScannerSingleChunk(t *testing.T) {
	var sc hookReadyScanner
	in := []byte("prompt stuff" + sshCwdHookReadyMarker + "more")
	cleaned, found := sc.Feed(in)
	if !found {
		t.Fatal("marker must be detected")
	}
	if string(cleaned) != "prompt stuffmore" {
		t.Fatalf("cleaned = %q, want %q", cleaned, "prompt stuffmore")
	}
}

func TestHookReadyScannerSplitAcrossChunks(t *testing.T) {
	m := []byte(sshCwdHookReadyMarker)
	var sc hookReadyScanner
	c1, found1 := sc.Feed(append([]byte("abc"), m[:7]...))
	if found1 || string(c1) != "abc" {
		t.Fatalf("first chunk found=%v cleaned=%q", found1, c1)
	}
	c2, found2 := sc.Feed(append(append([]byte{}, m[7:]...), []byte("xyz")...))
	if !found2 {
		t.Fatal("marker split across chunks must be detected")
	}
	if string(c2) != "xyz" {
		t.Fatalf("second chunk cleaned = %q, want xyz", c2)
	}
}

func TestHookReadyScannerNoMarkerPassthrough(t *testing.T) {
	var sc hookReadyScanner
	in := []byte("normal terminal output\r\n$ ")
	cleaned, found := sc.Feed(in)
	if found {
		t.Fatal("no marker must report found=false")
	}
	if string(cleaned) != string(in) {
		t.Fatalf("cleaned = %q, want input unchanged", cleaned)
	}
}

func TestHookReadyScannerStopsAfterDone(t *testing.T) {
	var sc hookReadyScanner
	if _, found := sc.Feed([]byte(sshCwdHookReadyMarker)); !found {
		t.Fatal("first marker must be detected")
	}
	sc.done = true
	in := []byte("everything passes through now \x1b]7777;uniterm-ok\x07")
	cleaned, found := sc.Feed(in)
	if found {
		t.Fatal("scanner must stop matching after done")
	}
	if string(cleaned) != string(in) {
		t.Fatalf("cleaned = %q, want input unchanged", cleaned)
	}
}

func TestWSLBootstrapKeepsIndependentStartupBehavior(t *testing.T) {
	files, ok := buildWSLShellBootstrap("/bin/bash")
	if !ok {
		t.Fatal("WSL bash must be supported")
	}
	rc := files["rcfile"]
	if !strings.Contains(rc, "$HOME/.bashrc") || !strings.Contains(rc, "$HOME/.bash_profile") {
		t.Fatalf("WSL bootstrap must preserve its existing rc files: %s", rc)
	}
	if strings.Contains(rc, "/etc/profile") || strings.Contains(rc, "$HOME/.bash_login") {
		t.Fatalf("SSH login emulation must not leak into WSL bootstrap: %s", rc)
	}
}

func TestSSHRunCommandTimesOutWhileOpeningSession(t *testing.T) {
	signer, err := ssh.ParsePrivateKey([]byte(testHostKeyPEM))
	if err != nil {
		t.Fatalf("parse test host key: %v", err)
	}
	serverConfig := &ssh.ServerConfig{NoClientAuth: true}
	serverConfig.AddHostKey(signer)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	releaseServer := make(chan struct{})
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		serverConn, err := listener.Accept()
		if err != nil {
			return
		}
		defer serverConn.Close()
		_, channels, requests, err := ssh.NewServerConn(serverConn, serverConfig)
		if err != nil {
			return
		}
		go ssh.DiscardRequests(requests)
		_, ok := <-channels
		if !ok {
			return
		}
		<-releaseServer
		// Closing the transport unblocks the pending channel-open without
		// depending on a channel rejection completing first.
		_ = serverConn.Close()
	}()

	clientConn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("dial test server: %v", err)
	}
	conn, channels, requests, err := ssh.NewClientConn(clientConn, "pipe", &ssh.ClientConfig{
		User:            "test",
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	})
	if err != nil {
		t.Fatalf("create SSH client: %v", err)
	}
	client := ssh.NewClient(conn, channels, requests)

	const timeout = 50 * time.Millisecond
	started := time.Now()
	_, err = sshRunCommand(client, "true", "", timeout)
	if err == nil || !strings.Contains(err.Error(), "timed out opening SSH session") {
		t.Fatalf("sshRunCommand error = %v, want channel-open timeout", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("channel-open timeout took %s, want a bounded return", elapsed)
	}

	close(releaseServer)
	select {
	case <-serverDone:
	case <-time.After(time.Second):
		t.Fatal("test SSH server did not stop")
	}
	_ = client.Close()
}

func TestSSHIntegrationTempPathValidation(t *testing.T) {
	valid := []string{
		"/tmp/uniterm-Ab12Z9",
		"/tmp/uniterm-000000",
	}
	for _, path := range valid {
		if !isSSHIntegrationTempPath(path) {
			t.Errorf("isSSHIntegrationTempPath(%q) = false, want true", path)
		}
	}

	invalid := []string{
		"",
		"/tmp/uniterm-",
		"/tmp/uniterm-short",
		"/tmp/uniterm-Ab12Z9/child",
		"/tmp/uniterm-Ab12Z_",
		"/var/tmp/uniterm-Ab12Z9",
	}
	for _, path := range invalid {
		if isSSHIntegrationTempPath(path) {
			t.Errorf("isSSHIntegrationTempPath(%q) = true, want false", path)
		}
	}
}

func TestCleanRemoteTempPath(t *testing.T) {
	tests := []struct {
		name    string
		out     string
		want    string
		wantErr bool
	}{
		{name: "plain", out: "/tmp/uniterm-Ab12Z9\n", want: "/tmp/uniterm-Ab12Z9"},
		{name: "crlf", out: "/tmp/uniterm-Ab12Z9\r\n", want: "/tmp/uniterm-Ab12Z9"},
		{name: "surrounded by banner", out: "Welcome\n/tmp/uniterm-Ab12Z9\nLast login\n", want: "/tmp/uniterm-Ab12Z9"},
		{name: "embedded path rejected", out: "created /tmp/uniterm-Ab12Z9\n", wantErr: true},
		{name: "whitespace rejected", out: " /tmp/uniterm-Ab12Z9 \n", wantErr: true},
		{name: "multiple paths rejected", out: "/tmp/uniterm-Ab12Z9\n/tmp/uniterm-Cd34Y8\n", wantErr: true},
		{name: "invalid suffix rejected", out: "/tmp/uniterm-Ab12Z_\n", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := cleanRemoteTempPath(tt.out)
			if (err != nil) != tt.wantErr {
				t.Fatalf("cleanRemoteTempPath(%q) error = %v, wantErr %v", tt.out, err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("cleanRemoteTempPath(%q) = %q, want %q", tt.out, got, tt.want)
			}
		})
	}
}
