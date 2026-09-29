package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/kevinpita/forklab/internal/cli/output"
	"github.com/kevinpita/forklab/internal/profile"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func newProfileCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profile",
		Short: "Manage chain profiles",
	}
	cmd.AddCommand(
		newProfileListCmd(a),
		newProfileShowCmd(a),
		newProfileValidateCmd(a),
		newProfileCreateCmd(a),
		newProfileEditCmd(a),
		newProfileDeleteCmd(a),
	)
	return cmd
}

type profileView struct {
	Name    string           `json:"name"`
	Origin  profile.Origin   `json:"origin"`
	Path    string           `json:"path,omitempty"`
	Profile profile.Document `json:"profile"`
	// action is the past-tense verb for create and edit; show prints YAML.
	action string
}

func newProfileView(e profile.Entry, action string) profileView {
	return profileView{Name: e.Doc.Name, Origin: e.Origin, Path: e.Path, Profile: e.Doc, action: action}
}

func (v profileView) WriteHuman(w io.Writer) error {
	if v.action != "" {
		_, err := fmt.Fprintf(w, "%s profile %s at %s\n", v.action, v.Name, v.Path)
		return err
	}
	data, err := v.Profile.Marshal()
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

type profileList []profile.Listing

func (l profileList) WriteHuman(w io.Writer) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAME\tORIGIN\tSTATUS")
	for _, p := range l {
		origin := string(p.Origin)
		if p.ShadowsBuiltin {
			origin += " (shadows builtin)"
		}
		status := "ok"
		if p.Error != "" {
			status = "invalid"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", p.Name, origin, status)
	}
	return tw.Flush()
}

type validView struct {
	Name   string `json:"name"`
	Origin string `json:"origin"`
	Path   string `json:"path,omitempty"`
}

func (v validView) WriteHuman(w io.Writer) error {
	_, err := fmt.Fprintf(w, "profile %s is valid (%s)\n", v.Name, v.Origin)
	return err
}

type deletedView struct {
	Name           string `json:"name"`
	Path           string `json:"path"`
	BuiltinVisible bool   `json:"builtin_visible"`
}

func (v deletedView) WriteHuman(w io.Writer) error {
	_, err := fmt.Fprintf(w, "deleted profile %s at %s\n", v.Name, v.Path)
	if err == nil && v.BuiltinVisible {
		_, err = fmt.Fprintf(w, "built-in profile %s is visible again\n", v.Name)
	}
	return err
}

func newProfileListCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List user and built-in profiles",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := profile.DefaultStore()
			if err != nil {
				return err
			}
			l, err := s.List()
			if err != nil {
				return err
			}
			return a.print(cmd, profileList(l))
		},
	}
}

func newProfileShowCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "show <name>",
		Short: "Print a profile as YAML",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := profile.DefaultStore()
			if err != nil {
				return err
			}
			e, err := s.Get(args[0])
			if err != nil {
				return err
			}
			return a.print(cmd, newProfileView(e, ""))
		},
	}
}

func newProfileValidateCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "validate <file|name>",
		Short: "Validate a profile file, or a stored profile by name",
		Long:  "An argument containing a path separator or ending in .yaml or .yml is a file; anything else is a profile name.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			arg := args[0]
			if strings.ContainsRune(arg, os.PathSeparator) || strings.HasSuffix(arg, ".yaml") || strings.HasSuffix(arg, ".yml") {
				data, err := os.ReadFile(arg)
				if err != nil {
					return err
				}
				d, _, err := profile.Load(data)
				if err != nil {
					return fmt.Errorf("profile file %s:\n%w", arg, err)
				}
				return a.print(cmd, validView{Name: d.Name, Origin: "file", Path: arg})
			}
			s, err := profile.DefaultStore()
			if err != nil {
				return err
			}
			e, err := s.Get(arg)
			if err != nil {
				return err
			}
			return a.print(cmd, validView{Name: e.Doc.Name, Origin: string(e.Origin), Path: e.Path})
		},
	}
}

