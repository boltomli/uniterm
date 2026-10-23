package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/ys-ll/uniterm/backend/platform"
	"github.com/ys-ll/uniterm/backend/session"
)

type desktopCaptureTestSession struct {
	session.Session
	status session.SessionStatus
}

func TestEncodeVNCClipboardText(t *testing.T) {
	a := &App{}
	for _, tc := range []struct {
		charset string
		text    string
		want    string
	}{
		{"gbk", "中文测似乎", "\xd6\xd0\xce\xc4\xb2\xe2\xcb\xc6\xba\xf5"},
		{"gb2312", "中文", "\xd6\xd0\xce\xc4"},
		{"gb18030", "中文😀", "\xd6\xd0\xce\xc4\x94\x39\xfc\x36"},
		{"big5", "中文", "\xa4\xa4\xa4\xe5"},
		{"shift-jis", "日本語", "\x93\xfa\x96\x7b\x8c\xea"},
		{"euc-jp", "日本語", "\xc6\xfc\xcb\xdc\xb8\xec"},
		{"euc-kr", "한글", "\xc7\xd1\xb1\xdb"},
	} {
		t.Run(tc.charset, func(t *testing.T) {
			for _, sample := range []struct{ text, want string }{{tc.text, tc.want}, {"", ""}, {"ASCII\r\ntext", "ASCII\r\ntext"}} {
				encoded, err := a.EncodeVNCClipboardText(sample.text, tc.charset)
				if err != nil {
					t.Fatal(err)
				}
				data, err := base64.StdEncoding.DecodeString(encoded)
				if err != nil {
					t.Fatal(err)
				}
				if string(data) != sample.want {
					t.Fatalf("%s bytes for %q = %x, want %x", tc.charset, sample.text, data, sample.want)
				}
			}
			if _, err := a.EncodeVNCClipboardText("😀", tc.charset); err == nil && tc.charset != "gb18030" {
				t.Fatal("unrepresentable text must not silently become question marks")
			}
		})
	}
	for _, charset := range []string{"", "invalid", "utf-8", "latin1"} {
		if _, err := a.EncodeVNCClipboardText("text", charset); err == nil {
			t.Fatalf("unsupported encoding %q accepted", charset)
		}
	}
}

func (s desktopCaptureTestSession) Status() session.SessionStatus { return s.status }

func TestRemoteDesktopKeyboardSessionValidation(t *testing.T) {
	a := &App{sessionManager: session.NewSessionManager(), rdpKeyboard: &platform.RDPKeyboardCapture{}, mainHwnd: 1}
	a.sessionManager.Add(desktopCaptureTestSession{Session: session.NewVNCSession("vnc"), status: session.StatusConnected})
	a.sessionManager.Add(desktopCaptureTestSession{Session: session.NewRDPSession("rdp"), status: session.StatusConnected})
	a.sessionManager.Add(desktopCaptureTestSession{Session: session.NewSPICESession("spice"), status: session.StatusConnected})
	if _, err := a.SetVNCKeyboardCapture("vnc", "vnc-capture", true); err != nil {
		t.Fatalf("connected VNC rejected: %v", err)
	}
	if _, err := a.SetRDPKeyboardCapture("rdp", "rdp-capture", true); err != nil {
		t.Fatalf("connected RDP rejected: %v", err)
	}
	if _, err := a.SetSPICEKeyboardCapture("spice", "spice-capture", true); err != nil {
		t.Fatalf("connected SPICE rejected: %v", err)
	}
	for _, id := range []string{"rdp", "vnc", "missing"} {
		if _, err := a.SetSPICEKeyboardCapture(id, "invalid", true); err == nil {
			t.Fatalf("SPICE accepted session %q", id)
		}
	}
	a.sessionManager.Add(desktopCaptureTestSession{Session: session.NewSPICESession("closed-spice"), status: session.StatusDisconnected})
	if _, err := a.SetSPICEKeyboardCapture("closed-spice", "invalid", true); err == nil {
		t.Fatal("disconnected SPICE accepted")
	}
	if _, err := a.SetSPICEKeyboardCapture("missing", "stale", false); err != nil {
		t.Fatalf("stale SPICE blur must be harmless: %v", err)
	}
	for _, id := range []string{"rdp", "missing"} {
		if _, err := a.SetVNCKeyboardCapture(id, "invalid", true); err == nil {
			t.Fatalf("VNC accepted session %q", id)
		}
	}
	a.sessionManager.Add(desktopCaptureTestSession{Session: session.NewVNCSession("closed"), status: session.StatusDisconnected})
	if _, err := a.SetVNCKeyboardCapture("closed", "invalid", true); err == nil {
		t.Fatal("disconnected VNC accepted")
	}
	if _, err := a.SetVNCKeyboardCapture("missing", "stale", false); err != nil {
		t.Fatalf("stale blur must be harmless: %v", err)
	}
}

