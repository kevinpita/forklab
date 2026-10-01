package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/kevinpita/forklab/internal/progress"
)

func TestRunnerDeliversProgressBeforeFinalEnvelope(t *testing.T) {
	dir := t.TempDir()
	gate := filepath.Join(dir, "gate")
	bin := filepath.Join(dir, "forklab")
	script := fmt.Sprintf(`#!/bin/sh
case " $* " in *" --progress=json "*) ;; *) exit 9;; esac
printf '%%s\n' '{"type":"forklab.progress","version":1,"event":{"phase":"snapshot.fetch","message":"Downloading snapshot","state":"progress","done":1024,"total":4096,"unit":"bytes"}}' >&2
while [ ! -f %q ]; do sleep 0.01; done
printf '%%s\n' '{"ok":true,"data":{"name":"demo"}}'
`, gate)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	c := Command{"lab", "create", "demo"}
	events := make(chan progress.Event, 1)
	done := make(chan Result, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() { done <- (Runner{Exe: bin}).RunWithProgress(ctx, c, func(e progress.Event) { events <- e }) }()
	select {
	case e := <-events:
		if e.Done != 1024 || e.Total == nil || *e.Total != 4096 {
			t.Fatalf("counts: %+v", e)
		}
	case <-ctx.Done():
		t.Fatal("no live event before result")
	}
	select {
	case <-done:
		t.Fatal("result before gate")
	default:
	}
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	res := <-done
	if res.Err != nil || string(res.Data) != `{"name":"demo"}` {
		t.Fatalf("result: %+v", res)
	}
	if strings.Contains(c.String(), "progress") {
		t.Fatalf("transport leaked into preview: %s", c.String())
	}
}

func TestProgressDecoderKeepsDiagnosticsAndBoundsMemory(t *testing.T) {
	w := &progressWriter{}
	_, _ = w.Write([]byte(strings.Repeat("x", progressLineLimit*8) + "\n"))
	_, _ = w.Write([]byte(`{"type":"forklab.progress","version":1,"event":broken}` + "\n"))
	_, _ = w.Write([]byte("panic: useful diagnostic forklab.progress\n"))
	if len(w.diagnostics) > progressLineLimit || len(w.line) != 0 {
		t.Fatal("unbounded stderr")
	}
	if err := failure(fmt.Errorf("exit"), w.diagnostics); !strings.Contains(err.Error(), "panic: useful diagnostic forklab.progress") || strings.Contains(string(w.diagnostics), `"event":broken`) {
		t.Fatalf("diagnostics: %v", err)
	}
}

func TestProgressOwnedByActionAndForm(t *testing.T) {
	m := emptyModel(t)
	m.form = newForm(m.th, 1, labCreateSpec(m, true))
	m.form.running = Command{"lab", "create", "devnet"}
	m.form.action = 2
	m.running = []runningCmd{{id: 1, form: 1, cmd: m.form.running}, {id: 2, form: 2, cmd: m.form.running}}
	e := progress.Event{Phase: "binary.build", Message: "Building binary", State: progress.Started}
	m.applyProgress(progressMsg{id: 1, event: e})
	if m.form.progress.current.Message != "" {
		t.Fatal("old action touched newer form")
	}
	m.applyAction(actionMsg{id: 1, form: 1, res: Result{Cmd: m.form.running}})
	if m.form.running == nil || len(m.running) != 1 || m.running[0].id != 2 {
		t.Fatal("identical argv completed wrong action")
	}
	m.form.id = 2
	m.applyProgress(progressMsg{id: 2, event: e})
	if m.form.progress.current.Message != "Building binary" {
		t.Fatal("owner did not get event")
	}
	m.closeForm()
	if !strings.Contains(ansi.Strip(m.statusLine()), "Building binary") {
		t.Fatal("background lost activity")
	}
	e.State = progress.Completed
	e.Message = "Binary built"
	m.applyProgress(progressMsg{id: 2, event: e})
	m.applyProgress(progressMsg{id: 2, event: e})
	if len(m.running[0].progress.completed) != 1 {
		t.Fatal("duplicate history")
	}
	m.applyProgress(progressMsg{id: 1, event: e})
	if len(m.running[0].progress.completed) != 1 {
		t.Fatal("late progress changed newer action")
	}
}

func TestRunningProgressVisibleAtSmallSizes(t *testing.T) {
	m := emptyModel(t)
	m.form = newForm(m.th, 1, labCreateSpec(m, true))
	m.overlay = overlayForm
	m.form.running = Command{"lab", "create", "devnet", "--profile", "xrplevm", "--version", "11.1.1", "--validators", "2", "--fork", "polkachu"}
	m.form.started = time.Now()
	total := int64(4096)
	m.form.progress.current = progress.Event{Phase: "snapshot.fetch", Message: "Downloading snapshot", State: progress.Updated, Done: 1024, Total: &total, Unit: "bytes"}
	m.form.progress.completed = []progress.Event{{Phase: "binary.verify", Message: "Binary ready", State: progress.Completed}}
	for _, size := range [][2]int{{120, 30}, {59, 20}, {59, 15}, {40, 12}} {
		m.w, m.h = size[0], size[1]
		v := ansi.Strip(strings.Join(m.formView(), "\n"))
		if !strings.Contains(v, "Downloading snapshot") {
			t.Fatalf("active phase missing %v:\n%s", size, v)
		}
		for _, line := range strings.Split(v, "\n") {
			if ansi.StringWidth(line) > size[0] {
				t.Fatalf("overflow %v: %q", size, line)
			}
		}
	}
	m.w, m.h = 120, 30
	v := ansi.Strip(strings.Join(m.formView(), "\n"))
	if !strings.Contains(v, "25%") || !strings.Contains(v, "1.0 KiB / 4.0 KiB") || !strings.Contains(v, "Binary ready") {
		t.Fatalf("known counts missing:\n%s", v)
	}
	m.form.progress.current.Total = nil
	v = ansi.Strip(strings.Join(m.formView(), "\n"))
	if strings.Contains(v, "25%") || !strings.Contains(v, "1.0 KiB") {
		t.Fatalf("unknown total fabricated:\n%s", v)
	}
}

func TestProgressInboxCoalescesWithoutBlocking(t *testing.T) {
	p := newProgressInbox()
	p.send(progressMsg{id: 1, event: progress.Event{Phase: "download", State: progress.Started}})
	for i := int64(0); i < 100000; i++ {
		p.send(progressMsg{id: 1, event: progress.Event{Phase: "download", State: progress.Updated, Done: i}})
	}
	p.send(progressMsg{id: 1, event: progress.Event{Phase: "download", State: progress.Completed}})
	events := p.drain()
	if len(events) != 3 || events[1].event.Done != 99999 || events[0].event.State != progress.Started || events[2].event.State != progress.Completed {
		t.Fatalf("coalescing: %+v", events)
	}
}

func TestRunnerFailureKeepsOrdinaryStderr(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "forklab")
	script := `#!/bin/sh
printf '%s\n' '{"type":"forklab.progress","version":1,"event":{"phase":"snapshot.extract","message":"Extracting snapshot","state":"started"}}' >&2
printf '%s\n' 'snapshot archive is corrupt' >&2
exit 3
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	var events []progress.Event
	res := (Runner{Exe: bin}).RunWithProgress(context.Background(), Command{"lab", "create", "demo"}, func(e progress.Event) { events = append(events, e) })
	if res.Err == nil || !strings.Contains(res.Err.Error(), "snapshot archive is corrupt") || len(events) != 1 {
		t.Fatalf("failure: %+v events: %+v", res, events)
	}
}