func newProfileCreateCmd(a *app) *cobra.Command {
	var from string
	var f docFlags
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a user profile from flags, optionally cloning another profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			s, err := profile.DefaultStore()
			if err != nil {
				return err
			}
			if s.HasUser(name) {
				return fmt.Errorf("profile %s already exists; use profile edit", name)
			}
			d := profile.Document{}
			if from != "" {
				base, err := s.Get(from)
				if err != nil {
					return err
				}
				d = base.Doc
			}
			d.Name = name
			if err := f.apply(cmd.Flags(), &d); err != nil {
				return err
			}
			e, err := s.Save(d)
			if err != nil {
				return withFlagHints(err)
			}
			return a.print(cmd, newProfileView(e, "created"))
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "clone this profile (user or built-in) as the starting point")
	f.register(cmd.Flags())
	return cmd
}

func newProfileEditCmd(a *app) *cobra.Command {
	var f docFlags
	cmd := &cobra.Command{
		Use:   "edit <name>",
		Short: "Change fields of a profile; editing a built-in saves a user copy that shadows it",
		Long: "Change fields of a profile; editing a built-in saves a user copy that shadows it.\n" +
			"The profile is rewritten from its parsed fields, so YAML comments and formatting are not preserved.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := profile.DefaultStore()
			if err != nil {
				return err
			}
			e, err := s.Get(args[0])
			if err != nil {
				return err
			}
			changed := false
			cmd.Flags().Visit(func(fl *pflag.Flag) {
				changed = changed || cmd.LocalNonPersistentFlags().Lookup(fl.Name) != nil
			})
			if !changed {
				return output.Usagef("no field flags given; see profile edit --help")
			}
			d := e.Doc
			if err := f.apply(cmd.Flags(), &d); err != nil {
				return err
			}
			saved, err := s.Save(d)
			if err != nil {
				return withFlagHints(err)
			}
			return a.print(cmd, newProfileView(saved, "edited"))
		},
	}
	f.register(cmd.Flags())
	return cmd
}

func newProfileDeleteCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a user profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := profile.DefaultStore()
			if err != nil {
				return err
			}
			path, err := s.Delete(args[0])
			if err != nil {
				return err
			}
			return a.print(cmd, deletedView{Name: args[0], Path: path, BuiltinVisible: profile.IsBuiltin(args[0])})
		},
	}
}

// scalarFlags maps each single-value profile field to its flag.
var scalarFlags = []struct {
	flag, field, usage string
	set                func(*profile.Document, string)
}{
	{"binary-name", "binary_name", "chain binary name, such as simd", func(d *profile.Document, v string) { d.BinaryName = v }},
	{"chain-id", "chain_id", "chain ID for fresh labs", func(d *profile.Document, v string) { d.ChainID = v }},
	{"bech32-prefix", "bech32_prefix", "account address prefix, such as cosmos", func(d *profile.Document, v string) { d.Bech32Prefix = v }},
	{"key-algo", "key_algo", "keyring algorithm, such as secp256k1", func(d *profile.Document, v string) { d.KeyAlgo = v }},
	{"bond-denom", "bond_denom", "staking denom", func(d *profile.Document, v string) { d.BondDenom = v }},
	{"fee-denom", "fee_denom", "fee denom", func(d *profile.Document, v string) { d.FeeDenom = v }},
	{"gas-prices", "gas_prices", "gas price coin, such as 0.025stake", func(d *profile.Document, v string) { d.GasPrices = v }},
	{"block-time", "block_time", "target block time, such as 1s", func(d *profile.Document, v string) { d.BlockTime = v }},
	{"voting-period", "gov.voting_period", "gov voting period, such as 30s", func(d *profile.Document, v string) { d.Gov.VotingPeriod = v }},
	{"expedited-voting-period", "gov.expedited_voting_period", "gov expedited voting period; empty unsets it", func(d *profile.Document, v string) { d.Gov.ExpeditedVotingPeriod = v }},
	{"upgrade-name", "upgrade_name", "upgrade plan name template, such as v{version}", func(d *profile.Document, v string) { d.UpgradeName = v }},
}

// listFlags maps field path prefixes of list and map fields to their flag.
var listFlags = []struct{ prefix, flag string }{
	{"export_args", "export-arg"},
	{"extra_ports", "extra-port"},
	{"binaries", "binary"},
	{"snapshots", "snapshot"},
	{"fresh_patches", "fresh-patch"},
	{"fork_patches", "fork-patch"},
}

