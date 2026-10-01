package cli

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kevinpita/forklab/internal/binary"
	"github.com/kevinpita/forklab/internal/cli/output"
	"github.com/kevinpita/forklab/internal/lab"
	taskprogress "github.com/kevinpita/forklab/internal/progress"
	"github.com/kevinpita/forklab/internal/supervisor"
	"github.com/spf13/cobra"
)

func lockUpgrade(dir string) (func(), error) {
	f, err := os.OpenFile(filepath.Join(dir, "upgrade-command.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, errors.New("another upgrade command is running; wait for it to finish or stop that command before recovering")
	}
	return func() { _ = f.Close() }, nil
}

type recoveryView struct {
	Lab    string        `json:"lab"`
	Mode   string        `json:"mode"`
	Height int64         `json:"height"`
	Nodes  []upgradeNode `json:"nodes"`
}

func (v recoveryView) WriteHuman(w io.Writer) error {
	if v.Mode == "previous" {
		_, _ = fmt.Fprintf(w, "returned to previous binaries and skipped the upgrade; chain reached height %d\n", v.Height)
	} else {
		_, _ = fmt.Fprintf(w, "upgrade retry succeeded; chain reached height %d\n", v.Height)
	}
	return writeUpgradeNodes(w, v.Nodes)
}

func newUpgradeRecoverCmd(a *app, ref *string) *cobra.Command {
	var previous bool
	var version, format string
	cmd := &cobra.Command{
		Use:   "recover (--previous | --version VERSION)",
		Short: "Recover a failed upgrade using the previous or a selected binary",
		Long:  "Stop all validators, switch binaries, and wait for fresh blocks beyond the failed upgrade.\n--previous adds the failed height to --unsafe-skip-upgrades and keeps existing skip heights.\nThis switches executables; it does not restore database state.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if previous == (version != "") {
				return output.Usagef("choose exactly one of --previous or --version")
			}
			if format != "" && format != "json" {
				return output.Usagef("--progress must be json")
			}
			var report taskprogress.Reporter
			if format == "json" {
				report = taskprogress.JSON(cmd.ErrOrStderr())
			}
			e, err := openLab(*ref)
			if err != nil {
				return err
			}
			unlock, err := lockUpgrade(e.dir)
			if err != nil {
				return err
			}
			defer unlock()
			return recoverUpgrade(cmd, a, e, previous, version, report)
		},
	}
	cmd.Flags().BoolVar(&previous, "previous", false, "return to original binaries and skip this upgrade height")
	cmd.Flags().StringVar(&version, "version", "", "retry the upgrade with this profile version")
	registerCompletion(cmd, "version", completeLabVersions)
	cmd.Flags().StringVar(&format, "progress", "", "live progress on stderr (json)")
	return cmd
}

