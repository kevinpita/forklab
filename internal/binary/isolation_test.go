package binary

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/kevinpita/forklab/internal/profile"
)

func TestSourceSlotsPreserveOtherBinariesAndLegacyCache(t *testing.T) {
	root := t.TempDir()
	checkout := filepath.Join(root, "checkout")
	if err := os.Mkdir(checkout, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"first": "#!/bin/sh\necho 1.2.3\n", "second": "#!/bin/sh\necho 1.2.3\n# second\n"} {
		if err := os.WriteFile(filepath.Join(checkout, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	c := Cache{Dir: filepath.Join(root, "cache")}
	legacy := filepath.Join(c.Dir, "chain", "1.2.3", "chaind")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("legacy binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	sources := []profile.SrcSource{
		{Dir: profile.Template(checkout), Build: `cp "$INPUT" out`, Out: "out", Env: map[string]string{"INPUT": "first"}},
		{Dir: profile.Template(checkout), Build: `cp "$INPUT" out`, Out: "out", Env: map[string]string{"INPUT": "second"}},
		{Dir: profile.Template(checkout), Build: `cp "$INPUT" out; printf '# other build\n' >> out`, Out: "out", Env: map[string]string{"INPUT": "second"}},
		{Dir: profile.Template(checkout), Build: `cp first another`, Out: "another"},
		{Dir: profile.Template(checkout), Build: `cp first another; cp second out`, Out: "another"},
		{Dir: profile.Template(checkout), Build: `cp first another; cp second out`, Out: "out"},
	}
	var paths []string
	var contents [][]byte
	for _, src := range sources {
		b, err := c.Resolve(t.Context(), chainProfile("1.2.3", src), "1.2.3", Options{})
		if err != nil {
			t.Fatal(err)
		}
		for i, path := range paths {
			if b.Path == path {
				t.Fatalf("different build shared executable %s", path)
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != string(contents[i]) {
				t.Fatalf("earlier binary changed: %s, %v", data, err)
			}
		}
		data, err := os.ReadFile(b.Path)
		if err != nil {
			t.Fatal(err)
		}
		paths, contents = append(paths, b.Path), append(contents, data)
	}
	if data, err := os.ReadFile(legacy); err != nil || string(data) != "legacy binary" {
		t.Fatalf("legacy cache changed: %s, %v", data, err)
	}
	listed, err := c.List()
	if err != nil || len(listed) != len(sources) {
		t.Fatalf("listed %v: %v", listed, err)
	}
	for _, b := range listed {
		if b.Version != "1.2.3" || b.Profile != "chain" {
			t.Fatalf("slot leaked into metadata: %+v", b)
		}
	}
}

func TestRelativeCheckoutResolvesAgainstCurrentDirectory(t *testing.T) {
	c := Cache{Dir: t.TempDir()}
	p := chainProfile("1.2.3", profile.SrcSource{Dir: ".", Build: "cp input out", Out: "out"})
	var first Binary
	for i, body := range []string{"#!/bin/sh\necho 1.2.3\n", "#!/bin/sh\necho 1.2.3\n# second checkout\n"} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "input"), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Chdir(dir)
		b, err := c.Resolve(t.Context(), p, "1.2.3", Options{})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = b
		} else if b.Path == first.Path || b.Source == first.Source {
			t.Fatalf("different checkout reused %+v", b)
		}
	}
}

func TestResolverExpandsTildeForExecutableAndCheckout(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	if err := os.WriteFile(filepath.Join(root, "input"), []byte("#!/bin/sh\necho 1.2.3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := Cache{Dir: t.TempDir()}
	for _, src := range []profile.Source{profile.PathSource{Path: "~/input"}, profile.SrcSource{Dir: "~/", Build: "cp input out", Out: "out"}} {
		b, err := c.Resolve(t.Context(), chainProfile("1.2.3", src), "1.2.3", Options{})
		if err != nil {
			t.Fatal(err)
		}
		if b.Source != filepath.Join(root, "input") && b.Source != root {
			t.Fatalf("tilde source %s", b.Source)
		}
	}
}

type buildStarted struct {
	once  sync.Once
	ready chan struct{}
}

func (w *buildStarted) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.ready) })
	return len(p), nil
}

func TestConcurrentLocalBuildsPreserveEachConfiguration(t *testing.T) {
	root := t.TempDir()
	checkout, alias := filepath.Join(root, "checkout"), filepath.Join(root, "alias")
	if err := os.Mkdir(checkout, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(checkout, alias); err != nil {
		t.Fatal(err)
	}
	release := filepath.Join(root, "release")
	releaseFirst := func() {
		if err := os.WriteFile(release, nil, 0o644); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(releaseFirst)
	script := `printf '#!/bin/sh\necho %s\n' "$MARKER" > out
chmod +x out
printf 'built %s\n' "$MARKER"
if test "$MARKER" = first; then
 while ! test -f "$RELEASE"; do sleep 0.01; done
fi`
	c := Cache{Dir: filepath.Join(root, "cache")}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	type result struct {
		binary Binary
		err    error
	}
	resolve := func(name, dir, marker string, started *buildStarted, done chan<- result) {
		p := chainProfile("1.2.3", profile.SrcSource{Dir: profile.Template(dir), Build: script, Out: "out", Env: map[string]string{"MARKER": marker, "RELEASE": release}})
		p.Name = name
		b, err := c.Resolve(ctx, p, "1.2.3", Options{BuildLog: started})
		done <- result{b, err}
	}
	firstStarted := &buildStarted{ready: make(chan struct{})}
	secondStarted := &buildStarted{ready: make(chan struct{})}
	firstDone, secondDone := make(chan result, 1), make(chan result, 1)
	go resolve("first-profile", checkout, "first", firstStarted, firstDone)
	select {
	case <-firstStarted.ready:
	case <-ctx.Done():
		t.Fatal("first build did not start")
	}
	go resolve("second-profile", alias, "second", secondStarted, secondDone)
	select {
	case <-secondStarted.ready:
	case <-time.After(time.Second):
	}
	releaseFirst()
	for marker, done := range map[string]<-chan result{"first": firstDone, "second": secondDone} {
		select {
		case r := <-done:
			if r.err != nil {
				t.Fatal(r.err)
			}
			if got := versionOf(t, r.binary.Path); got != marker+"\n" {
				t.Fatalf("%s configuration cached %q", marker, got)
			}
		case <-ctx.Done():
			t.Fatal("concurrent builds did not complete")
		}
	}
}
