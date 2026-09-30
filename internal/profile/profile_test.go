package profile_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kevinpita/forklab/internal/profile"
)

func validDoc() profile.Document {
	return profile.Document{
		Name:         "test",
		BinaryName:   "simd",
		ChainID:      "test-1",
		Bech32Prefix: "cosmos",
		KeyAlgo:      "secp256k1",
		BondDenom:    "stake",
		FeeDenom:     "stake",
		GasPrices:    "0.025stake",
		BlockTime:    "1s",
		Gov:          profile.GovDocument{VotingPeriod: "30s", ExpeditedVotingPeriod: "20s"},
		UpgradeName:  "v{version}",
		Binaries: map[string]profile.BinaryDocument{
			"1.0.0": {URL: "https://example.com/simd_{version}_{os}_{arch}.tar.gz"},
		},
	}
}

func errorPaths(t *testing.T, err error) []string {
	t.Helper()
	var errs profile.Errors
	if !errors.As(err, &errs) {
		t.Fatalf("error %v is not profile.Errors", err)
	}
	paths := make([]string, len(errs))
	for i, e := range errs {
		paths[i] = e.Path
	}
	return paths
}

func TestValidateNamesFieldPath(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*profile.Document)
		want   []string
	}{
		{"missing name", func(d *profile.Document) { d.Name = "" }, []string{"name"}},
		{"name with spaces", func(d *profile.Document) { d.Name = "My Chain" }, []string{"name"}},
		{"missing chain id", func(d *profile.Document) { d.ChainID = "" }, []string{"chain_id"}},
		{"chain id with spaces", func(d *profile.Document) { d.ChainID = "my chain" }, []string{"chain_id"}},
		{"chain id too long", func(d *profile.Document) { d.ChainID = strings.Repeat("a", 51) }, []string{"chain_id"}},
		{"gas prices not a coin", func(d *profile.Document) { d.GasPrices = "cheap" }, []string{"gas_prices"}},
		{"gas prices in another denom", func(d *profile.Document) { d.GasPrices = "1uatom" }, []string{"gas_prices"}},
		{"block time not a duration", func(d *profile.Document) { d.BlockTime = "fast" }, []string{"block_time"}},
		{"block time negative", func(d *profile.Document) { d.BlockTime = "-1s" }, []string{"block_time"}},
		{"missing voting period", func(d *profile.Document) { d.Gov.VotingPeriod = "" }, []string{"gov.voting_period"}},
		{"expedited not shorter", func(d *profile.Document) { d.Gov.ExpeditedVotingPeriod = "30s" }, []string{"gov.expedited_voting_period"}},
		{"unknown template var", func(d *profile.Document) { d.UpgradeName = "v{ver}" }, []string{"upgrade_name"}},
		{"unknown var in export arg", func(d *profile.Document) { d.ExportArgs = []string{"--home", "{home}"} }, []string{"export_args[1]"}},
		{"unknown config file", func(d *profile.Document) {
			d.ExtraPorts = map[string]map[string]int{"node.toml": {"x": 1}}
		}, []string{"extra_ports[node.toml]"}},
		{"port out of range", func(d *profile.Document) {
			d.ExtraPorts = map[string]map[string]int{"app.toml": {"json-rpc.address": 70000}}
		}, []string{"extra_ports[app.toml][json-rpc.address]"}},
		{"no binaries", func(d *profile.Document) { d.Binaries = nil }, []string{"binaries"}},
		{"two sources", func(d *profile.Document) {
			d.Binaries["1.0.0"] = profile.BinaryDocument{URL: "https://x", Path: "/bin/simd"}
		}, []string{"binaries[1.0.0]"}},
		{"no source", func(d *profile.Document) {
			d.Binaries["1.0.0"] = profile.BinaryDocument{Build: "make"}
		}, []string{"binaries[1.0.0]"}},
		{"git without ref build out", func(d *profile.Document) {
			d.Binaries["1.0.0"] = profile.BinaryDocument{Git: "https://github.com/cosmos/cosmos-sdk"}
		}, []string{"binaries[1.0.0].ref", "binaries[1.0.0].build", "binaries[1.0.0].out"}},
		{"src without out", func(d *profile.Document) {
			d.Binaries["dev"] = profile.BinaryDocument{Src: "~/code/sdk", Build: "make build"}
		}, []string{"binaries[dev].out"}},
		{"url with build", func(d *profile.Document) {
			d.Binaries["1.0.0"] = profile.BinaryDocument{URL: "https://x", Build: "make"}
		}, []string{"binaries[1.0.0].build"}},
		{"path with env", func(d *profile.Document) {
			d.Binaries["1.0.0"] = profile.BinaryDocument{Path: "/bin/simd", Env: map[string]string{"A": "b"}}
		}, []string{"binaries[1.0.0].env"}},
		{"src with ref", func(d *profile.Document) {
			d.Binaries["dev"] = profile.BinaryDocument{Src: ".", Ref: "main", Build: "make", Out: "simd"}
		}, []string{"binaries[dev].ref"}},
		{"bad env name", func(d *profile.Document) {
			d.Binaries["dev"] = profile.BinaryDocument{Src: ".", Build: "make", Out: "simd", Env: map[string]string{"1BAD": "x"}}
		}, []string{"binaries[dev].env[1BAD]"}},
		{"version escapes cache dir", func(d *profile.Document) {
			d.Binaries["../x"] = profile.BinaryDocument{Path: "/bin/simd"}
		}, []string{"binaries[../x]"}},
		{"unknown var in url", func(d *profile.Document) {
			d.Binaries["1.0.0"] = profile.BinaryDocument{URL: "https://x/{tag}"}
		}, []string{"binaries[1.0.0].url"}},
		{"empty snapshot url", func(d *profile.Document) {
			d.Snapshots = map[string]string{"local": ""}
		}, []string{"snapshots[local]"}},
		{"jq syntax error", func(d *profile.Document) {
			d.ForkPatches = []string{".a = 1", ".app_state |= map("}
		}, []string{"fork_patches[1]"}},
		{"jq syntax error in fresh patch", func(d *profile.Document) {
			d.FreshPatches = []string{".", "}"}
		}, []string{"fresh_patches[1]"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := validDoc()
			tt.mutate(&d)
			_, err := d.Profile()
			if got := errorPaths(t, err); !slices.Equal(got, tt.want) {
				t.Errorf("error paths = %q, want %q\n%v", got, tt.want, err)
			}
		})
	}
}

