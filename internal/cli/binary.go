package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
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
		ValidArgsFunction: completeProfileVersions,
		Use:               "fetch <version>",
		Short:             "Resolve a profile's binary version into the cache, downloading or building it if needed",
		Args:              cobra.ExactArgs(1),
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
			stderr := cmd.ErrOrStderr()
			b, err := resolveBinary(cmd.Context(), a, stderr, e.Profile, version, binary.Options{NoVerify: noVerify, Rebuild: rebuild})
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

// resolveBinary resolves a binary, drawing download progress and build
// output on stderr unless output is JSON.
func resolveBinary(ctx context.Context, a *app, stderr io.Writer, p profile.Profile, version string, opts binary.Options) (binary.Binary, error) {
	c, err := binaryCache()
	if err != nil {
		return binary.Binary{}, err
	}
	bar := &progress{w: stderr, last: -1}
	if !a.json && opts.Reporter == nil {
		opts.Progress = bar.update
		opts.BuildLog = stderr
	}
	b, err := c.Resolve(ctx, p, version, opts)
	bar.end()
	return b, err
}

// cosmovisorBin checks the path given as --cosmovisor-bin.
func cosmovisorBin(path string) (string, error) {
	path, err := paths.UserPath(path)
	if err != nil {
		return "", err
	}
	if info, err := os.Stat(path); err != nil {
		return "", output.Usagef("--cosmovisor-bin: %v", err)
	} else if info.IsDir() || info.Mode()&0o111 == 0 {
		return "", output.Usagef("--cosmovisor-bin %s is not an executable file", path)
	}
	return path, nil
}

// resolveCosmovisor returns the cosmovisor version forklab pins, built and
// cached on first use.
func resolveCosmovisor(ctx context.Context, a *app, stderr io.Writer, opts binary.Options) (string, error) {
	c, err := binaryCache()
	if err != nil {
		return "", err
	}
	if !a.json && opts.Reporter == nil {
		opts.BuildLog = stderr
	}
	b, err := c.Cosmovisor(ctx, opts)
	return b.Path, err
}

// upgradeInfo is the plan info of an upgrade proposal. When cosmovisor
// downloads the binary it is the link cosmovisor reads: the version's url
// source with the checksum of what forklab itself downloaded from it, under
// the platform the nodes run on, goos and this machine's architecture. A
// source that cosmovisor could not use is refused here, before a proposal
// that would halt the chain with no binary: cosmovisor unpacks fewer archive
// formats than forklab and finds the binary only at the top of the archive
// or in its bin directory.
func upgradeInfo(bin binary.Binary, download bool, goos string) (string, error) {
	if !download {
		return "forklab upgrade to " + bin.Version, nil
	}
	const instead = "add a url source cosmovisor can use, or pass --no-auto-swap and deliver the binary yourself"
	if bin.Kind != binary.KindURL || bin.ArtifactSHA256 == "" {
		return "", fmt.Errorf("this lab's cosmovisor downloads upgrade binaries, which needs a url source: version %s of profile %s is a %s source; %s", bin.Version, bin.Profile, bin.Kind, instead)
	}
	u, err := url.Parse(bin.Source)
	if err != nil {
		return "", err
	}
	if strings.HasSuffix(strings.ToLower(u.Path), ".tar.lz4") {
		return "", fmt.Errorf("this lab's cosmovisor downloads upgrade binaries, and cosmovisor cannot unpack a .tar.lz4 archive: %s; %s", bin.Source, instead)
	}
	name := filepath.Base(bin.Path)
	if bin.ArtifactEntry != name && bin.ArtifactEntry != "bin/"+name {
		return "", fmt.Errorf("this lab's cosmovisor downloads upgrade binaries: cosmovisor looks for %s or bin/%s at the top of the archive, and %s has it at %s; %s", name, name, bin.Source, bin.ArtifactEntry, instead)
	}
	checksum := "checksum=sha256:" + bin.ArtifactSHA256
	if u.RawQuery != "" {
		checksum = u.RawQuery + "&" + checksum
	}
	u.RawQuery = checksum
	data, err := json.Marshal(map[string]map[string]string{"binaries": {goos + "/" + runtime.GOARCH: u.String()}})
	return string(data), err
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
		_, _ = fmt.Fprintf(p.w, "\rdownloading %d%% (%s)", step, byteSizes(done, total))
	} else {
		_, _ = fmt.Fprintf(p.w, "\rdownloading %s", byteSizes(done))
	}
}

// byteSizes writes sizes as "a/b unit" in the unit that suits the last one,
// so a small archive does not read as 0 MiB.
func byteSizes(sizes ...int64) string {
	unit, name := int64(1), "B"
	for _, u := range []struct {
		size int64
		name string
	}{{1 << 30, "GiB"}, {1 << 20, "MiB"}, {1 << 10, "KiB"}} {
		if sizes[len(sizes)-1] >= u.size {
			unit, name = u.size, u.name
			break
		}
	}
	parts := make([]string, len(sizes))
	for i, n := range sizes {
		parts[i] = strconv.FormatInt(n, 10)
		if unit > 1 {
			parts[i] = strconv.FormatFloat(float64(n)/float64(unit), 'f', 1, 64)
		}
	}
	return strings.Join(parts, "/") + " " + name
}

func (p *progress) end() {
	if p.last >= 0 {
		_, _ = fmt.Fprintln(p.w)
	}
}
