package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/kevinpita/forklab/internal/binary"
	"github.com/kevinpita/forklab/internal/cli/output"
	"github.com/kevinpita/forklab/internal/lab"
	"github.com/kevinpita/forklab/internal/paths"
	"github.com/kevinpita/forklab/internal/profile"
	taskprogress "github.com/kevinpita/forklab/internal/progress"
	"github.com/kevinpita/forklab/internal/snapshot"
	"github.com/kevinpita/forklab/internal/supervisor"
	"github.com/spf13/cobra"
)

func labs() (lab.Labs, error) {
	home, err := paths.Home()
	return lab.Labs{Dir: filepath.Join(home, "labs")}, err
}

func newLabCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "lab",
		Short: "Create, run, reset, and delete labs",
	}
	cmd.AddCommand(newLabCreateCmd(a), newLabListCmd(a), newLabShowCmd(a), newLabUpCmd(a), newLabDownCmd(a), newLabResetCmd(a), newLabDeleteCmd(a), newLabPauseCmd(a), newLabResumeCmd(a))
	return cmd
}

// running reports whether any of the lab's node processes is live, by the
// supervisor when one answers, else by the pid files. A supervisor alone does
// not make a lab running.
func running(dir string) bool {
	if c, err := supervisor.Dial(dir); err == nil {
		if nodes, err := c.Status(); err == nil {
			return slices.ContainsFunc(nodes, func(n supervisor.NodeStatus) bool { return n.State == supervisor.StateRunning })
		}
	}
	live, err := supervisor.LiveNodes(dir)
	return err == nil && len(live) > 0
}

type labView struct {
	lab.Config
	Dir     string `json:"dir"`
	Running bool   `json:"running"`
	// ChainIDSource says where a new lab's chain ID came from: the
	// --chain-id flag or the profile.
	ChainIDSource string         `json:"chain_id_source,omitempty"`
	Mnemonics     []lab.Mnemonic `json:"mnemonics,omitempty"`
}