func TestProfileIsTyped(t *testing.T) {
	d := validDoc()
	d.Binaries["2.0.0"] = profile.BinaryDocument{
		Git: "https://github.com/cosmos/cosmos-sdk", Ref: "v{version}", Build: "make build", Out: "build/simd",
		Env: map[string]string{"CGO_ENABLED": "0"},
	}
	p, err := d.Profile()
	if err != nil {
		t.Fatal(err)
	}
	if p.GasPrices != (profile.Coin{Amount: "0.025", Denom: "stake"}) {
		t.Errorf("gas prices = %+v", p.GasPrices)
	}
	if p.BlockTime != time.Second || p.Gov.VotingPeriod != 30*time.Second || p.Gov.ExpeditedVotingPeriod != 20*time.Second {
		t.Errorf("durations = %v %+v", p.BlockTime, p.Gov)
	}
	git, ok := p.Binaries["2.0.0"].(profile.GitSource)
	if !ok || git.Ref != "v{version}" || git.Out != "build/simd" || git.Env["CGO_ENABLED"] != "0" {
		t.Errorf("binaries[2.0.0] = %#v", p.Binaries["2.0.0"])
	}
	if _, ok := p.Binaries["1.0.0"].(profile.URLSource); !ok {
		t.Errorf("binaries[1.0.0] = %#v, want URLSource", p.Binaries["1.0.0"])
	}
}

func TestDecodeNamesLine(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want []string
	}{
		{"unknown top-level field", "name: x\nbogus: 1\n", []string{"line 2: field bogus not found"}},
		{"unknown nested field", "gov:\n  voting_period: 1s\n  nope: 2\n", []string{"line 3: field nope not found"}},
		{"wrong scalar type", "extra_ports:\n  app.toml: { a: 1, b: high }\n", []string{"line 2: cannot unmarshal !!str `high` into int"}},
		{"several at once", "x: 1\ngov: { y: 2 }\n", []string{"line 1: field x not found", "line 2: field y not found"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := profile.Decode([]byte(tt.yaml))
			if err == nil {
				t.Fatal("want an error")
			}
			for _, w := range tt.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not contain %q", err, w)
				}
			}
		})
	}
}

