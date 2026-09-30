package runbook

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kevinpita/forklab/internal/chain"
)

type fakeRuntime struct {
	calls  []string
	args   []string
	code   uint32
	paused bool
}

func (f *fakeRuntime) WaitHeight(_ context.Context, h int64) (int64, error) {
	f.calls = append(f.calls, "height")
	return h + 1, nil
}

func (f *fakeRuntime) Tx(_ context.Context, from string, args []string) (Transaction, error) {
	f.calls = append(f.calls, "tx:"+from)
	f.args = args
	return Transaction{Hash: "AB", Height: 21, Code: f.code}, nil
}

func (f *fakeRuntime) Query(context.Context, []string) (any, error) {
	f.calls = append(f.calls, "query")
	return map[string]any{"amount": "123456789012345678901234567890"}, nil
}

func (f *fakeRuntime) Store(_ context.Context, r chain.StoreRequest) (any, error) {
	f.calls = append(f.calls, "store")
	return chain.StoreResult{Height: 30, KeyHex: r.KeyHex, ValueHex: "aabb"}, nil
}

func (f *fakeRuntime) Pause(_ context.Context, h int64) (any, error) {
	f.calls = append(f.calls, "pause")
	f.paused = true
	return map[string]any{"height": h}, nil
}

func (f *fakeRuntime) Resume(context.Context) error {
	f.calls = append(f.calls, "resume")
	f.paused = false
	return nil
}

func (f *fakeRuntime) WaitResumed(context.Context) error {
	f.calls = append(f.calls, "hold")
	f.paused = false
	return nil
}

