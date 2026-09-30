package cli

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/kevinpita/forklab/internal/chain"
	"github.com/kevinpita/forklab/internal/cli/output"
	"github.com/kevinpita/forklab/internal/lab"
	"github.com/kevinpita/forklab/internal/supervisor"
	"github.com/spf13/cobra"
)

// resolveLab turns a --lab value into a lab directory. A value with a slash,
// or . or .., is a directory, anything else a lab name. Without a value it
// picks the running lab, else the only lab.
func resolveLab(arg string) (string, error) {
	if strings.Contains(arg, "/") || arg == "." || arg == ".." {
		return filepath.Abs(arg)
	}
	l, err := labs()
	if err != nil {
		return "", err
	}
	if arg != "" {
		_, dir, err := l.Get(arg)
		return dir, err
	}
	active, err := supervisor.ActiveLab()
	if err != nil || active != "" {
		return active, err
	}
	rows, err := l.List()
	if err != nil {
		return "", err
	}
	if len(rows) == 1 {
		return rows[0].Dir, nil
	}
	if len(rows) == 0 {
		return "", output.Usagef("--lab is required: no lab exists; create one with forklab lab create")
	}
	names := make([]string, len(rows))
	for i, r := range rows {
		names[i] = r.Name
	}
	return "", output.Usagef("--lab is required when no lab is running and several exist: %s", strings.Join(names, ", "))
}

// downLab stops every node and the supervisor. Nodes left behind by a
// killed supervisor get a new one that adopts them and stops them. It
// returns false when there was nothing to stop.
func downLab(ctx context.Context, dir string, timeout time.Duration) ([]supervisor.NodeStatus, bool, error) {
	c, err := supervisor.Dial(dir)
	if err != nil {
		live, err := supervisor.LiveNodes(dir)
		if err != nil || len(live) == 0 {
			return nil, false, err
		}
		if c, err = ensureSupervisor(ctx, dir); err != nil {
			return nil, false, err
		}
	}
	nodes, err := c.Down(timeout)
	return nodes, true, err
}

type upNode struct {
	supervisor.NodeStatus
	Height int64 `json:"height"`
}

type labUp struct {
	Name  string   `json:"name"`
	Dir   string   `json:"dir"`
	Nodes []upNode `json:"nodes"`
}

func (u labUp) WriteHuman(w io.Writer) error {
	_, _ = fmt.Fprintf(w, "lab %s is up\n", u.Name)
	for _, n := range u.Nodes {
		_, _ = fmt.Fprintf(w, "%s pid %d at height %d\n", n.Name, n.PID, n.Height)
	}
	return nil
}

func newLabUpCmd(a *app) *cobra.Command {
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "up <name>",
		Short: "Start a lab's supervisor and nodes and wait for blocks",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			l, err := labs()
			if err != nil {
				return err
			}
			c, dir, err := l.Get(args[0])
			if err != nil {
				return err
			}
			sup, err := ensureSupervisor(cmd.Context(), dir)
			if err != nil {
				return err
			}
			if _, err := sup.Start(supervisor.All); err != nil {
				return fmt.Errorf("lab %s: start nodes: %w", c.Name, err)
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()
			heights, err := waitBlocks(ctx, sup, dir, c)
			if err != nil {
				return err
			}
			nodes, err := sup.Status()
			if err != nil {
				return err
			}
			out := labUp{Name: c.Name, Dir: dir}
			for i, n := range nodes {
				out.Nodes = append(out.Nodes, upNode{NodeStatus: n, Height: heights[i]})
			}
			return a.print(cmd, out)
		},
	}
	cmd.Flags().DurationVar(&timeout, "timeout", 2*time.Minute, "how long every node has to produce a new block")
	return cmd
}

