package lab

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kevinpita/forklab/internal/profile"
)

func builtinProfile(t *testing.T, name string) profile.Entry {
	t.Helper()
	e, err := profile.Store{Dir: t.TempDir()}.Get(name)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func sampleConfig(t *testing.T) Config {
	return Config{
		Name: "demo", Mode: ModeFresh, ChainID: "xrplevm_1440000-1", Version: "11.1.1", Validators: 1,
		CreatedAt: time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC),
		Profile:   builtinProfile(t, "xrplevm").Doc,
		Nodes: []Node{{
			Index: 0, Name: "node0", Version: "11.1.1", NodeID: "7fd4befd0aa6669059db3b22696fe49ff3c8e4f5", Validator: "val0",
			Ports: []Port{{File: "config.toml", Key: "rpc.laddr", Port: 26657}, {File: "app.toml", Key: "json-rpc.address", Port: 8545}},
		}},
		Accounts: []Account{{Name: "val0", Address: "ethm1rjx08x0d4dw44a7htfu5hddtp3f4gtv7jknvvp"}},
	}
}

func TestLabYAMLRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := sampleConfig(t)
	if err := Save(dir, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip changed lab.yaml:\n got %+v\nwant %+v", got, want)
	}
	if _, err := got.Profile.Profile(); err != nil {
		t.Fatalf("profile snapshot no longer validates: %v", err)
	}
	if got.Nodes[0].RPCPort() != 26657 {
		t.Fatalf("rpc port %d, want 26657", got.Nodes[0].RPCPort())
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, sampleConfig(t)); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, configFile)
	data, _ := os.ReadFile(path)
	if err := os.WriteFile(path, append(data, []byte("surprise: 1\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "surprise") {
		t.Fatalf("err = %v, want an unknown field error", err)
	}
}

func TestListSkipsPartialLabs(t *testing.T) {
	l := Labs{Dir: t.TempDir()}
	for _, name := range []string{"b", "a"} {
		if err := os.Mkdir(filepath.Join(l.Dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
		c := sampleConfig(t)
		c.Name = name
		if err := Save(filepath.Join(l.Dir, name), c); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(l.Dir, "c"+partialSuffix), 0o755); err != nil {
		t.Fatal(err)
	}
	rows, err := l.List()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range rows {
		names = append(names, r.Name)
	}
	if !reflect.DeepEqual(names, []string{"a", "b"}) {
		t.Fatalf("listed %v, want [a b]", names)
	}
}

// failBinary fails the test if Create gets as far as resolving the binary.
func failBinary(t *testing.T) func(context.Context) (string, error) {
	return func(context.Context) (string, error) {
		t.Error("binary resolved before the cheap checks failed")
		return "", errors.New("unreachable")
	}
}

func TestCreateRefusesAnExistingLab(t *testing.T) {
	l := Labs{Dir: t.TempDir()}
	if err := os.Mkdir(filepath.Join(l.Dir, "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, _, err := l.Create(context.Background(), CreateInput{Name: "demo", Profile: builtinProfile(t, "simd"), Validators: 1, Binary: failBinary(t)})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("err = %v, want already exists", err)
	}
}

func TestCreateFailsOnABusyPortBeforeResolvingTheBinary(t *testing.T) {
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	busy := ln.Addr().(*net.TCPAddr).Port
	p := builtinProfile(t, "simd")
	p.Profile.ExtraPorts = map[string]map[string]int{"app.toml": {"custom.address": busy}}
	l := Labs{Dir: t.TempDir()}
	_, _, err = l.Create(context.Background(), CreateInput{Name: "demo", Profile: p, Validators: 1, Binary: failBinary(t)})
	if err == nil || !strings.Contains(err.Error(), "custom.address") {
		t.Fatalf("err = %v, want the busy port named", err)
	}
	if _, err := os.Stat(filepath.Join(l.Dir, "demo")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lab directory exists after a failed create: %v", err)
	}
}