func recoverUpgrade(cmd *cobra.Command, a *app, e labEnv, previous bool, version string, report taskprogress.Reporter) error {
	ctx := cmd.Context()
	report.Emit("recovery.prepare", "Checking failed upgrade", taskprogress.Started)
	st, err := supervisor.LoadUpgradeStatus(e.dir)
	if err != nil {
		return err
	}
	if sup, dialErr := supervisor.Dial(e.dir); dialErr == nil {
		if st, err = sup.Upgrade(); err != nil {
			return err
		}
	}
	if st.Upgrade == nil {
		return errors.New("no pending upgrade to recover")
	}
	expected := st.Upgrade
	if expected.Name == "" || expected.Height <= 0 {
		return errors.New("pending upgrade has no valid name or height")
	}
	if expected.Recovery == nil && !slices.ContainsFunc(st.Nodes, func(n supervisor.NodeStatus) bool {
		return n.State != supervisor.StateRunning && (n.Halt != nil && n.Halt.Name == expected.Name && n.Halt.Height == expected.Height || n.Binary == expected.Binary)
	}) {
		return errors.New("the pending upgrade has not failed; recovery requires a halted or exited validator")
	}
	specs, err := supervisor.LoadNodes(e.dir)
	if err != nil {
		return err
	}
	next := *expected
	if len(next.Previous) == 0 {
		next.Previous, err = originalTargets(e, st)
		if err != nil {
			return err
		}
	}
	if len(next.Previous) != len(specs) {
		return errors.New("original binary records do not cover every validator")
	}
	targets := slices.Clone(next.Previous)
	mode := "previous"
	if !previous {
		mode = "retry"
		b, err := resolveBinary(ctx, a, cmd.ErrOrStderr(), e.profile, version, binary.Options{Reporter: report})
		if err != nil {
			return err
		}
		next.Binary, next.Version = b.Path, version
		for i := range targets {
			targets[i] = supervisor.UpgradeTarget{Index: i, Binary: b.Path, Version: version}
		}
	}
	args := make([][]string, len(specs))
	for i, target := range targets {
		if target.Index != i || target.Version == "" {
			return fmt.Errorf("invalid original binary record for validator %d", i)
		}
		info, err := os.Stat(target.Binary)
		if err != nil {
			return err
		}
		if info.IsDir() || info.Mode()&0o111 == 0 {
			return fmt.Errorf("%s is not executable", target.Binary)
		}
		args[i], err = recoveryArgs(specs[i].Args, expected.Height, previous)
		if err != nil {
			return fmt.Errorf("%s: %w", specs[i].Name, err)
		}
	}
	next.AutoSwap = false
	next.Recovery = &supervisor.Recovery{ID: rand.Text(), Mode: mode, Targets: targets}

	sup, err := supervisor.Dial(e.dir)
	if err != nil {
		report.Emit("recovery.supervisor", "Starting supervisor with automatic swaps disabled", taskprogress.Started)
		sup, err = supervisor.EnsureRunning(ctx, e.dir, func(dir string) (*exec.Cmd, error) {
			cmd, err := supervisor.ForklabSpawner(dir)
			if err == nil {
				cmd.Args = append(cmd.Args, "--freeze-upgrade")
			}
			return cmd, err
		})
		if err != nil {
			return err
		}
		frozen := *expected
		frozen.AutoSwap = false
		expected = &frozen
	}
	live, err := sup.Upgrade()
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(live.Upgrade, expected) {
		return errors.New("pending upgrade changed; inspect upgrade status and retry")
	}
	if live.RecoveryProtocol < 1 {
		report.Emit("recovery.supervisor", "Refreshing supervisor for recovery", taskprogress.Started)
		frozen := *expected
		frozen.AutoSwap = false
		if err := sup.Exit(); err != nil {
			return err
		}
		sup, err = supervisor.EnsureRunning(ctx, e.dir, func(dir string) (*exec.Cmd, error) {
			cmd, err := supervisor.ForklabSpawner(dir)
			if err == nil {
				cmd.Args = append(cmd.Args, "--freeze-upgrade")
			}
			return cmd, err
		})
		if err != nil {
			return err
		}
		expected = &frozen
		live, err = sup.Upgrade()
		if err != nil {
			return err
		}
		if live.RecoveryProtocol < 1 {
			return errors.New("supervisor does not support upgrade recovery; rebuild forklab")
		}
		if !reflect.DeepEqual(live.Upgrade, expected) {
			return errors.New("pending upgrade changed during supervisor refresh")
		}
	}
	if expected.Recovery == nil && !slices.ContainsFunc(live.Nodes, func(n supervisor.NodeStatus) bool { return n.State != supervisor.StateRunning }) {
		return errors.New("all validators are running; recovery requires a failed validator")
	}
	if _, err = sup.RecoverUpgrade(expected, &next); err != nil {
		return err
	}
	persisted, err := supervisor.LoadUpgrade(e.dir)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(persisted, &next) {
		return errors.New("supervisor did not persist the recovery plan; no nodes were changed")
	}
	report.Emit("recovery.prepare", "Original binaries saved and automatic swaps disabled", taskprogress.Completed)
	report.Emit("recovery.stop", "Stopping all validators", taskprogress.Started)
	if _, err = sup.Stop(supervisor.All, 0); err != nil {
		return err
	}
	report.Emit("recovery.stop", "All validators stopped", taskprogress.Completed)
	for i := range targets {
		if _, err = sup.Configure(strconv.Itoa(i), args[i]); err != nil {
			return err
		}
	}
	report.Emit("recovery.restart", "Starting recovery binaries", taskprogress.Started)
	for _, target := range targets {
		report.Emit("recovery.restart", fmt.Sprintf("Starting %s on %s", specs[target.Index].Name, target.Version), taskprogress.Updated)
		if _, err = sup.Restart(strconv.Itoa(target.Index), target.Binary, target.Version, 0); err != nil {
			nodes, _ := sup.Status()
			return fmt.Errorf("recovery failed; original binaries remain recorded: %w%s", err, e.unswappedLogs(nodes))
		}
	}
	report.Emit("recovery.restart", "Recovery binaries started", taskprogress.Completed)
	waitCtx, cancel := context.WithTimeout(ctx, pastPlanTimeout)
	defer cancel()
	height, err := e.waitRecovery(waitCtx, sup, &next, report)
	if err != nil {
		return fmt.Errorf("recovery remains pending: %w", err)
	}
	if _, err = sup.FinishRecovery(&next); err != nil {
		return err
	}
	cfg, err := lab.Load(e.dir)
	if err != nil {
		return err
	}
	nodes, err := sup.Status()
	if err != nil {
		return err
	}
	report.Emit("recovery.complete", "Recovery verified", taskprogress.Completed)
	return a.print(cmd, recoveryView{Lab: cfg.Name, Mode: mode, Height: height, Nodes: upgradeNodes(cfg, nodes)})
}

