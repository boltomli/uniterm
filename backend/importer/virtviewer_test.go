package importer

import (
	"testing"
)

func TestParseVirtViewerSpice(t *testing.T) {
	data := []byte("[virt-viewer]\n" +
		"type=spice\n" +
		"host=192.168.1.10\n" +
		"port=3128\n" +
		"password=one-time-ticket\n" +
		"title=PVE vm 101\n" +
		"username=root\n")
	res, err := parseVirtViewer(data, "pve-vm-101.vv")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(res.Connections) != 1 || len(res.Warnings) != 0 {
		t.Fatalf("expected 1 connection, 0 warnings, got %d connections, %d warnings", len(res.Connections), len(res.Warnings))
	}
	c := res.Connections[0]
	if c.Type != "spice" {
		t.Fatalf("type wrong: %q", c.Type)
	}
	if c.Host != "192.168.1.10" || c.Port != 3128 {
		t.Fatalf("host/port wrong: %+v", c)
	}
	if c.Password != "one-time-ticket" || c.AuthType != "password" || c.User != "root" {
		t.Fatalf("auth mapping wrong: %+v", c)
	}
	if c.Name != "PVE vm 101" {
		t.Fatalf("name wrong: %q", c.Name)
	}
}

func TestParseVirtViewerVncDefaults(t *testing.T) {
	res, err := parseVirtViewer([]byte("[virt-viewer]\ntype=vnc\nhost=10.0.0.5\n"), "console.vv")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(res.Connections) != 1 {
		t.Fatalf("expected 1 connection, got %d", len(res.Connections))
	}
	c := res.Connections[0]
	if c.Type != "vnc" || c.Host != "10.0.0.5" || c.Port != 5900 {
		t.Fatalf("vnc mapping wrong: %+v", c)
	}
	if c.Name != "console" {
		t.Fatalf("name should fall back to file name, got %q", c.Name)
	}
}

func TestParseVirtViewerTLSOnlyWarns(t *testing.T) {
	res, err := parseVirtViewer([]byte("[virt-viewer]\ntype=spice\nhost=h\nport=3128\ntls-port=3129\n"), "h.vv")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(res.Connections) != 1 || res.Connections[0].Port != 3128 {
		t.Fatalf("plain port should win over tls-port: %+v", res.Connections)
	}
	if len(res.Warnings) != 0 {
		t.Fatalf("no warning expected when a plain port exists, got %v", res.Warnings)
	}

	res, err = parseVirtViewer([]byte("[virt-viewer]\ntype=spice\nhost=h\ntls-port=3129\n"), "h.vv")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(res.Connections) != 1 || res.Connections[0].Port != 3129 {
		t.Fatalf("tls-port should be used when no plain port: %+v", res.Connections)
	}
	if len(res.Warnings) != 1 {
		t.Fatalf("expected a TLS warning, got %v", res.Warnings)
	}
}

func TestParseVirtViewerUnsupported(t *testing.T) {
	res, err := parseVirtViewer([]byte("[virt-viewer]\ntype=ovirt\nhost=h\n"), "h.vv")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(res.Connections) != 0 || len(res.Warnings) != 1 {
		t.Fatalf("ovirt should be skipped with a warning, got %+v / %v", res.Connections, res.Warnings)
	}

	if _, err := parseVirtViewer([]byte("[virt-viewer]\nhost=h\n"), "h.vv"); err == nil {
		t.Fatal("missing type should be an error")
	}

	res, err = parseVirtViewer([]byte("[virt-viewer]\ntype=spice\nunix-path=/run/vm.sock\n"), "vm.vv")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(res.Connections) != 0 || len(res.Warnings) != 1 {
		t.Fatalf("unix-path should be skipped with a warning, got %+v / %v", res.Connections, res.Warnings)
	}
}