type docFlags struct {
	exportArgs, freshPatches, forkPatches       []string
	noExportArgs, noFreshPatches, noForkPatches bool
	binaries, snapshots, extraPorts             []string
	binaryFields                                [len(binaryFieldFlags)][]string
	removeBinaries, removeSnapshots             []string
	removeExtraPorts                            []string
}

// binaryFieldFlags set one field of an existing binary entry, as
// --flag VERSION=VALUE, after --binary has created or replaced the entry.
var binaryFieldFlags = [...]struct {
	flag, usage string
	set         func(*profile.BinaryDocument, string) error
}{
	{"binary-ref", "VERSION=REF git ref to build", func(b *profile.BinaryDocument, v string) error { b.Ref = v; return nil }},
	{"binary-build", "VERSION=CMD build command for a git or src binary", func(b *profile.BinaryDocument, v string) error { b.Build = v; return nil }},
	{"binary-out", "VERSION=PATH built binary path for a git or src binary", func(b *profile.BinaryDocument, v string) error { b.Out = v; return nil }},
	{"binary-env", "VERSION=KEY=VALUE build environment for a git or src binary", func(b *profile.BinaryDocument, v string) error {
		key, value, ok := strings.Cut(v, "=")
		if !ok || key == "" {
			return errors.New("want VERSION=KEY=VALUE")
		}
		if b.Env == nil {
			b.Env = map[string]string{}
		}
		b.Env[key] = value
		return nil
	}},
}

func (f *docFlags) register(fs *pflag.FlagSet) {
	for _, s := range scalarFlags {
		fs.String(s.flag, "", s.usage)
	}
	fs.StringArrayVar(&f.exportArgs, "export-arg", nil, "export argument template; repeat for each, replaces the list")
	fs.BoolVar(&f.noExportArgs, "no-export-args", false, "clear export_args")
	fs.StringArrayVar(&f.freshPatches, "fresh-patch", nil, "gojq patch for fresh genesis; repeat for each, replaces the list")
	fs.BoolVar(&f.noFreshPatches, "no-fresh-patches", false, "clear fresh_patches")
	fs.StringArrayVar(&f.forkPatches, "fork-patch", nil, "gojq patch for forked genesis; repeat for each, replaces the list")
	fs.BoolVar(&f.noForkPatches, "no-fork-patches", false, "clear fork_patches")
	fs.StringArrayVar(&f.binaries, "binary", nil, "VERSION=KIND:LOCATION, KIND is url|path|git|src; replaces that version")
	for i, b := range binaryFieldFlags {
		fs.StringArrayVar(&f.binaryFields[i], b.flag, nil, b.usage)
	}
	fs.StringArrayVar(&f.removeBinaries, "remove-binary", nil, "VERSION to remove")
	fs.StringArrayVar(&f.snapshots, "snapshot", nil, "NAME=URL")
	fs.StringArrayVar(&f.removeSnapshots, "remove-snapshot", nil, "NAME to remove")
	fs.StringArrayVar(&f.extraPorts, "extra-port", nil, "FILE:KEY=PORT, such as app.toml:json-rpc.address=8545")
	fs.StringArrayVar(&f.removeExtraPorts, "remove-extra-port", nil, "FILE:KEY to remove")
}