func (v labView) WriteHuman(w io.Writer) error {
	state := "stopped"
	if v.Running {
		state = "running"
	}
	_, _ = fmt.Fprintf(w, "lab %s (%s, %s) at %s\n", v.Name, v.Mode, state, v.Dir)
	chain := v.ChainID
	if v.ChainIDSource != "" {
		chain += " (from " + v.ChainIDSource + ")"
	}
	_, _ = fmt.Fprintf(w, "profile %s, version %s, chain %s, %d validators\n\n", v.Profile.Name, versionText(v.Config), chain, v.Validators)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NODE\tVERSION\tRPC\tNODE ID\tPORTS")
	for _, n := range v.Nodes {
		var ports []string
		for _, p := range n.Ports {
			ports = append(ports, p.Key+"="+strconv.Itoa(p.Port))
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\n", n.Name, n.Version, n.RPCPort(), n.NodeID, strings.Join(ports, " "))
	}
	_ = tw.Flush()
	_, _ = fmt.Fprintln(w)
	mnemonic := map[string]string{}
	for _, m := range v.Mnemonics {
		mnemonic[m.Name] = m.Mnemonic
	}
	header := "KEY\tADDRESS"
	if v.Mnemonics != nil {
		header += "\tMNEMONIC"
	}
	_, _ = fmt.Fprintln(tw, header)
	for _, acc := range v.Accounts {
		line := acc.Name + "\t" + acc.Address
		if v.Mnemonics != nil {
			line += "\t" + mnemonic[acc.Name]
		}
		_, _ = fmt.Fprintln(tw, line)
	}
	return tw.Flush()
}

func newLabCreateCmd(a *app) *cobra.Command {
	var in lab.CreateInput
	var profileName, fork, progressFormat string
	var keepWork bool
	var binaries binaryFlags
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a lab with a fresh genesis, or one forked from a snapshot",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if progressFormat != "" && progressFormat != "json" {
				return output.Usagef("--progress must be json")
			}
			if progressFormat == "json" {
				in.Reporter = taskprogress.JSON(cmd.ErrOrStderr())
			}
			s, err := profile.DefaultStore()
			if err != nil {
				return err
			}
			if in.Profile, err = s.Get(profileName); err != nil {
				return err
			}
			if in.Profile, err = labProfile(in.Profile, binaries, in.Version); err != nil {
				return err
			}
			p := in.Profile.Profile
			if _, ok := p.Binaries[in.Version]; !ok {
				return output.Usagef("profile %s has no binary version %s", profileName, in.Version)
			}
			if in.Validators < 1 {
				return output.Usagef("--validators must be at least 1")
			}
			if in.TestAccounts < 0 {
				return output.Usagef("--test-accounts must not be negative")
			}
			in.Name = args[0]
			if err := lab.CheckName(in.Name); err != nil {
				return output.Usage(err)
			}
			root, err := paths.Home()
			if err != nil {
				return err
			}
			chainIDSource := "--chain-id"
			if in.ChainID == "" {
				in.ChainID, chainIDSource = p.ChainID, "profile "+p.Name
			}
			if fork != "" {
				vars := profile.Vars{Version: in.Version, OS: runtime.GOOS, Arch: runtime.GOARCH, ChainID: in.ChainID}
				in.Reporter.Emit("snapshot.resolve", "Resolving snapshot source", taskprogress.Started)
				src, err := forkSource(p, fork, vars)
				if err != nil {
					return output.Usage(err)
				}
				src, err = snapshot.Resolve(cmd.Context(), nil, src)
				if err != nil {
					return err
				}
				if err := snapshot.Reachable(cmd.Context(), nil, src, filepath.Join(root, "snapshots")); err != nil {
					return err
				}
				in.Reporter.Emit("snapshot.resolve", "Snapshot source reachable", taskprogress.Completed)
				if !a.json && in.Reporter == nil {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "forking %s as chain %s (from %s)\n", src, in.ChainID, chainIDSource)
				}
				in.Fork = &lab.ForkInput{Source: fork, Export: func(ctx context.Context, bin string) (string, string, error) {
					return exportSnapshot(ctx, a, cmd.ErrOrStderr(), root, src, snapshot.ExportInput{
						Reporter: in.Reporter, Binary: bin, Version: in.Version, ChainID: in.ChainID, Args: expand(p.ExportArgs, vars), KeepWork: keepWork,
					})
				}}
			}
			in.Binary = func(ctx context.Context) (string, error) {
				b, err := resolveBinary(ctx, a, cmd.ErrOrStderr(), p, in.Version, binary.Options{Reporter: in.Reporter})
				return b.Path, err
			}
			l, err := labs()
			if err != nil {
				return err
			}
			c, dir, err := l.Create(cmd.Context(), in)
			if err != nil {
				return err
			}
			return a.print(cmd, labView{Config: c, Dir: dir, ChainIDSource: chainIDSource})
		},
	}
	cmd.Flags().StringVar(&progressFormat, "progress", "", "live progress on stderr (json)")
	binaries.register(cmd.Flags())
	cmd.Flags().StringVar(&profileName, "profile", "", "profile name (required)")
	cmd.Flags().StringVar(&in.Version, "version", "", "binary version every node starts with (required)")
	cmd.Flags().IntVar(&in.Validators, "validators", 2, "number of validator nodes")
	cmd.Flags().IntVar(&in.TestAccounts, "test-accounts", 5, "number of funded test accounts")
	cmd.Flags().StringVar(&in.ChainID, "chain-id", "", "chain ID (default: the profile's)")
	cmd.Flags().StringVar(&fork, "fork", "", "fork the chain state of a snapshot: a profile snapshot name, a URL, or a .tar.lz4|gz|zst file; the lab runs as the profile's chain_id unless --chain-id is set")
	cmd.Flags().BoolVar(&keepWork, "keep-snapshot-work", false, "keep the extracted snapshot data after the export instead of deleting it")
	_ = cmd.MarkFlagRequired("profile")
	_ = cmd.MarkFlagRequired("version")
	return cmd
}