func decode(t *testing.T, s string) Document {
	t.Helper()
	d, err := Decode(strings.NewReader(s))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestRecipeOrderTemplatesAssertionsAndPause(t *testing.T) {
	d := decode(t, `version: 1
vars: {amount: '10'}
steps:
 - id: balance
   query: [bank, balances, '{{ .accounts.val1.address }}']
 - id: send
   at_height: 20
   tx:
     from: val0
     args: [bank, send, '{{ .accounts.val0.address }}', '{{ .accounts.val1.address }}', '{{ .vars.amount }}{{ .fee_denom }}']
 - assert: '.steps.send.code == 0 and .steps.send.height == 21 and .steps.balance.amount == "123456789012345678901234567890"'
 - pause: 30
 - id: raw
   store: {name: bank, key_hex: '01'}
 - assert: '.steps.raw.height == 30 and .steps.raw.value_hex == "aabb"'
 - hold: true
 - resume: true
`)
	f := &fakeRuntime{}
	r := Runner{Runtime: f, Values: map[string]any{"fee_denom": "stake", "accounts": map[string]any{"val0": map[string]any{"address": "addr0"}, "val1": map[string]any{"address": "addr1"}}}}
	report, err := r.Run(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Steps) != 8 || !reflect.DeepEqual(f.calls, []string{"query", "height", "tx:val0", "pause", "store", "hold", "resume"}) {
		t.Fatalf("steps=%+v calls=%v", report.Steps, f.calls)
	}
	if !reflect.DeepEqual(f.args, []string{"bank", "send", "addr0", "addr1", "10stake"}) {
		t.Fatalf("args=%v", f.args)
	}
}

func TestFailuresStopBeforeLaterTransactionsAndKeepPause(t *testing.T) {
	for _, expr := range []string{"false", "true, true", "null", "{}", "error(\"bad state\")"} {
		t.Run(expr, func(t *testing.T) {
			h := int64(30)
			d := Document{Version: 1, Steps: []Step{{Pause: &h}, {Assert: expr}, {Tx: &Tx{From: "val1", Args: []string{"bank", "send"}}}}}
			f := &fakeRuntime{}
			report, err := (Runner{Runtime: f}).Run(context.Background(), d)
			if err == nil || len(report.Steps) != 2 || !f.paused || report.Steps[1].Error == "" {
				t.Fatalf("report=%+v err=%v paused=%v", report, err, f.paused)
			}
			if !reflect.DeepEqual(f.calls, []string{"pause"}) {
				t.Fatalf("calls=%v", f.calls)
			}
		})
	}
}

func TestExpectedTransactionFailureAndMissingTemplate(t *testing.T) {
	d := decode(t, `version: 1
steps:
 - id: rejected
   tx: {from: val0, args: [staking, delegate], expect_code: 7}
 - assert: '.steps.rejected.code == 7'
`)
	f := &fakeRuntime{code: 7}
	if _, err := (Runner{Runtime: f}).Run(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	d.Steps[0].Tx.ExpectCode = 0
	report, err := (Runner{Runtime: f}).Run(context.Background(), d)
	if err == nil || len(report.Steps) != 1 || report.Steps[0].Output == nil {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	f.calls = nil
	d.Steps[0].Tx.Args = []string{"{{ .missing }}"}
	if _, err := (Runner{Runtime: f}).Run(context.Background(), d); err == nil || len(f.calls) != 0 {
		t.Fatalf("missing template err=%v calls=%v", err, f.calls)
	}
}

func TestScriptsReadContextUseRecipeDirectoryAndStopOnFailure(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "check.sh"), []byte("#!/bin/sh\ncat > input.json\nprintf '%s' '{\"checked\":true}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	d := Document{Version: 1, Vars: map[string]any{"case": "demo"}, Steps: []Step{{ID: "script", Script: []string{"./check.sh"}}, {Assert: ".steps.script.json.checked == true"}}}
	f := &fakeRuntime{}
	report, err := (Runner{Runtime: f, Dir: dir}).Run(context.Background(), d)
	if err != nil || len(report.Steps) != 2 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	input, err := os.ReadFile(filepath.Join(dir, "input.json"))
	if err != nil || !strings.Contains(string(input), `"case":"demo"`) {
		t.Fatalf("stdin=%s err=%v", input, err)
	}
	d.Steps = []Step{{Script: []string{"sh", "-c", "echo intentional >&2; exit 3"}}, {Query: []string{"bank", "total"}}}
	report, err = (Runner{Runtime: f, Dir: dir}).Run(context.Background(), d)
	if err == nil || len(report.Steps) != 1 || len(f.calls) != 0 || !strings.Contains(report.Error, "intentional") {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	d.Steps = []Step{{Timeout: "10ms", Script: []string{"sleep", "10"}}}
	start := time.Now()
	_, err = (Runner{Runtime: f}).Run(context.Background(), d)
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("cancellation err=%v duration=%s", err, time.Since(start))
	}
}

func TestValidationRejectsBadRecipesBeforeExecution(t *testing.T) {
	cases := []string{
		"version: 2\nsteps: [{resume: true}]",
		"version: 1\nsteps: [{tx: {from: val0, args: [bank]}, query: [bank]}]",
		"version: 1\nsteps: [{id: duplicate, resume: true}, {id: duplicate, resume: true}]",
		"version: 1\nsteps: [{pause: 0}]",
		"version: 1\nsteps: [{query: []}]",
		"version: 1\nsteps: [{hold: true, timeout: forever}]",
		"version: 1\nsteps: [{assert: 'unknown_function'}]",
		"version: 1\nsteps: [{tx: {from: val0, args: [bank], typo: true}}]",
		"version: 1\nsteps: [{resume: true}]\n---\nversion: 1",
	}
	for _, s := range cases {
		if _, err := Decode(strings.NewReader(s)); err == nil {
			t.Errorf("accepted %q", s)
		}
	}
	f := &fakeRuntime{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (Runner{Runtime: f}).Run(ctx, Document{Version: 1, Steps: []Step{{Assert: "true"}}})
	if err == nil || !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("cancel err=%v", err)
	}
}

type waitingRuntime struct {
	fakeRuntime
	resumed <-chan struct{}
}

func (f *waitingRuntime) WaitResumed(ctx context.Context) error {
	select {
	case <-f.resumed:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestHoldWaitsForManualResumeWithoutDefaultTimeout(t *testing.T) {
	resumed := make(chan struct{})
	f := &waitingRuntime{resumed: resumed}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() { time.Sleep(40 * time.Millisecond); close(resumed) }()
	report, err := (Runner{Runtime: f, StepTimeout: time.Millisecond}).Run(ctx, Document{Version: 1, Steps: []Step{{Hold: true}, {Assert: "true"}}})
	if err != nil || len(report.Steps) != 2 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	resumed = make(chan struct{})
	f.resumed = resumed
	report, err = (Runner{Runtime: f}).Run(ctx, Document{Version: 1, Steps: []Step{{Hold: true, Timeout: "1ms"}, {Assert: "true"}}})
	if err == nil || len(report.Steps) != 1 {
		t.Fatalf("explicit hold timeout ignored: %+v %v", report, err)
	}
}

func TestCancelledRecipeNeverBroadcasts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := &fakeRuntime{}
	report, err := (Runner{Runtime: f}).Run(ctx, Document{Version: 1, Steps: []Step{{Tx: &Tx{From: "val0", Args: []string{"bank", "send"}}}}})
	if err == nil || len(f.calls) != 0 || len(report.Steps) != 1 {
		t.Fatalf("calls=%v report=%+v err=%v", f.calls, report, err)
	}
}

func TestScriptTimeoutKillsBackgroundChild(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "child-survived")
	script := "(sleep 0.2; touch '" + marker + "') & wait"
	start := time.Now()
	_, err := (Runner{}).Run(context.Background(), Document{Version: 1, Steps: []Step{{Timeout: "30ms", Script: []string{"sh", "-c", script}}}})
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("timeout err=%v elapsed=%s", err, time.Since(start))
	}
	time.Sleep(250 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("background child survived cancellation: %v", err)
	}
}

func TestScriptJSONPreservesLargeNumbers(t *testing.T) {
	doc := Document{Version: 1, Steps: []Step{{ID: "result", Script: []string{"printf", `{"n":9007199254740993}`}}, {Assert: ".steps.result.json.n == 9007199254740993"}}}
	if _, err := (Runner{}).Run(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
}
