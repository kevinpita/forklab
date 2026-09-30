package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kevinpita/forklab/internal/chain"
	"github.com/kevinpita/forklab/internal/supervisor"
)

func testController(t *testing.T, overshoot bool) (Controller, func()) {
	t.Helper()
	root, err := os.MkdirTemp("", "fl-control-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	t.Setenv("FORKLAB_HOME", root)
	dir := filepath.Join(root, "lab")
	home := filepath.Join(dir, "node0")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "mock-chain")
	script := `#!/bin/sh
if [ "$1" = version ];then printf '%s' '{"cosmos_sdk_version":"v0.53.8"}';exit 0;fi
if [ "$2" = --help ];then echo ' --halt-height uint';exit 0;fi
shift
while [ "$#" -gt 0 ];do
 case "$1" in
 --home) shift; mock_home=$1;;
 --halt-height) shift; mock_halt=$1;;
 esac
 shift
done
mock_height=$(cat "$mock_home/height")
if [ -n "${mock_halt:-}" ];then
 mock_height=$((mock_halt-1+OVERSHOOT))
 echo "halt per configuration height $mock_halt time 0"
else
 mock_height=$((mock_height+1))
fi
printf '%s' "$mock_height" > "$mock_home/height"
trap 'exit 0' TERM INT
while :;do sleep 0.05;done
`
	over := 0
	if overshoot {
		over = 1
	}
	script = strings.ReplaceAll(script, "OVERSHOOT", fmt.Sprint(over))
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "height"), []byte("2"), 0o644); err != nil {
		t.Fatal(err)
	}
	spec := supervisor.NodeSpec{Index: 0, Name: "node0", Binary: bin, Args: []string{"start", "--home", home}, Home: home, LogPath: filepath.Join(home, "node.log"), PidPath: filepath.Join(home, "node.pid")}
	if err := supervisor.SaveNodes(dir, []supervisor.NodeSpec{spec}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, _ := os.ReadFile(filepath.Join(home, "height"))
		height := strings.TrimSpace(string(h))
		if height == "" {
			height = "0"
		}
		if r.URL.Path != "/abci_info" {
			http.Error(w, "use application state", 500)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"response": map[string]any{"last_block_height": height, "last_block_app_hash": "AB"}}})
	}))
	t.Cleanup(srv.Close)
	rpc, err := chain.New(srv.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var cancel context.CancelFunc
	var done chan error
	start := func() *supervisor.Client {
		ctx, stop := context.WithCancel(context.Background())
		cancel = stop
		done = make(chan error, 1)
		go func() { done <- supervisor.Run(ctx, supervisor.Options{LabDir: dir}) }()
		for until := time.Now().Add(3 * time.Second); time.Now().Before(until); {
			if c, err := supervisor.Dial(dir); err == nil {
				return c
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("supervisor did not start")
		return nil
	}
	c := Controller{Dir: dir, Supervisor: start(), Clients: []*chain.Client{rpc}}
	t.Cleanup(func() {
		if client, err := supervisor.Dial(dir); err == nil {
			_, _ = client.Stop(supervisor.All, time.Second)
		}
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("supervisor did not exit")
		}
	})
	adopt := func() {
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		c.Supervisor = start()
	}
	return c, func() { adopt() }
}

func TestExactPauseAndResumeRestoreArguments(t *testing.T) {
	c, _ := testController(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	original, err := supervisor.LoadNodes(c.Dir)
	if err != nil {
		t.Fatal(err)
	}
	st, err := c.Pause(ctx, 10)
	if err != nil || st.Phase != "paused" {
		t.Fatalf("pause=%+v err=%v", st, err)
	}
	info, err := c.Clients[0].AppInfo(ctx)
	if err != nil || info.Height != 10 {
		t.Fatalf("info=%+v err=%v", info, err)
	}
	persisted, err := Load(c.Dir)
	if err != nil || persisted.Phase != "paused" {
		t.Fatalf("state=%+v err=%v", persisted, err)
	}
	if err := c.Resume(ctx); err != nil {
		t.Fatal(err)
	}
	specs, err := supervisor.LoadNodes(c.Dir)
	if err != nil || !reflect.DeepEqual(specs[0].Args, original[0].Args) {
		t.Fatalf("args=%v err=%v", specs, err)
	}
	if state, err := Load(c.Dir); err != nil || state != nil {
		t.Fatalf("state=%v err=%v", state, err)
	}
}

func TestFailedExactPauseKeepsRecoverableBarrier(t *testing.T) {
	c, _ := testController(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, err := c.Pause(ctx, 10)
	if err == nil || st == nil || !strings.Contains(err.Error(), "instead of 10") {
		t.Fatalf("pause=%+v err=%v", st, err)
	}
	if _, err := c.Pause(ctx, 20); err == nil {
		t.Fatal("second barrier accepted")
	}
	if err := c.Resume(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestCancelledPauseResumesFromActualHeight(t *testing.T) {
	c, _ := testController(t, false)
	specs, err := supervisor.LoadNodes(c.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := save(c.Dir, State{Height: 100000, Phase: "arming", Args: [][]string{specs[0].Args}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := c.Resume(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestPauseCapabilityAndLockFailBeforeMutation(t *testing.T) {
	dir := t.TempDir()
	unlock, err := lock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err := lock(dir); err == nil {
		t.Fatal("concurrent operation accepted")
	}
	bin := filepath.Join(dir, "old-chain")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf '%s' '{\"cosmos_sdk_version\":\"v0.47.17\"}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := (SDKFinalizeHalt{}).Arguments(context.Background(), bin, 20); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("old SDK err=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (SDKFinalizeHalt{}).Arguments(ctx, bin, 20); err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel err=%v", err)
	}
}

func TestPauseSurvivesSupervisorAdoption(t *testing.T) {
	c, adopt := testController(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.Pause(ctx, 10); err != nil {
		t.Fatal(err)
	}
	adopt()
	if err := c.Resume(ctx); err != nil {
		t.Fatal(err)
	}
}