func labProfile(base profile.Entry, f binaryFlags, version string) (profile.Entry, error) {
	hasFields := false
	for i, specs := range f.binaryFields {
		for _, spec := range specs {
			hasFields = true
			target, _, ok := strings.Cut(spec, "=")
			if !ok || target != version {
				return profile.Entry{}, output.Usagef("--%s must target --version %s", binaryFieldFlags[i].flag, version)
			}
		}
	}
	if len(f.binaries) == 0 {
		if hasFields {
			return profile.Entry{}, output.Usagef("binary metadata requires --binary for --version %s", version)
		}
		return base, nil
	}
	if len(f.binaries) != 1 {
		return profile.Entry{}, output.Usagef("provide exactly one --binary for --version %s", version)
	}
	target, _, err := parseBinaryFlag(f.binaries[0])
	if err != nil {
		return profile.Entry{}, err
	}
	if target != version {
		return profile.Entry{}, output.Usagef("--binary must target --version %s", version)
	}
	d := base.Snapshot()
	d.Binaries = maps.Clone(d.Binaries)
	if err := f.apply(&d); err != nil {
		return profile.Entry{}, err
	}
	b := d.Binaries[version]
	for _, location := range []*string{&b.Path, &b.Src} {
		if *location == "" {
			continue
		}
		absolute, err := paths.UserPath(*location)
		if err != nil {
			return profile.Entry{}, err
		}
		*location = absolute
	}
	d.Binaries[version] = b
	p, err := d.Profile()
	if err != nil {
		return profile.Entry{}, withFlagHints(err)
	}
	base.Doc, base.Profile = d, p
	return base, nil
}

// forkSource turns the --fork argument into a URL or an existing file: a
// profile snapshot name first, then a URL, then a file.
func forkSource(p profile.Profile, arg string, vars profile.Vars) (string, error) {
	if t, ok := p.Snapshots[arg]; ok {
		return t.Expand(vars), nil
	}
	if snapshot.IsURL(arg) {
		return arg, nil
	}
	if _, err := os.Stat(arg); err != nil {
		known := "profile " + p.Name + " has no snapshots"
		if len(p.Snapshots) > 0 {
			known = "profile " + p.Name + " snapshots: " + strings.Join(slices.Sorted(maps.Keys(p.Snapshots)), ", ")
		}
		return "", fmt.Errorf("--fork %s is not a snapshot name, a URL, or a file (%s): %w", arg, known, err)
	}
	return arg, nil
}

func expand(ts []profile.Template, vars profile.Vars) []string {
	var out []string
	for _, t := range ts {
		out = append(out, t.Expand(vars))
	}
	return out
}

// exportSnapshot downloads src into <root>/snapshots and exports its state
// under <root>/snapshots/work/<key>. Progress and step lines go to stderr
// unless --json is set.
func exportSnapshot(ctx context.Context, a *app, stderr io.Writer, root, src string, in snapshot.ExportInput) (archive, exported string, err error) {
	var log io.Writer
	bar := &progress{w: stderr, last: -1}
	var onProgress snapshot.Progress
	if !a.json && in.Reporter == nil {
		log, onProgress = stderr, bar.update
	}
	snapshots := filepath.Join(root, "snapshots")
	phase := "snapshot.fetch"
	var message string
	if snapshot.IsURL(src) {
		if _, statErr := os.Stat(filepath.Join(snapshots, snapshot.Key(src))); statErr == nil {
			message = "Reusing cached snapshot archive"
		} else {
			message = "Fetching snapshot archive"
		}
	} else {
		message = "Using local snapshot archive"
	}
	in.Reporter.Emit(phase, message, taskprogress.Started)
	previous := onProgress
	downloaded := false
	archive, err = snapshot.Fetch(ctx, nil, src, snapshots, func(done, total int64) {
		if previous != nil {
			previous(done, total)
		}
		downloaded = downloaded || done > 0
		in.Reporter.Bytes(phase, "Downloading snapshot", done, total)
	})
	bar.end()
	if err != nil {
		return "", "", err
	}
	completed := "Local snapshot archive ready"
	if snapshot.IsURL(src) {
		completed = "Reused cached snapshot archive"
		if downloaded {
			completed = "Snapshot downloaded"
		}
	}
	in.Reporter.Emit(phase, completed, taskprogress.Completed)
	in.Archive, in.WorkDir, in.Log = archive, filepath.Join(snapshots, "work", snapshot.Key(src)), log
	out, err := snapshot.Export(ctx, in)
	if err != nil {
		return "", "", err
	}
	if out.Cached && log != nil {
		_, _ = fmt.Fprintf(log, "reusing export %s\n", out.Path)
	}
	return archive, out.Path, nil
}