func originalTargets(e labEnv, st supervisor.Response) ([]supervisor.UpgradeTarget, error) {
	cache, err := binaryCache()
	if err != nil {
		return nil, err
	}
	bins, err := cache.List()
	if err != nil {
		return nil, err
	}
	out := make([]supervisor.UpgradeTarget, len(e.cfg.Nodes))
	for i := range out {
		if i >= len(st.Nodes) || st.Nodes[i].Halt == nil || st.Nodes[i].Halt.Binary == "" {
			return nil, fmt.Errorf("%s has no recorded original binary; cannot safely recover", e.cfg.Nodes[i].Name)
		}
		h := st.Nodes[i].Halt
		if h.Name != st.Upgrade.Name || h.Height != st.Upgrade.Height {
			return nil, fmt.Errorf("%s halt does not match the pending upgrade", e.cfg.Nodes[i].Name)
		}
		version := ""
		for _, b := range bins {
			if b.Path == h.Binary && b.Profile == e.profile.Name {
				if version != "" && version != b.Version {
					return nil, fmt.Errorf("ambiguous version for original binary %s", h.Binary)
				}
				version = b.Version
			}
		}
		if version == "" {
			return nil, fmt.Errorf("original binary %s has no matching cached version metadata", h.Binary)
		}
		out[i] = supervisor.UpgradeTarget{Index: i, Binary: h.Binary, Version: version}
	}
	return out, nil
}

func recoveryArgs(args []string, height int64, skip bool) ([]string, error) {
	var out []string
	var heights []int64
	for i := 0; i < len(args); i++ {
		arg := args[i]
		value, matched := strings.CutPrefix(arg, "--unsafe-skip-upgrades=")
		if arg == "--unsafe-skip-upgrades" {
			i++
			if i >= len(args) {
				return nil, errors.New("--unsafe-skip-upgrades has no heights")
			}
			value, matched = args[i], true
		}
		if !matched {
			out = append(out, arg)
			continue
		}
		for _, part := range strings.Split(value, ",") {
			h, err := strconv.ParseInt(part, 10, 64)
			if err != nil || h <= 0 {
				return nil, fmt.Errorf("invalid skipped upgrade height %q", part)
			}
			if h != height && !slices.Contains(heights, h) {
				heights = append(heights, h)
			}
		}
	}
	if skip {
		heights = append(heights, height)
	}
	if len(heights) > 0 {
		values := make([]string, len(heights))
		for i, h := range heights {
			values[i] = strconv.FormatInt(h, 10)
		}
		out = append(out, "--unsafe-skip-upgrades", strings.Join(values, ","))
	}
	return out, nil
}

func (e labEnv) waitRecovery(ctx context.Context, sup *supervisor.Client, plan *supervisor.Upgrade, report taskprogress.Reporter) (int64, error) {
	baseline := make([]int64, len(e.clients))
	report.Emit("recovery.blocks", "Waiting for fresh blocks beyond the upgrade", taskprogress.Started)
	for {
		st, err := sup.Upgrade()
		if err != nil {
			return 0, err
		}
		if !reflect.DeepEqual(st.Upgrade, plan) {
			return 0, errors.New("pending recovery changed")
		}
		if len(st.Nodes) != len(plan.Recovery.Targets) {
			return 0, errors.New("validator set changed")
		}
		all := true
		var top int64
		for i, n := range st.Nodes {
			if n.State != supervisor.StateRunning || n.Binary != plan.Recovery.Targets[i].Binary {
				return 0, fmt.Errorf("%s is %s on %s%s", n.Name, n.State, n.Binary, e.unswappedLogs(st.Nodes))
			}
			status, err := e.clients[i].Status(ctx)
			if err != nil {
				all = false
				continue
			}
			h := status.LatestHeight
			if baseline[i] == 0 {
				baseline[i] = h
				all = false
			}
			if h <= baseline[i] || h < plan.Height+pastPlanBlocks {
				all = false
			}
			top = max(top, h)
		}
		if all {
			return top, nil
		}
		report.Emit("recovery.blocks", fmt.Sprintf("Waiting beyond height %d; latest height %d", plan.Height+pastPlanBlocks, top), taskprogress.Updated)
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}
