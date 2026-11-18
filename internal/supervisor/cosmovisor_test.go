package supervisor

import (
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestCosmovisorPrepareSwitchesOnlyToAPlanThatIsReady(t *testing.T) {
	const bin = "/bin/sh"
	halt := func(t *testing.T, d cosmovisorDir) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(d.chainInfo()), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(d.chainInfo(), []byte(`{"name":"v2","height":50}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	place := func(t *testing.T, d cosmovisorDir, slot string) {
		t.Helper()
		if err := d.place(slot, bin); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		name  string
		setup func(t *testing.T, d cosmovisorDir)
		want  string
	}{
		{"first start builds the genesis slot", func(*testing.T, cosmovisorDir) {}, genesisSlot},
		{"a halt without the plan's binary keeps the slot", func(t *testing.T, d cosmovisorDir) {
			place(t, d, genesisSlot)
			_ = d.setCurrent(genesisSlot)
			halt(t, d)
		}, genesisSlot},
		{"a halt with the plan's binary placed switches to it", func(t *testing.T, d cosmovisorDir) {
			place(t, d, genesisSlot)
			_ = d.setCurrent(genesisSlot)
			halt(t, d)
			place(t, d, planSlot("v2"))
		}, planSlot("v2")},
		{"a slot set after the halt is not switched away from", func(t *testing.T, d cosmovisorDir) {
			halt(t, d)
			place(t, d, planSlot("v2"))
			place(t, d, manualSlot)
			_ = d.setCurrent(manualSlot)
		}, manualSlot},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := cosmovisorDir{home: t.TempDir(), name: "chaind"}
			tt.setup(t, d)
			if err := d.prepare(bin); err != nil {
				t.Fatal(err)
			}
			if got := d.current(); got != tt.want {
				t.Fatalf("current = %q, want %q", got, tt.want)
			}
			if err := isExecutable(d.bin(tt.want)); err != nil {
				t.Errorf("current slot has no binary: %v", err)
			}
			if plan := infoName(d.chainInfo()); plan != "" && tt.want != genesisSlot && infoName(d.slotInfo(tt.want)) != plan {
				t.Errorf("slot %s lacks the upgrade-info.json of plan %s", tt.want, plan)
			}
		})
	}
}

func TestKillOrphansDoesNotWaitForAProcessItMayNotKill(t *testing.T) {
	spec := NodeSpec{Home: t.TempDir(), Cosmovisor: &Cosmovisor{Name: "chaind"}}
	bin := spec.cosmovisorDir().bin(genesisSlot)
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/bin/sh", bin); err != nil {
		t.Fatal(err)
	}
	// The shell waits on a stdin that stays open, as an orphan stays up.
	orphan := exec.Command(bin, "-c", "read line")
	stdin, err := orphan.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := orphan.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = stdin.Close()
		_ = orphan.Wait()
	}()

	orphanKill = func(int, syscall.Signal) error { return syscall.EPERM }
	start := time.Now()
	killOrphans(spec)
	orphanKill = syscall.Kill
	if took := time.Since(start); took > killTimeout/2 {
		t.Errorf("killOrphans waited %s for a process it may not kill", took)
	}
	if !alive(orphan.Process.Pid) {
		t.Fatal("the refused kill ended the process anyway")
	}
	killOrphans(spec)
	if err := orphan.Wait(); err == nil {
		t.Error("killOrphans left a process it may kill")
	}
}

// startOrphan runs a process out of spec's cosmovisor tree, as the chain
// binary of a cosmovisor that died does. The shell waits on a stdin that
// stays open.
func startOrphan(t *testing.T, spec NodeSpec) (exited <-chan struct{}) {
	t.Helper()
	bin := spec.cosmovisorDir().bin(genesisSlot)
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/bin/sh", bin); err != nil {
		t.Fatal(err)
	}
	orphan := exec.Command(bin, "-c", "read line")
	stdin, err := orphan.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := orphan.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		_ = orphan.Wait()
		close(done)
	}()
	t.Cleanup(func() {
		_ = stdin.Close()
		<-done
	})
	return done
}

func TestPlanSlotIsWhereCosmovisorLooks(t *testing.T) {
	for name, want := range map[string]string{
		"v2":             "upgrades/v2",
		"UpgradeV2":      "upgrades/upgradev2",
		"v2 rc/1":        "upgrades/v2%20rc%2F1",
		"forklab-manual": "upgrades/forklab-manual",
	} {
		if got := planSlot(name); got != want {
			t.Errorf("planSlot(%q) = %q, want %q: cosmovisor lower-cases the name, then path-escapes it", name, got, want)
		}
	}
	if planSlot("forklab-manual") == manualSlot || planSlot("Forklab-Manual") == manualSlot {
		t.Errorf("a plan can be named into the manual slot %q", manualSlot)
	}
}

func TestPlaceGivesTheSlotAFileOfItsOwn(t *testing.T) {
	src := filepath.Join(t.TempDir(), "chaind")
	if err := os.WriteFile(src, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	d := cosmovisorDir{home: t.TempDir(), name: "chaind"}
	if err := d.place(genesisSlot, src); err != nil {
		t.Fatal(err)
	}
	// Cosmovisor sets every execute bit on the binary it runs.
	if err := os.Chmod(d.bin(genesisSlot), 0o755); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(src); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("the source binary is now %v, %v; want its mode left at 0700", info.Mode().Perm(), err)
	}
}

func TestOrphanOfADeadCosmovisorIsLiveUntilASupervisorEndsIt(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "node0")
	spec := NodeSpec{
		Name: "node0", Binary: "/bin/sh", Args: []string{"start", "--home", home}, Home: home,
		LogPath: filepath.Join(home, "node.log"), PidPath: filepath.Join(home, "node.pid"),
		Cosmovisor: &Cosmovisor{Path: "/x/cosmovisor", Name: "chaind"},
	}
	orphanExited := startOrphan(t, spec)
	dead := exec.Command("/bin/sh", "-c", "exit 0")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	if err := writePidFile(spec.PidPath, pidFile{PID: dead.Process.Pid, Binary: spec.Binary, Exec: spec.Cosmovisor.Path}); err != nil {
		t.Fatal(err)
	}
	if err := SaveNodes(dir, []NodeSpec{spec}); err != nil {
		t.Fatal(err)
	}
	if live, err := LiveNodes(dir); err != nil || len(live) != 1 {
		t.Errorf("LiveNodes = %v, %v; want the node live: its chain binary still holds the database", live, err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	n := &node{log: log.New(io.Discard, "", 0), stop: stop, wg: &wg, rt: hostRuntime{}, spec: spec}
	if err := n.adopt(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-orphanExited:
	case <-time.After(3 * time.Second):
		t.Error("a new supervisor left the orphaned chain binary running")
	}
	for deadline := time.Now().Add(3 * time.Second); n.status().State != StateExited && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	if st := n.status(); st.State != StateExited {
		t.Errorf("node = %+v, want it exited once the orphan is gone", st)
	}
	close(stop)
	wg.Wait()
}

func TestClearingAPlanRemovesItsStagedBinary(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "node0")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	spec := NodeSpec{Name: "node0", Binary: "/bin/sh", Home: home, Cosmovisor: &Cosmovisor{Path: "/bin/sh", Name: "chaind"}}
	s := &supervisor{paths: Paths{Dir: dir}, log: log.New(io.Discard, "", 0), nodes: []*node{{spec: spec}}}
	plan := &Upgrade{Name: "v2", Height: 50, Version: "2", Binary: "/bin/sh", AutoSwap: true}
	d := spec.cosmovisorDir()
	staged := d.bin(planSlot("v2"))

	if err := s.setUpgrade(plan); err != nil {
		t.Fatal(err)
	}
	if err := isExecutable(staged); err != nil {
		t.Fatalf("the plan's binary was not staged: %v", err)
	}
	if err := s.setUpgrade(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Errorf("the staged binary outlived its plan (%v): a later plan of the same name would run it", err)
	}

	// The slot the node runs from stays.
	if err := s.setUpgrade(plan); err != nil {
		t.Fatal(err)
	}
	if err := d.setCurrent(planSlot("v2")); err != nil {
		t.Fatal(err)
	}
	if err := s.setUpgrade(nil); err != nil {
		t.Fatal(err)
	}
	if err := isExecutable(staged); err != nil {
		t.Errorf("clearing the plan removed the binary the node runs: %v", err)
	}
}

// A supervisor that died before it wrote the pid file, or a cosmovisor
// whose chain binary could not be killed when it exited, leaves a chain
// process that no file records.
func TestOrphanWithoutAPidFileIsEndedByTheNextSupervisor(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "node0")
	spec := NodeSpec{
		Name: "node0", Binary: "/bin/sh", Args: []string{"start", "--home", home}, Home: home,
		LogPath: filepath.Join(home, "node.log"), PidPath: filepath.Join(home, "node.pid"),
		Cosmovisor: &Cosmovisor{Path: "/x/cosmovisor", Name: "chaind"},
	}
	orphanExited := startOrphan(t, spec)
	if err := SaveNodes(dir, []NodeSpec{spec}); err != nil {
		t.Fatal(err)
	}
	if live, err := LiveNodes(dir); err != nil || len(live) != 1 {
		t.Errorf("LiveNodes = %v, %v; want the node live", live, err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	n := &node{log: log.New(io.Discard, "", 0), stop: stop, wg: &wg, rt: hostRuntime{}, spec: spec}
	if err := n.adopt(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-orphanExited:
	case <-time.After(3 * time.Second):
		t.Error("a new supervisor left the unrecorded chain binary running: the lab stays live and nothing can stop it")
	}
	close(stop)
	wg.Wait()
}

func TestAPlanThatReplacesAnotherDoesNotInheritItsBinary(t *testing.T) {
	bins := t.TempDir()
	oldBin, newBin := filepath.Join(bins, "old"), filepath.Join(bins, "new")
	for path, body := range map[string]string{oldBin: "#!/bin/sh\necho old\n", newBin: "#!/bin/sh\necho new\n"} {
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	first := Upgrade{Name: "v2", Height: 50, Version: "2", Binary: oldBin, AutoSwap: true}
	for _, tt := range []struct {
		name string
		next Upgrade
		// want is the binary in the slot afterwards, empty for none.
		want string
	}{
		{"the same name again, to be delivered by hand", Upgrade{Name: "v2", Height: 80, Version: "3", Binary: newBin}, ""},
		{"a name that differs only in case", Upgrade{Name: "V2", Height: 80, Version: "3", Binary: newBin}, ""},
		{"the same name again, placed by forklab", Upgrade{Name: "v2", Height: 80, Version: "3", Binary: newBin, AutoSwap: true}, newBin},
		{"the same plan with its proposal", Upgrade{Name: "v2", Height: 50, Version: "2", Binary: oldBin, AutoSwap: true, ProposalID: 7}, oldBin},
	} {
		dir := t.TempDir()
		home := filepath.Join(dir, "node0")
		if err := os.MkdirAll(home, 0o755); err != nil {
			t.Fatal(err)
		}
		spec := NodeSpec{Name: "node0", Binary: oldBin, Home: home, Cosmovisor: &Cosmovisor{Path: "/bin/sh", Name: "chaind"}}
		s := &supervisor{paths: Paths{Dir: dir}, log: log.New(io.Discard, "", 0), nodes: []*node{{spec: spec}}}
		plan := first
		if err := s.setUpgrade(&plan); err != nil {
			t.Fatal(err)
		}
		if err := s.setUpgrade(&tt.next); err != nil {
			t.Fatal(err)
		}
		got, _ := os.ReadFile(spec.cosmovisorDir().bin(planSlot("v2")))
		want, _ := os.ReadFile(tt.want)
		if string(got) != string(want) {
			t.Errorf("%s: the slot holds %q, want %q: cosmovisor runs whatever is there at the height", tt.name, got, want)
		}
	}

	// A plan the operator delivers by hand keeps what was delivered when
	// the same plan is set again, as upgrade schedule does once the
	// proposal has an id.
	dir := t.TempDir()
	home := filepath.Join(dir, "node0")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	spec := NodeSpec{Name: "node0", Binary: oldBin, Home: home, Cosmovisor: &Cosmovisor{Path: "/bin/sh", Name: "chaind"}}
	s := &supervisor{paths: Paths{Dir: dir}, log: log.New(io.Discard, "", 0), nodes: []*node{{spec: spec}}}
	manual := Upgrade{Name: "v2", Height: 50, Version: "2", Binary: newBin}
	if err := s.setUpgrade(&manual); err != nil {
		t.Fatal(err)
	}
	if err := spec.cosmovisorDir().place(planSlot("v2"), newBin); err != nil {
		t.Fatal(err)
	}
	manual.ProposalID = 7
	if err := s.setUpgrade(&manual); err != nil {
		t.Fatal(err)
	}
	if err := isExecutable(spec.cosmovisorDir().bin(planSlot("v2"))); err != nil {
		t.Errorf("the binary the operator delivered was removed when the plan got its proposal: %v", err)
	}
}

func TestDownloadLabGetsNoBinaryPlaced(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "node0")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	spec := NodeSpec{Name: "node0", Binary: "/bin/sh", Home: home, Cosmovisor: &Cosmovisor{Path: "/bin/sh", Name: "chaind", Download: true}}
	s := &supervisor{paths: Paths{Dir: dir}, log: log.New(io.Discard, "", 0), nodes: []*node{{spec: spec}}}
	if err := s.setUpgrade(&Upgrade{Name: "v2", Height: 50, Version: "2", Binary: "/bin/sh", AutoSwap: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(spec.cosmovisorDir().bin(planSlot("v2"))); !os.IsNotExist(err) {
		t.Errorf("forklab placed the binary that cosmovisor is to download (%v)", err)
	}
}

// The slot of a plan is removed with the plan, so it must be a directory
// of its own under upgrades.
func TestPlanNamesThatAreNotASlotOfTheirOwnAreRefused(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "node0")
	spec := NodeSpec{Name: "node0", Binary: "/bin/sh", Home: home, Cosmovisor: &Cosmovisor{Path: "/bin/sh", Name: "chaind"}}
	d := spec.cosmovisorDir()
	for _, slot := range []string{genesisSlot, planSlot("v1")} {
		if err := d.place(slot, "/bin/sh"); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.setCurrent(planSlot("v1")); err != nil {
		t.Fatal(err)
	}
	n := &node{spec: spec}
	s := &supervisor{paths: Paths{Dir: dir}, log: log.New(io.Discard, "", 0), nodes: []*node{n}}
	for _, name := range []string{"..", ".", ""} {
		if err := s.setUpgrade(&Upgrade{Name: name, Height: 50, Version: "2", Binary: "/bin/sh"}); err == nil {
			t.Errorf("a plan named %q was accepted", name)
			_ = s.setUpgrade(nil)
		}
		// A plan that a state file from elsewhere carries is not acted on.
		s.unstage(n, &Upgrade{Name: name})
		for _, slot := range []string{genesisSlot, planSlot("v1")} {
			if err := isExecutable(d.bin(slot)); err != nil {
				t.Fatalf("a plan named %q took the %s slot with it: %v", name, slot, err)
			}
		}
	}
	for _, name := range []string{"v2", "a/b", "..."} {
		if err := PlanName(name); err != nil {
			t.Errorf("PlanName(%q) = %v", name, err)
		}
	}
}

// Cosmovisor starts the chain binary at the path its own symlink
// resolution gives, so with the labs directory reached through a symlink
// the orphan does not name the node home as forklab knows it.
func TestOrphanIsFoundWhenTheLabIsReachedThroughASymlink(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "labs")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	cv := &Cosmovisor{Path: "/x/cosmovisor", Name: "chaind"}
	startOrphan(t, NodeSpec{Home: filepath.Join(real, "node0"), Cosmovisor: cv})
	if got := chainProcesses(NodeSpec{Home: filepath.Join(link, "node0"), Cosmovisor: cv}); len(got) != 1 {
		t.Errorf("chain processes of the node = %v, want the one orphan", got)
	}
}

// A validator operator's own cosmovisor settings in the shell that starts
// the supervisor are not the lab's.
func TestHostCosmovisorTakesItsSettingsFromForklabOnly(t *testing.T) {
	cv := Cosmovisor{Path: "/x/cosmovisor", Name: "chaind"}
	got := cv.environ("/labs/lab/node0", []string{"PATH=/bin", "DAEMON_SHUTDOWN_GRACE=30s", "DAEMON_HOME=/validator", "COSMOVISOR_DISABLE_RECASE=true", "UNSAFE_SKIP_BACKUP=false", "HOME=/home/op"})
	want := append([]string{"PATH=/bin", "HOME=/home/op"}, cv.env("/labs/lab/node0")...)
	if !slices.Equal(got, want) {
		t.Errorf("environment = %q, want %q", got, want)
	}
}
