package importer

import "testing"

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
