package session

import (
	"reflect"
	"testing"
)

func TestMaterializeIdentityPassword(t *testing.T) {
	resolve := func(id string) (Identity, bool) {
		if id == "id-1" {
			return Identity{ID: "id-1", Name: "prod", Username: "root", AuthType: "password", Password: "s3cret"}, true
		}
		return Identity{}, false
	}
	cfg := ConnectionConfig{ID: "c1", Host: "10.0.0.5", AuthType: "identity", IdentityId: "id-1"}
	got, err := MaterializeIdentity(cfg, resolve)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.User != "root" || got.AuthType != "password" || got.Password != "s3cret" {
		t.Fatalf("bad materialization: %+v", got)
	}
}

func TestMaterializeIdentityKey(t *testing.T) {
	resolve := func(id string) (Identity, bool) {
		return Identity{ID: "id-2", Username: "git", AuthType: "key", KeyPath: "/home/git/.ssh/id_ed25519", Password: "pp"}, true
	}
	cfg := ConnectionConfig{AuthType: "identity", IdentityId: "id-2"}
	got, err := MaterializeIdentity(cfg, resolve)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.User != "git" || got.AuthType != "key" || got.KeyPath != "/home/git/.ssh/id_ed25519" || got.Password != "pp" {
		t.Fatalf("bad materialization: %+v", got)
	}
}

// TestMaterializeIdentityUserOverride 锁定 issue #959 的语义：连接配置里
// 非空的 User 覆盖密钥库中的用户名（一个密钥多台服务器共用时免建重复身份），
// 空则回退为密钥库的用户名。
func TestMaterializeIdentityUserOverride(t *testing.T) {
	resolve := func(id string) (Identity, bool) {
		return Identity{ID: "id-3", Username: "root", AuthType: "password", Password: "s3cret"}, true
	}
	override := ConnectionConfig{User: "deploy", AuthType: "identity", IdentityId: "id-3"}
	got, err := MaterializeIdentity(override, resolve)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.User != "deploy" {
		t.Fatalf("User override = %q, want deploy", got.User)
	}
	fallback := ConnectionConfig{User: "", AuthType: "identity", IdentityId: "id-3"}
	got, err = MaterializeIdentity(fallback, resolve)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.User != "root" {
		t.Fatalf("User fallback = %q, want root", got.User)
	}
}

func TestMaterializeIdentityPassthrough(t *testing.T) {
	cfg := ConnectionConfig{User: "alice", AuthType: "password", Password: "x"}
	got, err := MaterializeIdentity(cfg, nil)
	if err != nil || !reflect.DeepEqual(got, cfg) {
		t.Fatalf("non-identity should pass through unchanged: %+v err=%v", got, err)
	}
}

func TestMaterializeIdentityMissing(t *testing.T) {
	cfg := ConnectionConfig{AuthType: "identity", IdentityId: "nope"}
	if _, err := MaterializeIdentity(cfg, func(string) (Identity, bool) { return Identity{}, false }); err == nil {
		t.Fatal("expected error for missing identity")
	}
}