func TestDecodeRejects(t *testing.T) {
	for name, in := range map[string]string{
		"empty":          "",
		"two documents":  "name: a\n---\nname: b\n",
		"invalid syntax": "name: [\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := profile.Decode([]byte(in)); err == nil {
				t.Error("want an error")
			}
		})
	}
}

func TestDecodeUnquotedNumberIntoString(t *testing.T) {
	d, err := profile.Decode([]byte("binaries:\n  dev: { src: ., env: { CGO_ENABLED: 0 } }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := d.Binaries["dev"].Env["CGO_ENABLED"]; got != "0" {
		t.Errorf("env = %q, want \"0\"", got)
	}
}

func TestTemplateExpand(t *testing.T) {
	vars := profile.Vars{Version: "8.0.0", OS: "linux", Arch: "amd64", ChainID: "xrplevm_1440000-1"}
	tests := []struct{ in, want string }{
		{"https://x/v{version}/node_{version}_{os}_{arch}.tar.gz", "https://x/v8.0.0/node_8.0.0_linux_amd64.tar.gz"},
		{"{chain_id}", "xrplevm_1440000-1"},
		{"node_{version}_{Os}_{arch}.tar.gz", "node_8.0.0_Linux_amd64.tar.gz"},
		{"no vars", "no vars"},
		{"${HOME}/{nope}", "${HOME}/{nope}"},
	}
	for _, tt := range tests {
		if got := profile.Template(tt.in).Expand(vars); got != tt.want {
			t.Errorf("Expand(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestBuiltins(t *testing.T) {
	s := profile.Store{Dir: t.TempDir()}
	x, err := s.Get("xrplevm")
	if err != nil {
		t.Fatal(err)
	}
	if x.Origin != profile.OriginBuiltin || x.Profile.GasPrices.String() != "800000000000axrp" ||
		x.Profile.ExtraPorts["app.toml"]["json-rpc.ws-address"] != 8546 || len(x.Profile.ForkPatches) != 3 || len(x.Profile.FreshPatches) == 0 {
		t.Errorf("xrplevm = %+v", x)
	}
	url, ok := x.Profile.Binaries["11.2.0"].(profile.URLSource)
	want := "https://github.com/xrplevm/node/releases/download/v11.2.0/node_11.2.0_Linux_amd64.tar.gz"
	if got := url.URL.Expand(profile.Vars{Version: "11.2.0", OS: "linux", Arch: "amd64"}); !ok || got != want {
		t.Errorf("xrplevm binaries[11.2.0] = %#v, expands to %q", x.Profile.Binaries["11.2.0"], got)
	}

	simd, err := s.Get("simd")
	if err != nil {
		t.Fatal(err)
	}
	git, ok := simd.Profile.Binaries["0.53.8"].(profile.GitSource)
	if !ok || git.Ref.Expand(profile.Vars{Version: "0.53.8"}) != "v0.53.8" || git.Out != "simapp/build/simd" ||
		len(git.Env) != 2 || git.Env["GOTOOLCHAIN"] != "go1.23.6" || git.Env["CGO_ENABLED"] != "0" {
		t.Errorf("simd binaries[0.53.8] = %#v", simd.Profile.Binaries["0.53.8"])
	}
	if simd.Profile.Bech32Prefix != "cosmos" || simd.Profile.KeyAlgo != "secp256k1" || simd.Profile.BondDenom != "stake" {
		t.Errorf("simd = %+v", simd.Profile)
	}
}

func TestStoreShadowing(t *testing.T) {
	s := profile.Store{Dir: t.TempDir()}
	d := validDoc()
	d.Name = "xrplevm"
	d.ChainID = "mine-1"
	if _, err := s.Save(d); err != nil {
		t.Fatal(err)
	}

	e, err := s.Get("xrplevm")
	if err != nil {
		t.Fatal(err)
	}
	if e.Origin != profile.OriginUser || e.Profile.ChainID != "mine-1" {
		t.Errorf("Get = %s %s, want user profile shadowing the built-in", e.Origin, e.Profile.ChainID)
	}
	l, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(l) != 2 || l[1].Name != "xrplevm" || l[1].Origin != profile.OriginUser || !l[1].ShadowsBuiltin {
		t.Errorf("List = %+v", l)
	}

	if _, err := s.Delete("xrplevm"); err != nil {
		t.Fatal(err)
	}
	if e, err := s.Get("xrplevm"); err != nil || e.Origin != profile.OriginBuiltin {
		t.Errorf("after delete: origin %s, err %v; want the built-in back", e.Origin, err)
	}
	if _, err := s.Delete("xrplevm"); err == nil || !strings.Contains(err.Error(), "built in") {
		t.Errorf("deleting a built-in: err = %v", err)
	}
	if _, err := s.Delete("nope"); !errors.Is(err, profile.ErrNotFound) {
		t.Errorf("deleting a missing profile: err = %v", err)
	}
}

func TestStoreInvalidUserProfileDoesNotFallBack(t *testing.T) {
	s := profile.Store{Dir: t.TempDir()}
	if err := os.WriteFile(filepath.Join(s.Dir, "simd.yaml"), []byte("name: simd\nbogus: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("simd"); err == nil || !strings.Contains(err.Error(), "line 2: field bogus not found") {
		t.Errorf("Get = %v, want the user file's error", err)
	}
	l, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if l[0].Name != "simd" || l[0].Error == "" {
		t.Errorf("List = %+v, want simd flagged invalid", l)
	}
}

func TestStoreNameMustMatchFile(t *testing.T) {
	s := profile.Store{Dir: t.TempDir()}
	d := validDoc()
	d.BlockTime = ""
	data, err := d.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Dir, "other.yaml"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = s.Get("other")
	if got := errorPaths(t, err); !slices.Equal(got, []string{"name"}) {
		t.Errorf("paths = %q", got)
	}
}

func TestStoreRejectsPathNames(t *testing.T) {
	s := profile.Store{Dir: t.TempDir()}
	if _, err := s.Get("../etc/passwd"); err == nil {
		t.Error("Get accepted a path as a name")
	}
}

func TestSaveRoundTrip(t *testing.T) {
	s := profile.Store{Dir: t.TempDir()}
	d := validDoc()
	d.ExtraPorts = map[string]map[string]int{"app.toml": {"json-rpc.address": 8545}}
	d.ForkPatches = []string{".a = 1"}
	if _, err := s.Save(d); err != nil {
		t.Fatal(err)
	}
	e, err := s.Get("test")
	if err != nil {
		t.Fatal(err)
	}
	if e.Doc.ExtraPorts["app.toml"]["json-rpc.address"] != 8545 || e.Doc.ForkPatches[0] != ".a = 1" || e.Doc.Binaries["1.0.0"].URL != d.Binaries["1.0.0"].URL {
		t.Errorf("round trip = %+v", e.Doc)
	}

	bad := validDoc()
	bad.Name = "bad"
	bad.BlockTime = "soon"
	if _, err := s.Save(bad); err == nil {
		t.Error("Save accepted an invalid profile")
	}
	if _, err := os.Stat(filepath.Join(s.Dir, "bad.yaml")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("invalid profile was written: %v", err)
	}
}

func TestDefaultStoreDir(t *testing.T) {
	tests := []struct {
		name               string
		forklab, xdg, home string
		want               string
	}{
		{"override", "/f", "/x", "/h", "/f/profiles"},
		{"xdg", "", "/x", "/h", "/x/forklab/profiles"},
		{"home", "", "", "/h", "/h/.config/forklab/profiles"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("FORKLAB_CONFIG_DIR", tt.forklab)
			t.Setenv("XDG_CONFIG_HOME", tt.xdg)
			t.Setenv("HOME", tt.home)
			s, err := profile.DefaultStore()
			if err != nil || s.Dir != tt.want {
				t.Errorf("Dir = %q, %v; want %q", s.Dir, err, tt.want)
			}
		})
	}
}
