package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestFixture is a fake forklab. The runner tests re-exec the test binary
// into it, so the real exec, pipe, and decode paths run against canned CLI
// output captured from a live lab.
func TestFixture(t *testing.T) {
	if os.Getenv("FORKLAB_TUI_FIXTURE") == "" {
		t.Skip("fixture process only")
	}
	args := os.Args[slices.Index(os.Args, "--")+1:]
	args = slices.DeleteFunc(args, func(s string) bool { return s == "--progress=json" })
	code := fixture(strings.Join(args, " "))
	os.Exit(code)
}

func fixture(argv string) int {
	file := func(name string) {
		data, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(9)
		}
		_, _ = os.Stdout.Write(data)
	}
	switch argv {
	case "node list --json":
		file("node_list_stopped.json")
	case "node stop 0 --json", "node start 1 --json":
		file("node_list_stopped.json")
	case "node stop 7 --json":
		file("err_node.json")
		return 1
	case "exec --json -- bogus":
		file("exec_fail.json")
		return 1
	case "crash --json":
		fmt.Fprintln(os.Stderr, "panic: boom")
		return 2
	case "status -w --json":
		file("status_w.ndjson")
		fmt.Println(`{"error":{"code":"error","message":"rpc status: connection refused"}}`)
	case "profile list --json":
		file("profile_list.json")
	case "profile show lsimd --json":
		file("profile_show.json")
	case "lab list --json":
		file("lab_list.json")
	case "account list --json":
		file("account_list.json")
	case "gov list --json":
		file("gov_list.json")
	case "lab create devnet --profile lsimd --version 0.53.8 --validators 2 --json":
		fmt.Println(`{"ok":true,"data":{"name":"devnet"}}`)
	case "lab create taken --profile lsimd --version 0.53.8 --validators 2 --json":
		fmt.Println(`{"ok":false,"error":{"code":"error","message":"lab taken already exists"}}`)
		return 1
	case "node logs 0 -f --tail 500 --json":
		file("node_logs.ndjson")
		time.Sleep(time.Minute)
	default:
		fmt.Printf(`{"ok":false,"error":{"code":"usage","message":"fixture has no %q"}}`+"\n", argv)
		return 2
	}
	return 0
}

func fixtureRunner() Runner {
	return Runner{
		Exe:    os.Args[0],
		Prefix: []string{"-test.run=^TestFixture$", "--"},
		Env:    append(os.Environ(), "FORKLAB_TUI_FIXTURE=1"),
	}
}

func TestRunDecodesEnvelopes(t *testing.T) {
	r := fixtureRunner()
	ctx := context.Background()

	ok := r.Run(ctx, Command{"node", "list"})
	var nodes []nodeInfo
	if ok.Err != nil || decodeInto(ok.Data, &nodes) != nil || len(nodes) != 2 || nodes[1].State != "exited" {
		t.Fatalf("node list: err %v, nodes %+v", ok.Err, nodes)
	}

	bad := r.Run(ctx, Command{"node", "stop", "7"})
	var ce *CLIError
	if !errors.As(bad.Err, &ce) || ce.Code != "error" || !strings.Contains(ce.Message, `node "7"`) {
		t.Fatalf("node stop 7: err %#v, want the envelope's error", bad.Err)
	}

	// exec reports a failed child as ok false with data and no error body;
	// the data is still what the user needs to see.
	ex := r.Run(ctx, Command{"exec", "--", "bogus"})
	var res execResult
	if ex.Err == nil {
		t.Fatal("exec bogus: failed child reported ok")
	}
	if err := json.Unmarshal(ex.Data, &res); err != nil || res.ExitCode != 1 || !strings.Contains(res.Stderr, "unknown command") {
		t.Fatalf("exec data = %+v, %v", res, err)
	}

	crash := r.Run(ctx, Command{"crash"})
	if crash.Err == nil || !strings.Contains(crash.Err.Error(), "panic: boom") {
		t.Fatalf("crash err = %v, want the stderr line", crash.Err)
	}
}

func TestStreamSendsLinesThenDone(t *testing.T) {
	ch := make(chan streamEvent, 64)
	go fixtureRunner().Stream(context.Background(), Command{"status", "-w"}, streamStatus, 7, ch)
	var heights []int64
	var pollErr error
	for ev := range ch {
		if ev.session != 7 || ev.kind != streamStatus {
			t.Fatalf("event tagged %v/%d, want status/7", ev.kind, ev.session)
		}
		if ev.done {
			if ev.err != nil {
				t.Fatalf("clean exit reported %v", ev.err)
			}
			break
		}
		var s labStatus
		if err := decodeLine(ev.line, &s); err != nil {
			pollErr = err
			continue
		}
		heights = append(heights, s.Height)
	}
	if !slices.Equal(heights, []int64{11, 12, 13}) {
		t.Errorf("heights = %v, want 11 12 13", heights)
	}
	if pollErr == nil || pollErr.Error() != "rpc status: connection refused" {
		t.Errorf("poll error = %v", pollErr)
	}
}

func TestStreamStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan streamEvent)
	finished := make(chan struct{})
	go func() {
		fixtureRunner().Stream(ctx, logsCmd("0"), streamLogs, 1, ch)
		close(finished)
	}()
	var l logLine
	if err := decodeLine((<-ch).line, &l); err != nil || !strings.Contains(l.Line, "IAVL") {
		t.Fatalf("first log line = %+v, %v", l, err)
	}
	cancel()
	select {
	case <-finished:
	case <-time.After(10 * time.Second):
		t.Fatal("stream kept running after cancel")
	}
	select {
	case ev := <-ch:
		t.Fatalf("canceled stream sent %+v", ev)
	default:
	}
}

func TestCommandString(t *testing.T) {
	tests := []struct {
		cmd  Command
		want string
	}{
		{Command{"node", "stop", "1"}, "forklab node stop 1 --json"},
		{Command{"exec", "--", "q", "bank", "total"}, "forklab exec --json -- q bank total"},
		{Command{"gov", "submit", "my file.json"}, "forklab gov submit 'my file.json' --json"},
	}
	for _, tt := range tests {
		if got := tt.cmd.String(); got != tt.want {
			t.Errorf("%q.String() = %q, want %q", []string(tt.cmd), got, tt.want)
		}
	}
}

func TestSplitArgs(t *testing.T) {
	got, err := splitArgs(` exec -- q bank balances "cosmos1 x" 'a"b'`)
	want := []string{"exec", "--", "q", "bank", "balances", "cosmos1 x", `a"b`}
	if err != nil || !slices.Equal(got, want) {
		t.Errorf("splitArgs = %q, %v; want %q", got, err, want)
	}
	if _, err := splitArgs(`node "stop`); err == nil {
		t.Error("unterminated quote accepted")
	}
}
