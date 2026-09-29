package cli

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"text/tabwriter"

	"github.com/kevinpita/forklab/internal/binary"
	"github.com/kevinpita/forklab/internal/cli/output"
	"github.com/kevinpita/forklab/internal/paths"
	"github.com/kevinpita/forklab/internal/profile"
	"github.com/spf13/cobra"
)

func binaryCache() (binary.Cache, error) {
	home, err := paths.Home()
	return binary.Cache{Dir: filepath.Join(home, "bin")}, err
}

func newBinaryCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "binary",
		Short: "Fetch, build, and list cached chain binaries",
	}
	cmd.AddCommand(newBinaryListCmd(a), newBinaryResolveCmd(a, false), newBinaryResolveCmd(a, true))
	return cmd
}

type binaryView binary.Binary

func (v binaryView) WriteHuman(w io.Writer) error {
	_, err := fmt.Fprintf(w, "%s %s (%s, version %s) at %s\n", v.Profile, v.Version, v.Kind, reportedText(binary.Binary(v)), v.Path)
	return err
}

func reportedText(b binary.Binary) string {
	switch b.Check {
	case binary.CheckMatched:
		return b.ReportedVersion
	case binary.CheckUnknown:
		return "unknown"
	default:
		return fmt.Sprintf("%s, %s", b.ReportedVersion, b.Check)
	}
}

type binaryList []binary.Binary

func (l binaryList) WriteHuman(w io.Writer) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "PROFILE\tVERSION\tKIND\tREPORTED\tPATH")
	for _, b := range l {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", b.Profile, b.Version, b.Kind, reportedText(b), b.Path)
	}
	return tw.Flush()
}

func newBinaryListCmd(a *app) *cobra.Command {
	var profileName string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List cached binaries",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := binaryCache()
			if err != nil {
				return err
			}
			all, err := c.List()
			if err != nil {
				return err
			}
			l := binaryList{}
			for _, b := range all {
				if profileName == "" || b.Profile == profileName {
					l = append(l, b)
				}
			}
			return a.print(cmd, l)
		},
	}
	cmd.Flags().StringVar(&profileName, "profile", "", "only this profile")
	return cmd
}

// newBinaryResolveCmd is fetch, or build when rebuild is set. Build always
// rebuilds, so it only applies to git and src sources.
func newBinaryResolveCmd(a *app, rebuild bool) *cobra.Command {
	var profileName string
	var noVerify bool
	cmd := &cobra.Command{
		Use:   "fetch <version>",
		Short: "Resolve a profile's binary version into the cache, downloading or building it if needed",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := profile.DefaultStore()
			if err != nil {
				return err
			}
			e, err := s.Get(profileName)
			if err != nil {
				return err
			}
			version := args[0]
			src, ok := e.Profile.Binaries[version]
			if !ok {
				return output.Usagef("profile %s has no binary version %s", profileName, version)
			}
			if rebuild {
				switch src.(type) {
				case profile.GitSource, profile.SrcSource:
				default:
					return output.Usagef("binary build applies to git and src binaries; %s %s is not built, use binary fetch", profileName, version)
				}
			}
			c, err := binaryCache()
			if err != nil {
				return err
			}
			opts := binary.Options{NoVerify: noVerify, Rebuild: rebuild}
			stderr := cmd.ErrOrStderr()
			bar := &progress{w: stderr, last: -1}
			if !a.json {
				opts.Progress = bar.update
				opts.BuildLog = stderr
			}
			b, err := c.Resolve(cmd.Context(), e.Profile, version, opts)
			bar.end()
			if errors.Is(err, binary.ErrVersionMismatch) {
				return fmt.Errorf("%w (pass --no-verify to accept it)", err)
			}
			if err != nil {
				return err
			}
			if !a.json && b.Check == binary.CheckUnknown {
				_, _ = fmt.Fprintf(stderr, "warning: %s version printed nothing; version recorded as unknown\n", filepath.Base(b.Path))
			}
			return a.print(cmd, binaryView(b))
		},
	}
	if rebuild {
		cmd.Use = "build <version>"
		cmd.Short = "Build a git or src binary again, replacing the cached build"
	}
	cmd.Flags().StringVar(&profileName, "profile", "", "profile name (required)")
	cmd.Flags().BoolVar(&noVerify, "no-verify", false, "accept a binary whose `version` output differs from the requested version")
	_ = cmd.MarkFlagRequired("profile")
	return cmd
}

// progress redraws one download line on w each whole percent, or each MiB
// when the size is unknown.
type progress struct {
	w    io.Writer
	last int64
}

func (p *progress) update(done, total int64) {
	const mib = 1 << 20
	step := done / mib
	if total > 0 {
		step = done * 100 / total
	}
	if step == p.last {
		return
	}
	p.last = step
	if total > 0 {
		_, _ = fmt.Fprintf(p.w, "\rdownloading %d%% (%d/%d MiB)", step, done/mib, total/mib)
	} else {
		_, _ = fmt.Fprintf(p.w, "\rdownloading %d MiB", step)
	}
}

func (p *progress) end() {
	if p.last >= 0 {
		_, _ = fmt.Fprintln(p.w)
	}
}
