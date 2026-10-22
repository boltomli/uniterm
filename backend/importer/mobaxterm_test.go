package importer

import (
	"strings"
	"testing"

	"github.com/ys-ll/uniterm/backend/session"
)

func TestParseMobaXterm(t *testing.T) {
	data := []byte("[Bookmarks_1]\nSubRep=prod\\web\nssh1=#109#0%10.0.0.2%22%admin\n[Bookmarks_2]\nSubRep=staging\ndb1=#109#0%10.0.0.5%22%admin\n")
	res, err := parseMobaXterm(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(res.Connections) != 2 {
		t.Fatalf("expected 2 connections, got %d", len(res.Connections))
	}
	if res.Connections[0].Type != "ssh" || res.Connections[0].Host != "10.0.0.2" || res.Connections[0].User != "admin" {
		t.Fatalf("ssh mapping wrong: %+v", res.Connections[0])
	}
	if res.Connections[1].Type != "ssh" || res.Connections[1].Host != "10.0.0.5" || res.Connections[1].User != "admin" {
		t.Fatalf("db mapping wrong: %+v", res.Connections[1])
	}
	if res.Connections[0].GroupId == nil || res.Connections[1].GroupId == nil {
		t.Fatalf("expected group ids, got %+v %+v", res.Connections[0], res.Connections[1])
	}
	if groupPathFor(res.Groups, *res.Connections[0].GroupId) != "prod/web" {
		t.Fatalf("ssh1 group path: want prod/web, got %q", groupPathFor(res.Groups, *res.Connections[0].GroupId))
	}
	if groupPathFor(res.Groups, *res.Connections[1].GroupId) != "staging" {
		t.Fatalf("db1 group path: want staging, got %q", groupPathFor(res.Groups, *res.Connections[1].GroupId))
	}
}

func TestParseMobaXtermSFTP(t *testing.T) {
	data := []byte("[Bookmarks]\nSubRep=\n89.111.142.71 (root)=#140#7%89.111.142.71%22%root%-1%0%%0%0%%0%%1080%%%%0#MobaFont%10%0%0%-1%15%236,236,236%30,30,30%180,180,192%0%-1%0%%xterm%-1%0%_Std_Colors_0_%80%24%0%1%-1%<none>%%0%0%-1%0%#0# #-1\n")
	res, err := parseMobaXterm(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Connections) != 1 {
		t.Fatalf("expected 1 connection, got %d (warnings: %v)", len(res.Connections), res.Warnings)
	}
	c := res.Connections[0]
	if c.Type != "sftp" || c.Host != "89.111.142.71" || c.Port != 22 || c.User != "root" {
		t.Fatalf("sftp mapping wrong: %+v", c)
	}
	if c.AuthType != "password" {
		t.Fatalf("sftp auth type: want password, got %q", c.AuthType)
	}
}

func TestParseMobaXtermProtocols(t *testing.T) {
	data := []byte("[Bookmarks]\nSubRep=\n" +
		"telnet1=#98#1%10.0.0.2%23%bob\n" +
		"rdp1=#91#4%10.0.0.3%-1%administrator\n" +
		"vnc1=#128#5%10.0.0.4%5901%guest\n" +
		"ftp1=#141#6%10.0.0.5%-1%ftpuser\n" +
		"serial1=#131#8%COM3%115200\n" +
		"shell1=#105#10%\n" +
		"mosh1=#130#12%10.0.0.7%-1%admin\n" +
		"s3=#150#13%s3.example.com%443%ACCESSKEY\n" +
		"wsl1=#151#14%Ubuntu\n" +
		"rsh1=#97#2%10.0.0.9%22%root\n")
	res, err := parseMobaXterm(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Connections) != 9 {
		t.Fatalf("expected 9 connections, got %d (warnings: %v)", len(res.Connections), res.Warnings)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "Rsh") {
		t.Fatalf("expected one Rsh warning, got %v", res.Warnings)
	}
	byName := map[string]session.ConnectionConfig{}
	for _, c := range res.Connections {
		byName[c.Name] = c
	}
	checks := []struct {
		name string
		typ  string
		host string
		port int
		user string
	}{
		{"telnet1", "telnet", "10.0.0.2", 23, "bob"},
		{"rdp1", "rdp", "10.0.0.3", 3389, "administrator"},
		{"vnc1", "vnc", "10.0.0.4", 5901, "guest"},
		{"ftp1", "ftp", "10.0.0.5", 21, "ftpuser"},
		{"mosh1", "mosh", "10.0.0.7", 22, "admin"},
		{"s3", "s3", "s3.example.com", 443, "ACCESSKEY"},
	}
	for _, want := range checks {
		c, ok := byName[want.name]
		if !ok {
			t.Fatalf("%s not imported", want.name)
		}
		if c.Type != want.typ || c.Host != want.host || c.Port != want.port || c.User != want.user {
			t.Fatalf("%s mapping wrong: %+v", want.name, c)
		}
	}
	if c := byName["ftp1"]; c.AuthType != "password" {
		t.Fatalf("ftp auth type: want password, got %q", c.AuthType)
	}
	if c := byName["serial1"]; c.SerialPort != "COM3" || c.SerialBaudRate != 115200 || c.Port != 0 {
		t.Fatalf("serial mapping wrong: %+v", c)
	}
	if c := byName["shell1"]; c.Type != "local" || c.Host != "" {
		t.Fatalf("local mapping wrong: %+v", c)
	}
	if c := byName["wsl1"]; c.Type != "wsl" || c.ShellPath != "wsl://Ubuntu" {
		t.Fatalf("wsl mapping wrong: %+v", c)
	}
}

func TestParseMobaXtermPrivateKey(t *testing.T) {
	data := []byte("[Bookmarks]\nSubRep=\nweb=#109#0%10.0.0.8%22%alice%%-1%-1%%%%%0%0%0%C:\\keys\\example.ppk%%-1%-1%0\nplain=#109#0%10.0.0.9%22%bob%%-1%-1%%%%%0%0%0%%%-1\n")
	res, err := parseMobaXterm(data)
	if err != nil {
		t.Fatal(err)
	}
	if c := res.Connections[0]; c.AuthType != "key" || c.KeyPath != `C:\keys\example.ppk` {
		t.Fatalf("key not imported: %+v", c)
	}
	if c := res.Connections[1]; c.AuthType != "password" || c.KeyPath != "" {
		t.Fatalf("plain session changed: %+v", c)
	}
}
