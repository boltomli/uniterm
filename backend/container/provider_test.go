package container

import (
	"context"
	"io"
	"strings"
	"testing"
)

type fakeRunner struct {
	lastArgv []string
	runOut   []byte
	runErr   error
}

func (f *fakeRunner) Run(_ context.Context, argv []string) ([]byte, error) {
	f.lastArgv = argv
	return f.runOut, f.runErr
}
func (f *fakeRunner) RunStdin(_ context.Context, argv []string, _ []byte) ([]byte, error) {
	f.lastArgv = argv
	return f.runOut, f.runErr
}
func (f *fakeRunner) RunStdinStream(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
	f.lastArgv = argv
	return f.runOut, f.runErr
}
func (f *fakeRunner) RunRaw(_ context.Context, argv []string) (RawStream, error) {
	f.lastArgv = argv
	return nil, f.runErr
}
func (f *fakeRunner) RunStream(_ context.Context, argv []string) (LineStream, error) {
	f.lastArgv = argv
	return nil, f.runErr
}
func (f *fakeRunner) RunPTY(_ context.Context, argv []string, _, _ int) (PTYStream, error) {
	f.lastArgv = argv
	return nil, f.runErr
}

func TestProviderList(t *testing.T) {
	fr := &fakeRunner{runOut: []byte("a1b2c3d4e5f6|nginx:latest|web|running|Up 5 days||2026-01-02 03:04:05 +0000 UTC\n")}
	p := NewProvider(RuntimeDocker, "", fr)
	list, err := p.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Name != "web" {
		t.Fatalf("got %+v", list)
	}
	if strings.Join(fr.lastArgv, " ") != "docker ps -a --format {{.ID}}|{{.Image}}|{{.Names}}|{{.State}}|{{.Status}}|{{.Ports}}|{{.CreatedAt}}" {
		t.Fatalf("argv: %v", fr.lastArgv)
	}
}

func TestProviderActionRejectsBadAction(t *testing.T) {
	p := NewProvider(RuntimeDocker, "", &fakeRunner{})
	if err := p.Action(context.Background(), "a1", "kill9"); err == nil {
		t.Fatal("expected error")
	}
}

func TestProviderCreate(t *testing.T) {
	fr := &fakeRunner{}
	p := NewProvider(RuntimeDocker, "", fr)
	err := p.Create(context.Background(), CreateOptions{
		Image: "nginx:latest", Name: "web",
		Ports:   []PortMapping{{HostPort: "8080", ContainerPort: "80"}},
		Env:     []string{"A=1"},
		Restart: "always",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "docker run -d --name web -p 8080:80 -e A=1 --restart always nginx:latest"
	if strings.Join(fr.lastArgv, " ") != want {
		t.Fatalf("argv: %v", fr.lastArgv)
	}
}
