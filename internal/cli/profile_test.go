package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kevinpita/forklab/internal/profile"
)

type profileEnvelope struct {
	OK   bool `json:"ok"`
	Data struct {
		Name           string           `json:"name"`
		Origin         string           `json:"origin"`
		Path           string           `json:"path"`
		Profile        profile.Document `json:"profile"`
		BuiltinVisible bool             `json:"builtin_visible"`
	} `json:"data"`
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func runJSON(t *testing.T, wantCode int, args ...string) profileEnvelope {
	t.Helper()
	code, stdout, stderr := run(append(args, "--json")...)
	if code != wantCode {
		t.Fatalf("%v: code = %d, want %d\nstdout: %s\nstderr: %s", args, code, wantCode, stdout, stderr)
	}
	var env profileEnvelope
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("%v: stdout is not an envelope: %v: %q", args, err, stdout)
	}
	return env
}

func configDir(t *testing.T) string {
	dir := t.TempDir()
	t.Setenv("FORKLAB_CONFIG_DIR", dir)
	return dir
}

func TestProfileRoundTrip(t *testing.T) {
	dir := configDir(t)
	file := filepath.Join(dir, "profiles", "mychain.yaml")

	runJSON(t, 0, "profile", "create", "mychain",
		"--binary-name", "mychaind",
		"--chain-id", "mychain-1",
		"--bech32-prefix", "my",
		"--key-algo", "secp256k1",
		"--bond-denom", "umy",
		"--fee-denom", "umy",
		"--gas-prices", "0.01umy",
		"--block-time", "2s",
		"--voting-period", "40s",
		"--upgrade-name", "v{version}",
		"--export-arg", "--chain-id", "--export-arg", "{chain_id}",
		"--extra-port", "app.toml:json-rpc.address=8545",
		"--binary", "1.0.0=url:https://example.com/mychain_{version}_{os}_{arch}.tar.gz",
		"--binary", "2.0.0=git:https://github.com/me/mychain",
		"--binary-ref", "2.0.0=v{version}",
		"--binary-build", "2.0.0=make build LDFLAGS=-X=a,b",
		"--binary-out", "2.0.0=build/mychaind",
		"--binary-env", "2.0.0=CGO_ENABLED=0",
		"--snapshot", "local=/tmp/snap.tar.lz4",
		"--fresh-patch", ".app_state.x = 1",
		"--fork-patch", ".app_state.y = 2", "--fork-patch", ".app_state.z = 3",
	)
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("create did not write %s: %v", file, err)
	}

	shown := runJSON(t, 0, "profile", "show", "mychain")
	p := shown.Data.Profile
	if shown.Data.Origin != "user" || shown.Data.Path != file {
		t.Errorf("show origin %q path %q", shown.Data.Origin, shown.Data.Path)
	}
	if p.ChainID != "mychain-1" || p.GasPrices != "0.01umy" || p.Gov.VotingPeriod != "40s" ||
		strings.Join(p.ExportArgs, " ") != "--chain-id {chain_id}" ||
		p.ExtraPorts["app.toml"]["json-rpc.address"] != 8545 ||
		p.Snapshots["local"] != "/tmp/snap.tar.lz4" || len(p.FreshPatches) != 1 || len(p.ForkPatches) != 2 {
		t.Errorf("show profile = %+v", p)
	}
	git := p.Binaries["2.0.0"]
	if git.Git != "https://github.com/me/mychain" || git.Ref != "v{version}" || git.Build != "make build LDFLAGS=-X=a,b" ||
		git.Out != "build/mychaind" || git.Env["CGO_ENABLED"] != "0" || p.Binaries["1.0.0"].URL == "" {
		t.Errorf("show binaries = %+v", p.Binaries)
	}

	runJSON(t, 0, "profile", "edit", "mychain",
		"--block-time", "500ms",
		"--remove-binary", "1.0.0",
		"--no-fork-patches",
		"--remove-extra-port", "app.toml:json-rpc.address",
	)
	p = runJSON(t, 0, "profile", "show", "mychain").Data.Profile
	if p.BlockTime != "500ms" || len(p.Binaries) != 1 || p.ForkPatches != nil || len(p.FreshPatches) != 1 || p.ExtraPorts != nil || p.ChainID != "mychain-1" {
		t.Errorf("after edit = %+v", p)
	}

	if v := runJSON(t, 0, "profile", "validate", "mychain"); v.Data.Origin != "user" {
		t.Errorf("validate by name = %+v", v.Data)
	}
	if v := runJSON(t, 0, "profile", "validate", file); v.Data.Origin != "file" || v.Data.Name != "mychain" {
		t.Errorf("validate by file = %+v", v.Data)
	}

	if d := runJSON(t, 0, "profile", "delete", "mychain"); d.Data.Path != file || d.Data.BuiltinVisible {
		t.Errorf("delete = %+v", d.Data)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Errorf("file still there after delete: %v", err)
	}
	runJSON(t, 1, "profile", "show", "mychain")
}

