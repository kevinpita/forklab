package cli

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/kevinpita/forklab/internal/snapshot"
	"github.com/spf13/cobra"
)

type snapshotView struct {
	Archive string           `json:"archive"`
	Home    string           `json:"home,omitempty"`
	Marker  *snapshot.Marker `json:"marker,omitempty"`
}

func (v snapshotView) WriteHuman(w io.Writer) error {
	if _, err := fmt.Fprintf(w, "archive %s\n", v.Archive); err != nil {
		return err
	}
	if v.Marker == nil {
		return nil
	}
	_, err := fmt.Fprintf(w, "extracted %s into %s/data: %s\n", v.Marker.Format, v.Home, strings.Join(v.Marker.Found, ", "))
	return err
}

func newSnapshotCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:    "snapshot",
		Short:  "Download and extract chain snapshots",
		Hidden: true,
	}
	var home string
	fetch := &cobra.Command{
		Use:   "fetch <url|file>",
		Short: "Download a snapshot into $FORKLAB_HOME/snapshots, and extract it with --home",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := forklabHome()
			if err != nil {
				return err
			}
			var progress snapshot.Progress
			if !a.json {
				line := &progressLine{w: cmd.ErrOrStderr(), last: -1}
				defer line.end()
				progress = line.update
			}
			archive, err := snapshot.Fetch(cmd.Context(), nil, args[0], filepath.Join(root, "snapshots"), progress)
			if err != nil {
				return err
			}
			v := snapshotView{Archive: archive}
			if home != "" {
				m, err := snapshot.Extract(cmd.Context(), archive, home)
				if err != nil {
					return err
				}
				v.Home, v.Marker = home, &m
			}
			return a.print(cmd, v)
		},
	}
	fetch.Flags().StringVar(&home, "home", "", "extract the snapshot's data/ into this node home")
	cmd.AddCommand(fetch)
	return cmd
}

// progressLine rewrites one stderr line with each whole percent.
type progressLine struct {
	w    io.Writer
	last int64
	open bool
}

func (p *progressLine) update(done, total int64) {
	if total <= 0 {
		return
	}
	pct := done * 100 / total
	if pct == p.last {
		return
	}
	p.last = pct
	_, _ = fmt.Fprintf(p.w, "\rdownload %3d%% (%d/%d bytes)", pct, done, total)
	p.open = true
}

// end finishes the line so whatever prints next starts on a fresh one.
func (p *progressLine) end() {
	if p.open {
		_, _ = fmt.Fprintln(p.w)
	}
}