// apply sets every changed flag on d. Removals run before additions so a
// flag pair can replace an entry.
func (f *docFlags) apply(fs *pflag.FlagSet, d *profile.Document) error {
	for _, s := range scalarFlags {
		if fs.Changed(s.flag) {
			v, _ := fs.GetString(s.flag)
			s.set(d, v)
		}
	}

	if f.noExportArgs {
		d.ExportArgs = nil
	}
	if len(f.exportArgs) > 0 {
		d.ExportArgs = f.exportArgs
	}
	if f.noFreshPatches {
		d.FreshPatches = nil
	}
	if len(f.freshPatches) > 0 {
		d.FreshPatches = f.freshPatches
	}
	if f.noForkPatches {
		d.ForkPatches = nil
	}
	if len(f.forkPatches) > 0 {
		d.ForkPatches = f.forkPatches
	}

	for _, v := range f.removeBinaries {
		if _, ok := d.Binaries[v]; !ok {
			return output.Usagef("--remove-binary %q: no binary %s in the profile", v, v)
		}
		delete(d.Binaries, v)
	}
	for _, spec := range f.binaries {
		version, b, err := parseBinaryFlag(spec)
		if err != nil {
			return err
		}
		if d.Binaries == nil {
			d.Binaries = map[string]profile.BinaryDocument{}
		}
		d.Binaries[version] = b
	}
	for i, field := range binaryFieldFlags {
		for _, spec := range f.binaryFields[i] {
			version, value, ok := strings.Cut(spec, "=")
			if !ok {
				return output.Usagef("--%s %q: want %s", field.flag, spec, field.usage)
			}
			b, found := d.Binaries[version]
			if !found {
				return output.Usagef("--%s %q: no binary %s in the profile", field.flag, spec, version)
			}
			if err := field.set(&b, value); err != nil {
				return output.Usagef("--%s %q: %v", field.flag, spec, err)
			}
			d.Binaries[version] = b
		}
	}

	for _, name := range f.removeSnapshots {
		if _, ok := d.Snapshots[name]; !ok {
			return output.Usagef("--remove-snapshot %q: no snapshot %s in the profile", name, name)
		}
		delete(d.Snapshots, name)
	}
	for _, spec := range f.snapshots {
		name, url, ok := strings.Cut(spec, "=")
		if !ok || name == "" {
			return output.Usagef("--snapshot %q: want NAME=URL", spec)
		}
		if d.Snapshots == nil {
			d.Snapshots = map[string]string{}
		}
		d.Snapshots[name] = url
	}

	for _, spec := range f.removeExtraPorts {
		file, key, ok := strings.Cut(spec, ":")
		if !ok {
			return output.Usagef("--remove-extra-port %q: want FILE:KEY", spec)
		}
		if _, ok := d.ExtraPorts[file][key]; !ok {
			return output.Usagef("--remove-extra-port %q: no extra port %s in %s", spec, key, file)
		}
		delete(d.ExtraPorts[file], key)
		if len(d.ExtraPorts[file]) == 0 {
			delete(d.ExtraPorts, file)
		}
	}
	for _, spec := range f.extraPorts {
		file, rest, ok := strings.Cut(spec, ":")
		key, portStr, ok2 := strings.Cut(rest, "=")
		port, err := strconv.Atoi(portStr)
		if !ok || !ok2 || file == "" || key == "" || err != nil {
			return output.Usagef("--extra-port %q: want FILE:KEY=PORT", spec)
		}
		if d.ExtraPorts == nil {
			d.ExtraPorts = map[string]map[string]int{}
		}
		if d.ExtraPorts[file] == nil {
			d.ExtraPorts[file] = map[string]int{}
		}
		d.ExtraPorts[file][key] = port
	}
	return nil
}

// parseBinaryFlag parses VERSION=KIND:LOCATION.
func parseBinaryFlag(spec string) (string, profile.BinaryDocument, error) {
	var b profile.BinaryDocument
	version, rest, ok := strings.Cut(spec, "=")
	kind, location, ok2 := strings.Cut(rest, ":")
	if !ok || !ok2 || version == "" || location == "" {
		return "", b, output.Usagef("--binary %q: want VERSION=KIND:LOCATION", spec)
	}
	switch kind {
	case "url":
		b.URL = location
	case "path":
		b.Path = location
	case "git":
		b.Git = location
	case "src":
		b.Src = location
	default:
		return "", b, output.Usagef("--binary %q: kind %q is not url, path, git, or src", spec, kind)
	}
	return version, b, nil
}

// withFlagHints marks profile validation errors from create and edit as usage
// errors and names the flag that sets each field.
func withFlagHints(err error) error {
	var errs profile.Errors
	if !errors.As(err, &errs) {
		return err
	}
	hinted := make(profile.Errors, len(errs))
	for i, e := range errs {
		if flag := flagFor(e.Path); flag != "" {
			e.Message += " (set with --" + flag + ")"
		}
		hinted[i] = e
	}
	return output.Usage(fmt.Errorf("invalid profile:\n%w", hinted))
}

func flagFor(path string) string {
	for _, s := range scalarFlags {
		if s.field == path {
			return s.flag
		}
	}
	for _, l := range listFlags {
		if strings.HasPrefix(path, l.prefix) {
			if _, field, ok := strings.Cut(path, "]."); ok && l.flag == "binary" {
				field, _, _ = strings.Cut(field, "[")
				if field == "ref" || field == "build" || field == "out" || field == "env" {
					return "binary-" + field
				}
			}
			return l.flag
		}
	}
	return ""
}
