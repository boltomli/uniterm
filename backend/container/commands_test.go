package container

import (
	"reflect"
	"testing"
)

func TestPSArgs(t *testing.T) {
	got := psArgs(RuntimeDocker, "")
	want := []string{"docker", "ps", "-a", "--format", psFormatDocker}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestPSArgsNerdctlNamespace(t *testing.T) {
	got := psArgs(RuntimeNerdctl, "k8s.io")
	want := []string{"nerdctl", "--namespace", "k8s.io", "ps", "-a", "--format", "{{json .}}"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestExecArgs(t *testing.T) {
	got := execArgs(RuntimeDocker, "", "abc123", "sh")
	want := []string{"docker", "exec", "-it", "abc123", "sh"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestJoinShellCommand(t *testing.T) {
	got := JoinShellCommand([]string{"docker", "exec", "-it", "my'container", "sh"})
	want := `docker exec -it 'my'\''container' sh`
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestPSArgsWSLC(t *testing.T) {
	got := psArgs(RuntimeWSLC, "")
	want := []string{"wslc", "ps", "-a", "--format", "json"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestImagesArgsWSLC(t *testing.T) {
	got := imagesArgs(RuntimeWSLC, "")
	want := []string{"wslc", "images", "--format", "json"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestStatsArgsWSLC(t *testing.T) {
	got := statsArgs(RuntimeWSLC, "")
	want := []string{"wslc", "stats", "--format", "json"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestActionArgsWSLCStart(t *testing.T) {
	got, err := actionArgs(RuntimeWSLC, "", "start", "abc123")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"wslc", "start", "abc123"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestActionArgsWSLCStop(t *testing.T) {
	got, err := actionArgs(RuntimeWSLC, "", "stop", "abc123")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"wslc", "stop", "abc123"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestActionArgsWSLCRm(t *testing.T) {
	got, err := actionArgs(RuntimeWSLC, "", "rm", "abc123")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"wslc", "rm", "abc123"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestActionArgsWSLCUnsupported(t *testing.T) {
	for _, act := range []string{"restart", "pause", "unpause"} {
		if _, err := actionArgs(RuntimeWSLC, "", act, "abc123"); err == nil {
			t.Errorf("action %q should be unsupported for WSLC", act)
		}
	}
}

func TestDetectArgsWSLC(t *testing.T) {
	got := detectArgs(RuntimeWSLC)
	want := []string{"sh", "-c", "command -v wslc"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestPullArgsPlain(t *testing.T) {
	got := pullArgs(RuntimeDocker, "", "nginx", TransferOptions{})
	want := []string{"docker", "pull", "nginx"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestPullArgsPlatformAllTags(t *testing.T) {
	got := pullArgs(RuntimeDocker, "", "nginx", TransferOptions{Platform: "linux/arm64", AllTags: true})
	want := []string{"docker", "pull", "--platform", "linux/arm64", "--all-tags", "nginx"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestPullArgsWSLCNoExtras(t *testing.T) {
	got := pullArgs(RuntimeWSLC, "", "nginx", TransferOptions{Platform: "linux/arm64", AllTags: true})
	want := []string{"wslc", "pull", "nginx"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestPullArgsInsecurePodman(t *testing.T) {
	got := pullArgs(RuntimePodman, "", "registry.example.com/foo:1", TransferOptions{Insecure: true})
	want := []string{"podman", "pull", "--tls-verify=false", "registry.example.com/foo:1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestPullArgsInsecureNerdctl(t *testing.T) {
	got := pullArgs(RuntimeNerdctl, "default", "192.168.1.10:5000/foo:1", TransferOptions{Insecure: true})
	want := []string{"nerdctl", "--namespace", "default", "--insecure-registry", "192.168.1.10:5000", "pull", "192.168.1.10:5000/foo:1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestPullArgsInsecureDockerIgnored(t *testing.T) {
	got := pullArgs(RuntimeDocker, "", "registry.example.com/foo:1", TransferOptions{Insecure: true})
	want := []string{"docker", "pull", "registry.example.com/foo:1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestPushArgs(t *testing.T) {
	got := pushArgs(RuntimeDocker, "", "repo/name:tag", TransferOptions{})
	want := []string{"docker", "push", "repo/name:tag"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestPushArgsNerdctlInsecure(t *testing.T) {
	got := pushArgs(RuntimeNerdctl, "", "192.168.1.10:5000/foo:1", TransferOptions{Insecure: true})
	want := []string{"nerdctl", "--insecure-registry", "192.168.1.10:5000", "push", "192.168.1.10:5000/foo:1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestTagArgs(t *testing.T) {
	got := tagArgs(RuntimeDocker, "", "abc123", "repo/name:v2")
	want := []string{"docker", "tag", "abc123", "repo/name:v2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestHistoryArgsNoTrunc(t *testing.T) {
	got := historyArgs(RuntimeDocker, "", "abc123")
	want := []string{"docker", "history", "--no-trunc", "--format", "{{json .}}", "abc123"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	got = historyArgs(RuntimeWSLC, "", "abc123")
	want = []string{"wslc", "history", "--format", "json", "abc123"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("wslc: got %v want %v", got, want)
	}
}

func TestImagePruneArgs(t *testing.T) {
	got := imagePruneArgs(RuntimeDocker, "")
	want := []string{"docker", "image", "prune", "-f"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestLoginArgs(t *testing.T) {
	got := loginArgs(RuntimeDocker, "registry.example.com", "alice", false)
	want := []string{"docker", "login", "--password-stdin", "-u", "alice", "registry.example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestLoginArgsNoRegistry(t *testing.T) {
	got := loginArgs(RuntimeDocker, "", "alice", false)
	want := []string{"docker", "login", "--password-stdin", "-u", "alice"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestLoginArgsPodmanInsecure(t *testing.T) {
	got := loginArgs(RuntimePodman, "registry.example.com", "alice", true)
	want := []string{"podman", "login", "--password-stdin", "-u", "alice", "--tls-verify=false", "registry.example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestLoginArgsNerdctlInsecure(t *testing.T) {
	got := loginArgs(RuntimeNerdctl, "192.168.1.10:5000", "alice", true)
	want := []string{"nerdctl", "--insecure-registry", "192.168.1.10:5000", "login", "--password-stdin", "-u", "alice", "192.168.1.10:5000"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestRegistryHost(t *testing.T) {
	cases := []struct{ in, want string }{
		{"nginx", ""},
		{"nginx:1", ""},
		{"registry.example.com/foo", "registry.example.com"},
		{"localhost/foo", "localhost"},
		{"192.168.1.10:5000/foo", "192.168.1.10:5000"},
		{"localhost:5000/foo", "localhost:5000"},
		{"foo/bar", ""},
	}
	for _, c := range cases {
		if got := registryHost(c.in); got != c.want {
			t.Errorf("registryHost(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
