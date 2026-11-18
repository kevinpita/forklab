package supervisor

import (
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// Cosmovisor makes a node run under cosmovisor: the supervised process is
// `<Path> run <Args...>`, the chain binary is its child, and cosmovisor, not
// the supervisor, switches the binary at an upgrade. Name is the chain
// binary's file name. Download lets cosmovisor fetch an upgrade binary from
// the links in the plan info; the supervisor then places none.
type Cosmovisor struct {
	Path     string `json:"path"`
	Name     string `json:"name"`
	Download bool   `json:"download,omitempty"`
}

// env is cosmovisor's configuration for the node at home. The backup is
// skipped because it copies the whole data directory before an upgrade,
// which a forked lab cannot afford.
func (c Cosmovisor) env(home string) []string {
	return []string{
		"DAEMON_HOME=" + home,
		"DAEMON_NAME=" + c.Name,
		"DAEMON_ALLOW_DOWNLOAD_BINARIES=" + strconv.FormatBool(c.Download),
		"DAEMON_DOWNLOAD_MUST_HAVE_CHECKSUM=true",
		"DAEMON_RESTART_AFTER_UPGRADE=true",
		"DAEMON_POLL_INTERVAL=300ms",
		"UNSAFE_SKIP_BACKUP=true",
		"COSMOVISOR_COLOR_LOGS=false",
		// Stated, not left to what the supervisor's environment has:
		// planSlot relies on cosmovisor lower-casing the plan name.
		"COSMOVISOR_DISABLE_RECASE=false",
	}
}

// environ is the environment of a cosmovisor that runs on the host: what
// the supervisor inherited, without any cosmovisor setting, then env. A
// validator operator's own DAEMON_ and COSMOVISOR_ exports would otherwise
// reach the lab's nodes.
func (c Cosmovisor) environ(home string, inherited []string) []string {
	own := c.env(home)
	out := make([]string, 0, len(inherited)+len(own))
	for _, kv := range inherited {
		key, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(key, "DAEMON_") || strings.HasPrefix(key, "COSMOVISOR_") ||
			slices.ContainsFunc(own, func(o string) bool { return strings.HasPrefix(o, key+"=") }) {
			continue
		}
		out = append(out, kv)
	}
	return append(out, own...)
}

// A slot is a directory under <home>/cosmovisor that holds bin/<name>;
// cosmovisor runs the one its current link names. Cosmovisor itself only
// ever moves the link to a plan's slot. manualSlot holds any other binary
// the supervisor is told to run; it is not under upgrades/, so no plan
// name leads to it.
const (
	genesisSlot = "genesis"
	manualSlot  = "forklab-manual"
)

// planSlot is where cosmovisor looks for a plan's binary. It lower-cases
// the plan name first, as every cosmovisor does unless it is started with
// COSMOVISOR_DISABLE_RECASE, which forklab leaves off like a validator who
// follows the defaults.
func planSlot(name string) string {
	return "upgrades/" + url.PathEscape(strings.ToLower(name))
}

// PlanName checks that an upgrade plan's name has a slot of its own. A
// slot is removed with its plan, and the slot of a plan named ".." would
// be the whole cosmovisor tree.
func PlanName(name string) error {
	if slot := strings.TrimPrefix(planSlot(name), "upgrades/"); slot == "" || slot == "." || slot == ".." {
		return errors.New("upgrade plan name " + strconv.Quote(name) + " is not usable: cosmovisor keeps a plan's binary in the directory upgrades/<name>, and that would not be a directory of the plan's own")
	}
	return nil
}

// cosmovisorDir is one node's <home>/cosmovisor tree.
type cosmovisorDir struct{ home, name string }

func (s NodeSpec) cosmovisorDir() cosmovisorDir {
	return cosmovisorDir{home: s.Home, name: s.Cosmovisor.Name}
}

func (d cosmovisorDir) root() string { return filepath.Join(d.home, "cosmovisor") }

func (d cosmovisorDir) bin(slot string) string {
	return filepath.Join(d.root(), slot, "bin", d.name)
}

// chainInfo is the upgrade-info.json the chain binary writes when it halts
// for a plan, and slotInfo cosmovisor's copy of it in the slot it switched
// to for that plan.
func (d cosmovisorDir) chainInfo() string {
	return filepath.Join(d.home, "data", "upgrade-info.json")
}

func (d cosmovisorDir) slotInfo(slot string) string {
	return filepath.Join(d.root(), slot, "upgrade-info.json")
}

// place puts a copy of binary in slot. A copy, not a hard link: cosmovisor
// sets every execute bit on the binary it runs, and through a link that
// would change the mode of the user's own file. On a filesystem that can
// share blocks between files the copy takes no space; elsewhere each slot
// of each node holds the binary once more. The file is replaced, never
// written in place, so a process running the old one is not disturbed.
func (d cosmovisorDir) place(slot, binary string) error {
	dst := d.bin(slot)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".tmp"
	if err := os.Remove(tmp); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := copyExecutable(binary, tmp); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

func copyExecutable(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// current returns the slot the current link names, or "" without a link.
func (d cosmovisorDir) current() string {
	target, err := os.Readlink(filepath.Join(d.root(), "current"))
	if err != nil {
		return ""
	}
	if filepath.IsAbs(target) {
		if target, err = filepath.Rel(d.root(), target); err != nil {
			return ""
		}
	}
	return filepath.Clean(target)
}

// setCurrent copies the chain's upgrade-info.json into slot, as cosmovisor
// does when it switches, and then points the current link at the slot. A
// cosmovisor started with a chain upgrade-info.json that names another plan
// than its current slot's stops the node at once to switch to that plan,
// so the copy is in place before the link can be seen.
func (d cosmovisorDir) setCurrent(slot string) error {
	info, err := os.ReadFile(d.chainInfo())
	switch {
	case err == nil:
		if err := writeFileAtomic(d.slotInfo(slot), info); err != nil {
			return err
		}
	case !errors.Is(err, os.ErrNotExist):
		return err
	}
	link := filepath.Join(d.root(), "current")
	tmp := link + ".tmp"
	if err := os.Remove(tmp); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Symlink(slot, tmp); err != nil {
		return err
	}
	return os.Rename(tmp, link)
}

// prepare readies the tree for a start. The first start creates it with
// binary in the genesis slot. Later starts make the switch cosmovisor does
// not make at startup: with an upgrade-info.json naming a plan the current
// slot has not applied, it runs the current binary, which halts again, even
// when the plan's binary is in place.
func (d cosmovisorDir) prepare(binary string) error {
	if d.current() == "" {
		if err := d.place(genesisSlot, binary); err != nil {
			return err
		}
		return d.setCurrent(genesisSlot)
	}
	plan := infoName(d.chainInfo())
	if plan == "" || infoName(d.slotInfo(d.current())) == plan || isExecutable(d.bin(planSlot(plan))) != nil {
		return nil
	}
	return d.setCurrent(planSlot(plan))
}

// infoName reads the plan name from an upgrade-info.json, "" when there is
// none.
func infoName(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var info struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(data, &info)
	return info.Name
}

// ResetCosmovisor removes a node's cosmovisor tree and the chain's
// upgrade-info.json, so its next start builds the tree again around
// spec.Binary. Nodes that do not run under cosmovisor are left alone.
func ResetCosmovisor(spec NodeSpec) error {
	if spec.Cosmovisor == nil {
		return nil
	}
	d := spec.cosmovisorDir()
	if err := os.RemoveAll(d.root()); err != nil {
		return err
	}
	if err := os.Remove(d.chainInfo()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