// labRow is one lab list row. A lab whose lab.yaml cannot be read has a nil
// Config and an Error, so it can still be shown and deleted.
type labRow struct {
	*lab.Config
	Name    string `json:"name"`
	Dir     string `json:"dir"`
	Running bool   `json:"running"`
	Error   string `json:"error,omitempty"`
}

type labList []labRow

func (l labList) WriteHuman(w io.Writer) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAME\tPROFILE\tMODE\tVERSION\tVALIDATORS\tCHAIN ID\tSTATE")
	for _, r := range l {
		state := "stopped"
		if r.Running {
			state = "running"
		}
		if r.Config == nil {
			_, _ = fmt.Fprintf(tw, "%s\t-\t-\t-\t-\t-\t%s, broken: %s\n", r.Name, state, r.Error)
			continue
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\t%s\n", r.Name, r.Profile.Name, r.Mode, versionText(*r.Config), r.Validators, r.ChainID, state)
	}
	return tw.Flush()
}

// versionText is the version the nodes run, with the creation version when
// an upgrade or a restart moved them off it.
func versionText(c lab.Config) string {
	var running []string
	for _, n := range c.Nodes {
		if !slices.Contains(running, n.Version) {
			running = append(running, n.Version)
		}
	}
	if len(running) == 0 || (len(running) == 1 && running[0] == c.Version) {
		return c.Version
	}
	return strings.Join(running, ", ") + " (created " + c.Version + ")"
}

func newLabListCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List labs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			l, err := labs()
			if err != nil {
				return err
			}
			rows, err := l.List()
			if err != nil {
				return err
			}
			out := labList{}
			for _, r := range rows {
				out = append(out, labRow{Config: r.Config, Name: r.Name, Dir: r.Dir, Running: running(r.Dir), Error: r.Error})
			}
			return a.print(cmd, out)
		},
	}
}

func newLabShowCmd(a *app) *cobra.Command {
	var showMnemonics bool
	cmd := &cobra.Command{
		Use:   "show <name>",
		Short: "Show a lab's nodes, ports, and keys",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			l, err := labs()
			if err != nil {
				return err
			}
			if err := lab.CheckName(args[0]); err != nil {
				return output.Usage(err)
			}
			c, dir, err := l.Get(args[0])
			if err != nil {
				return err
			}
			v := labView{Config: c, Dir: dir, Running: running(dir)}
			if showMnemonics {
				data, err := os.ReadFile(lab.MnemonicsPath(dir))
				if err != nil {
					return err
				}
				v.Mnemonics = []lab.Mnemonic{}
				if err := json.Unmarshal(data, &v.Mnemonics); err != nil {
					return fmt.Errorf("%s: %w", lab.MnemonicsPath(dir), err)
				}
			}
			return a.print(cmd, v)
		},
	}
	cmd.Flags().BoolVar(&showMnemonics, "show-mnemonics", false, "include every key's mnemonic")
	return cmd
}

type labDeleted struct {
	Name string `json:"name"`
	Dir  string `json:"dir"`
}

func (d labDeleted) WriteHuman(w io.Writer) error {
	_, err := fmt.Fprintf(w, "deleted lab %s (%s)\n", d.Name, d.Dir)
	return err
}

func newLabDeleteCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a stopped lab and everything in its directory",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			l, err := labs()
			if err != nil {
				return err
			}
			if err := lab.CheckName(args[0]); err != nil {
				return output.Usage(err)
			}
			dir, err := l.Delete(args[0])
			if errors.Is(err, lab.ErrRunning) {
				return fmt.Errorf("%w; stop it first with forklab lab down %s", err, args[0])
			}
			if err != nil {
				return err
			}
			return a.print(cmd, labDeleted{Name: args[0], Dir: dir})
		},
	}
}