func TestProfileCreateFromBuiltinThenEditShadows(t *testing.T) {
	configDir(t)
	created := runJSON(t, 0, "profile", "create", "mysimd", "--from", "simd", "--chain-id", "mysimd-1")
	if p := created.Data.Profile; p.Name != "mysimd" || p.ChainID != "mysimd-1" || p.BinaryName != "simd" {
		t.Errorf("clone = %+v", p)
	}
	runJSON(t, 1, "profile", "create", "mysimd", "--from", "simd")

	edited := runJSON(t, 0, "profile", "edit", "simd", "--chain-id", "local-1")
	if edited.Data.Origin != "user" {
		t.Errorf("editing a built-in saved origin %q, want user", edited.Data.Origin)
	}
	if got := runJSON(t, 0, "profile", "show", "simd").Data.Profile.ChainID; got != "local-1" {
		t.Errorf("shadowing chain_id = %q", got)
	}
	if d := runJSON(t, 0, "profile", "delete", "simd"); !d.Data.BuiltinVisible {
		t.Errorf("delete of the shadow = %+v", d.Data)
	}
	if got := runJSON(t, 0, "profile", "show", "simd").Data.Origin; got != "builtin" {
		t.Errorf("after delete origin = %q", got)
	}
	runJSON(t, 1, "profile", "delete", "simd")
}

func TestProfileList(t *testing.T) {
	configDir(t)
	runJSON(t, 0, "profile", "create", "xrplevm", "--from", "xrplevm", "--block-time", "2s")
	code, stdout, _ := run("profile", "list", "--json")
	var env struct {
		Data []profile.Listing `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); code != 0 || err != nil {
		t.Fatalf("code %d, err %v", code, err)
	}
	if len(env.Data) != 2 || env.Data[0].Name != "simd" || env.Data[0].Origin != "builtin" ||
		env.Data[1].Name != "xrplevm" || !env.Data[1].ShadowsBuiltin {
		t.Errorf("list = %+v", env.Data)
	}
}

func TestProfileUsageErrors(t *testing.T) {
	configDir(t)
	tests := []struct {
		name    string
		args    []string
		wantMsg string
	}{
		{"create with missing fields names the flags", []string{"profile", "create", "x", "--binary-name", "xd"}, "chain_id: required (set with --chain-id)"},
		{"bad binary kind", []string{"profile", "create", "x", "--binary", "1.0=ftp:x"}, `kind "ftp"`},
		{"bad extra port", []string{"profile", "create", "x", "--extra-port", "app.toml:a=high"}, "FILE:KEY=PORT"},
		{"env for missing binary", []string{"profile", "edit", "simd", "--binary-env", "9.9.9=A=b"}, "no binary 9.9.9"},
		{"edit without flags", []string{"profile", "edit", "simd"}, "no field flags"},
		{"invalid edit names the path", []string{"profile", "edit", "simd", "--binary", "0.53.8=git:https://x"}, "binaries[0.53.8].ref: required (set with --binary-ref)"},
		{"bad url template names --binary", []string{"profile", "edit", "simd", "--binary", "1.0=url:https://x/{nope}"}, "unknown template variable {nope}; known: {version} {os} {Os} {arch} {chain_id} (set with --binary)"},
		{"remove missing binary", []string{"profile", "edit", "simd", "--remove-binary", "9.9.9"}, "no binary 9.9.9"},
		{"remove missing snapshot", []string{"profile", "edit", "simd", "--remove-snapshot", "nope"}, "no snapshot nope"},
		{"field for missing binary", []string{"profile", "edit", "simd", "--binary-out", "9.9.9=x"}, "no binary 9.9.9"},
		{"missing name", []string{"profile", "show"}, "accepts 1 arg"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := runJSON(t, 2, tt.args...)
			if env.Error.Code != "usage" || !strings.Contains(env.Error.Message, tt.wantMsg) {
				t.Errorf("error = %+v, want message containing %q", env.Error, tt.wantMsg)
			}
		})
	}
}

func TestProfileValidateInvalidFile(t *testing.T) {
	dir := configDir(t)
	file := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(file, []byte("name: bad\nblock_tme: 1s\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := runJSON(t, 1, "profile", "validate", file)
	if !strings.Contains(env.Error.Message, "line 2: field block_tme not found") {
		t.Errorf("error = %+v", env.Error)
	}
}
