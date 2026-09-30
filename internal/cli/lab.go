package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/kevinpita/forklab/internal/binary"
	"github.com/kevinpita/forklab/internal/cli/output"
	"github.com/kevinpita/forklab/internal/lab"
	"github.com/kevinpita/forklab/internal/paths"
	"github.com/kevinpita/forklab/internal/profile"
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
		Short: "Create, inspect, and delete labs",
	}
	cmd.AddCommand(newLabCreateCmd(a), newLabListCmd(a), newLabShowCmd(a), newLabDeleteCmd(a))
	return cmd
}

func running(dir string) bool {
	_, err := supervisor.Dial(dir)
	return err == nil
}

type labView struct {
	lab.Config
	Dir       string         `json:"dir"`
	Running   bool           `json:"running"`
	Mnemonics []lab.Mnemonic `json:"mnemonics,omitempty"`
}

func (v labView) WriteHuman(w io.Writer) error {
	state := "stopped"
	if v.Running {
		state = "running"
	}
	_, _ = fmt.Fprintf(w, "lab %s (%s, %s) at %s\n", v.Name, v.Mode, state, v.Dir)
	_, _ = fmt.Fprintf(w, "profile %s, version %s, chain %s, %d validators\n\n", v.Profile.Name, v.Version, v.ChainID, v.Validators)
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
	var profileName string
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a lab with a fresh genesis",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := profile.DefaultStore()
			if err != nil {
				return err
			}
			if in.Profile, err = s.Get(profileName); err != nil {
				return err
			}
			if _, ok := in.Profile.Profile.Binaries[in.Version]; !ok {
				return output.Usagef("profile %s has no binary version %s", profileName, in.Version)
			}
			if in.Validators < 1 {
				return output.Usagef("--validators must be at least 1")
			}
			if in.TestAccounts < 0 {
				return output.Usagef("--test-accounts must not be negative")
			}
			if in.ChainID == "" {
				in.ChainID = in.Profile.Profile.ChainID
			}
			in.Name = args[0]
			if err := lab.CheckName(in.Name); err != nil {
				return output.Usage(err)
			}
			in.Binary = func(ctx context.Context) (string, error) {
				b, err := resolveBinary(ctx, a, cmd.ErrOrStderr(), in.Profile.Profile, in.Version, binary.Options{})
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
			return a.print(cmd, labView{Config: c, Dir: dir})
		},
	}
	cmd.Flags().StringVar(&profileName, "profile", "", "profile name (required)")
	cmd.Flags().StringVar(&in.Version, "version", "", "binary version every node starts with (required)")
	cmd.Flags().IntVar(&in.Validators, "validators", 2, "number of validator nodes")
	cmd.Flags().IntVar(&in.TestAccounts, "test-accounts", 5, "number of funded test accounts")
	cmd.Flags().StringVar(&in.ChainID, "chain-id", "", "chain ID (default: the profile's)")
	_ = cmd.MarkFlagRequired("profile")
	_ = cmd.MarkFlagRequired("version")
	return cmd
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
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\t%s\n", r.Name, r.Profile.Name, r.Mode, r.Version, r.Validators, r.ChainID, state)
	}
	return tw.Flush()
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
				path, _ := l.Path(args[0])
				return fmt.Errorf("%w; stop it first with forklab supervisor down --lab %s", err, path)
			}
			if err != nil {
				return err
			}
			return a.print(cmd, labDeleted{Name: args[0], Dir: dir})
		},
	}
}