// waitBlocks waits until every node's RPC answers and its height moves past
// the first height it reports, and returns each node's height. A node whose
// process exits fails at once instead of at the timeout.
func waitBlocks(ctx context.Context, sup *supervisor.Client, dir string, c lab.Config) ([]int64, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		index  int
		height int64
		err    error
	}
	results := make(chan result, len(c.Nodes))
	for _, n := range c.Nodes {
		client, err := chain.New(fmt.Sprintf("http://127.0.0.1:%d", n.RPCPort()), 2*time.Second)
		if err != nil {
			return nil, err
		}
		go func() {
			h, err := client.WaitHeight(ctx, 1)
			if err == nil {
				h, err = client.WaitHeight(ctx, h+1)
			}
			results <- result{n.Index, h, err}
		}()
	}
	nodeErr := func(i int, err error) error {
		n := c.Nodes[i]
		return fmt.Errorf("lab %s: %s: %w; see %s", c.Name, n.Name, err, filepath.Join(lab.NodeHome(dir, n), "node.log"))
	}
	heights := make([]int64, len(c.Nodes))
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for pending := len(c.Nodes); pending > 0; {
		select {
		case r := <-results:
			if r.err != nil {
				return nil, nodeErr(r.index, r.err)
			}
			heights[r.index] = r.height
			pending--
		case <-tick.C:
			nodes, err := sup.Status()
			if err != nil {
				return nil, err
			}
			for _, n := range nodes {
				if n.State != supervisor.StateRunning {
					return nil, nodeErr(n.Index, fmt.Errorf("process %s (exit %s)", n.State, exitOf(n)))
				}
			}
		}
	}
	return heights, nil
}

type labDown struct {
	Name        string                  `json:"name"`
	Dir         string                  `json:"dir"`
	AlreadyDown bool                    `json:"already_down"`
	Nodes       []supervisor.NodeStatus `json:"nodes"`
}

func (d labDown) WriteHuman(w io.Writer) error {
	if d.AlreadyDown {
		_, err := fmt.Fprintf(w, "lab %s is already down\n", d.Name)
		return err
	}
	_, _ = fmt.Fprintf(w, "lab %s is down\n", d.Name)
	return nodeList(d.Nodes).WriteHuman(w)
}

func newLabDownCmd(a *app) *cobra.Command {
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "down <name>",
		Short: "Stop a lab's nodes and supervisor",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			l, err := labs()
			if err != nil {
				return err
			}
			c, dir, err := l.Get(args[0])
			if err != nil {
				return err
			}
			nodes, stopped, err := downLab(cmd.Context(), dir, timeout)
			if err != nil {
				return fmt.Errorf("lab %s: down: %w", c.Name, err)
			}
			if nodes == nil {
				nodes = []supervisor.NodeStatus{}
			}
			return a.print(cmd, labDown{Name: c.Name, Dir: dir, AlreadyDown: !stopped, Nodes: nodes})
		},
	}
	cmd.Flags().DurationVar(&timeout, "timeout", supervisor.DefaultStopTimeout, "how long to wait for SIGTERM before SIGKILL")
	return cmd
}

type resetNode struct {
	Name   string          `json:"name"`
	Method lab.ResetMethod `json:"method"`
}

type labReset struct {
	Name  string      `json:"name"`
	Dir   string      `json:"dir"`
	Nodes []resetNode `json:"nodes"`
}

func (r labReset) WriteHuman(w io.Writer) error {
	_, err := fmt.Fprintf(w, "reset lab %s; its next up replays from genesis\n", r.Name)
	return err
}

func newLabResetCmd(a *app) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "reset <name>",
		Short: "Wipe every node's chain data so the lab replays from genesis",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			l, err := labs()
			if err != nil {
				return err
			}
			c, dir, err := l.Get(args[0])
			if err != nil {
				return err
			}
			if force {
				if _, _, err := downLab(cmd.Context(), dir, supervisor.DefaultStopTimeout); err != nil {
					return fmt.Errorf("lab %s: down: %w", c.Name, err)
				}
			} else {
				live, err := supervisor.LiveNodes(dir)
				if err != nil {
					return err
				}
				if len(live) > 0 || running(dir) {
					return fmt.Errorf("lab %s: %w; stop it first with forklab lab down %s, or pass --force", c.Name, lab.ErrRunning, c.Name)
				}
			}
			methods, err := lab.Reset(cmd.Context(), dir)
			if err != nil {
				return fmt.Errorf("lab %s: %w", c.Name, err)
			}
			out := labReset{Name: c.Name, Dir: dir}
			for i, n := range c.Nodes {
				out.Nodes = append(out.Nodes, resetNode{Name: n.Name, Method: methods[i]})
			}
			return a.print(cmd, out)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "bring the lab down first if it is running")
	return cmd
}