func TestClipboardTransferChunks(t *testing.T) {
	a := &App{}
	files := []ClipboardFileInfo{{Name: "报告.txt", Size: 6, LastModified: 1700000000000}, {Name: "empty", Size: 0}}
	id, err := a.ClipboardBeginFileTransfer(files)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.ClipboardAbortFileTransfer(id) })
	encode := func(value string) string { return base64.StdEncoding.EncodeToString([]byte(value)) }
	if err := a.ClipboardWriteFileChunk(id, 0, 1, encode("abc")); err == nil {
		t.Fatal("accepted a non-contiguous chunk")
	}
	if err := a.ClipboardWriteFileChunk(id, 0, 0, encode("abc")); err != nil {
		t.Fatal(err)
	}
	publish := func(paths []string) error {
		t.Cleanup(func() { os.RemoveAll(filepath.Dir(paths[0])) })
		data, err := os.ReadFile(paths[0])
		if err != nil || string(data) != "abcdef" {
			t.Fatalf("downloaded file = %q, %v", data, err)
		}
		st, err := os.Stat(paths[1])
		if err != nil || st.Size() != 0 {
			t.Fatalf("empty file: %v", err)
		}
		st, err = os.Stat(paths[0])
		if err != nil || st.ModTime().UnixMilli() != files[0].LastModified {
			t.Fatalf("modified time: %v", err)
		}
		return nil
	}
	if err := finishClipboardTransfer(id, publish); err == nil {
		t.Fatal("published an incomplete file")
	}
	if err := a.ClipboardWriteFileChunk(id, 0, 3, encode("defg")); err == nil {
		t.Fatal("accepted data beyond declared size")
	}
	if err := a.ClipboardWriteFileChunk(id, 0, 3, encode("def")); err != nil {
		t.Fatal(err)
	}
	if err := finishClipboardTransfer(id, publish); err != nil {
		t.Fatal(err)
	}
	if err := a.ClipboardWriteFileChunk(id, 0, 0, encode("abc")); err == nil {
		t.Fatal("accepted an expired transfer token")
	}
}

func TestClipboardTransferRejectsPaths(t *testing.T) {
	a := &App{}
	for _, name := range []string{"", ".", "..", "../outside", `..\outside`, "dir/file", `C:\file`, "file:stream", "file\x00.txt"} {
		t.Run(name, func(t *testing.T) {
			if id, err := a.ClipboardBeginFileTransfer([]ClipboardFileInfo{{Name: name}}); err == nil {
				a.ClipboardAbortFileTransfer(id)
				t.Fatalf("accepted unsafe path %q", name)
			}
		})
	}
	if id, err := a.ClipboardBeginFileTransfer([]ClipboardFileInfo{{Name: "File"}, {Name: "file"}}); err == nil {
		a.ClipboardAbortFileTransfer(id)
		t.Fatal("accepted case-insensitive duplicate names")
	}
}

func TestClipboardTransferAbort(t *testing.T) {
	a := &App{}
	id, err := a.ClipboardBeginFileTransfer([]ClipboardFileInfo{{Name: "partial", Size: 1024}})
	if err != nil {
		t.Fatal(err)
	}
	clipboardTransfers.Lock()
	dir := clipboardTransfers.items[id].dir
	clipboardTransfers.Unlock()
	if err := a.ClipboardAbortFileTransfer(id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("partial files remain: %v", err)
	}
}
