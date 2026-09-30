package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/kevinpita/forklab/internal/cli/output"
	"github.com/kevinpita/forklab/internal/supervisor"
	"github.com/spf13/cobra"
)

func newNodeCmd(a *app) *cobra.Command {
	var labArg, labDir string
	cmd := &cobra.Command{
		Use:   "node",
		Short: "Control a lab's node processes",
		PersistentPreRunE: func(*cobra.Command, []string) error {
			var err error
			labDir, err = resolveLab(labArg)
			return err
		},
	}
	cmd.PersistentFlags().StringVar(&labArg, "lab", "", "lab name or directory (default: the running lab, else the only lab)")
	cmd.AddCommand(
		newNodeListCmd(a, &labDir),
		newNodeStartCmd(a, &labDir),
		newNodeStopCmd(a, &labDir),
		newNodeKillCmd(a, &labDir),
		newNodeRestartCmd(a, &labDir),
		newNodeLogsCmd(a, &labDir),
	)
	return cmd
}

type nodeList []supervisor.NodeStatus

func (l nodeList) WriteHuman(w io.Writer) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NODE\tSTATE\tPID\tUPTIME\tEXIT\tBINARY")
	for _, n := range l {
		pid, uptime, exit := "-", "-", "-"
		switch n.State {
		case supervisor.StateRunning:
			pid = strconv.Itoa(n.PID)
			uptime = (time.Duration(n.UptimeSeconds) * time.Second).String()
			if n.Adopted {
				pid += " (adopted)"
			}
		case supervisor.StateExited:
			exit = exitOf(n)
		}
		_, _ = fmt.Fprintf(tw, "%d %s\t%s\t%s\t%s\t%s\t%s\n", n.Index, n.Name, n.State, pid, uptime, exit, n.Binary)
	}
	return tw.Flush()
}

func exitOf(n supervisor.NodeStatus) string {
	switch {
	case n.Signal != "":
		return n.Signal
	case n.ExitCode != nil:
		return strconv.Itoa(*n.ExitCode)
	}
	return "unknown"
}

var nodeArg = cobra.ExactArgs(1)

func newNodeListCmd(a *app, labDir *string) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "Show every node's state, pid, uptime, and binary",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := ensureSupervisor(cmd.Context(), *labDir)
			if err != nil {
				return err
			}
			nodes, err := c.Status()
			if err != nil {
				return err
			}
			return a.print(cmd, nodeList(nodes))
		},
	}
}

func newNodeStartCmd(a *app, labDir *string) *cobra.Command {
	return &cobra.Command{
		Use:   "start <i|all>",
		Short: "Start a node, or all of them",
		Args:  nodeArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := ensureSupervisor(cmd.Context(), *labDir)
			if err != nil {
				return err
			}
			nodes, err := c.Start(args[0])
			if err != nil {
				return err
			}
			return a.print(cmd, nodeList(nodes))
		},
	}
}

func newNodeStopCmd(a *app, labDir *string) *cobra.Command {
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "stop <i|all>",
		Short: "Stop a node with SIGTERM and wait for it to exit",
		Args:  nodeArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := ensureSupervisor(cmd.Context(), *labDir)
			if err != nil {
				return err
			}
			nodes, err := c.Stop(args[0], timeout)
			if err != nil {
				return err
			}
			return a.print(cmd, nodeList(nodes))
		},
	}
	cmd.Flags().DurationVar(&timeout, "timeout", supervisor.DefaultStopTimeout, "how long to wait for the node to exit")
	return cmd
}

func newNodeKillCmd(a *app, labDir *string) *cobra.Command {
	return &cobra.Command{
		Use:   "kill <i|all>",
		Short: "Kill a node with SIGKILL to simulate a crash",
		Args:  nodeArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := ensureSupervisor(cmd.Context(), *labDir)
			if err != nil {
				return err
			}
			nodes, err := c.Kill(args[0])
			if err != nil {
				return err
			}
			return a.print(cmd, nodeList(nodes))
		},
	}
}

func newNodeRestartCmd(a *app, labDir *string) *cobra.Command {
	var timeout time.Duration
	var binary string
	cmd := &cobra.Command{
		Use:   "restart <i|all>",
		Short: "Stop a node and start it again, optionally with another binary",
		Args:  nodeArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := ensureSupervisor(cmd.Context(), *labDir)
			if err != nil {
				return err
			}
			nodes, err := c.Restart(args[0], binary, timeout)
			if err != nil {
				return err
			}
			return a.print(cmd, nodeList(nodes))
		},
	}
	cmd.Flags().DurationVar(&timeout, "timeout", supervisor.DefaultStopTimeout, "how long to wait for the node to exit")
	cmd.Flags().StringVar(&binary, "binary", "", "path of the binary to run from now on")
	return cmd
}

// closedChan makes Tail read the file once instead of following it.
var closedChan = func() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}()

type logLine struct {
	Node int    `json:"node"`
	Line string `json:"line"`
}

func newNodeLogsCmd(a *app, labDir *string) *cobra.Command {
	var follow bool
	cmd := &cobra.Command{
		Use:   "logs <i>",
		Short: "Print a node's log; --json emits one object per line",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			specs, err := supervisor.LoadNodes(*labDir)
			if err != nil {
				return err
			}
			i, err := strconv.Atoi(args[0])
			if err != nil || i < 0 || i >= len(specs) {
				return output.Usagef("node %q: want an index 0..%d", args[0], len(specs)-1)
			}
			var done <-chan struct{} = closedChan
			if follow {
				done = cmd.Context().Done()
			}
			w := cmd.OutOrStdout()
			enc := json.NewEncoder(w)
			return supervisor.Tail(specs[i].LogPath, 0, done, func(line string) {
				if a.json {
					_ = enc.Encode(logLine{Node: i, Line: line})
				} else {
					_, _ = fmt.Fprintln(w, line)
				}
			})
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "keep printing new lines until interrupted")
	return cmd
}
