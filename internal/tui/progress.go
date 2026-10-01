package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/kevinpita/forklab/internal/progress"
)

type (
	progressMsg struct {
		id    int
		event progress.Event
	}
	progressReadyMsg struct{}
)

type progressInbox struct {
	mu    sync.Mutex
	queue []progressMsg
	ready chan struct{}
}

func newProgressInbox() *progressInbox { return &progressInbox{ready: make(chan struct{}, 1)} }
func (p *progressInbox) send(msg progressMsg) {
	p.mu.Lock()
	n := len(p.queue)
	if n > 0 && msg.event.State == progress.Updated && p.queue[n-1].id == msg.id && p.queue[n-1].event.Phase == msg.event.Phase && p.queue[n-1].event.State == progress.Updated {
		p.queue[n-1] = msg
	} else {
		if n >= 256 {
			i := slices.IndexFunc(p.queue, func(m progressMsg) bool { return m.event.State == progress.Updated })
			if i < 0 {
				i = 0
			}
			p.queue = slices.Delete(p.queue, i, i+1)
		}
		p.queue = append(p.queue, msg)
	}
	p.mu.Unlock()
	select {
	case p.ready <- struct{}{}:
	default:
	}
}

func (p *progressInbox) drain() []progressMsg {
	p.mu.Lock()
	defer p.mu.Unlock()
	q := p.queue
	p.queue = nil
	return q
}

func (p *progressInbox) wait(ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		select {
		case <-p.ready:
			return progressReadyMsg{}
		case <-ctx.Done():
			return nil
		}
	}
}

type actionProgress struct {
	current   progress.Event
	since     time.Time
	completed []progress.Event
}

func (p *actionProgress) apply(e progress.Event) {
	if e.State == progress.Completed {
		if slices.ContainsFunc(p.completed, func(old progress.Event) bool { return old.Phase == e.Phase }) {
			return
		}
		p.completed = append(p.completed, e)
		if len(p.completed) > 16 {
			p.completed = p.completed[len(p.completed)-16:]
		}
		if p.current.Phase == e.Phase {
			p.current = progress.Event{}
			p.since = time.Time{}
		}
		return
	}
	if slices.ContainsFunc(p.completed, func(old progress.Event) bool { return old.Phase == e.Phase }) {
		return
	}
	if p.current.Phase != e.Phase {
		p.since = time.Now()
	}
	p.current = e
}

func (m *Model) applyProgress(msg progressMsg) {
	for i := range m.running {
		r := &m.running[i]
		if r.id != msg.id {
			continue
		}
		r.progress.apply(msg.event)
		if f := m.form; f != nil && f.action == msg.id && f.id == r.form {
			f.progress = r.progress
		}
		return
	}
}

func (m *Model) drainProgress() {
	for _, msg := range m.progress.drain() {
		m.applyProgress(msg)
	}
}

func progressBytes(n int64) string {
	for _, u := range []struct {
		n int64
		s string
	}{{1 << 30, "GiB"}, {1 << 20, "MiB"}, {1 << 10, "KiB"}} {
		if n >= u.n {
			return fmt.Sprintf("%.1f %s", float64(n)/float64(u.n), u.s)
		}
	}
	return fmt.Sprintf("%d B", n)
}

func (m *Model) progressLines(inner, room int) []string {
	f, th := m.form, m.th
	p := f.progress
	width := max(inner-2, 1)
	e := p.current
	message := e.Message
	if message == "" {
		message = "Running " + strings.ToLower(f.spec.title)
		if len(p.completed) > 0 {
			message = "Continuing " + strings.ToLower(f.spec.title)
		}
	}
	lines := []string{" " + th.Val.Render(spinFrame(m.spin)) + " " + th.Title.Render(ansi.Truncate(message, max(width-2, 1), "…"))}
	if e.Total == nil || e.Unit != "bytes" {
		barW := max(min(width-2, 48), 1)
		pulseW := min(6, barW)
		position := m.spin % max(2*(barW-pulseW), 1)
		if position > barW-pulseW {
			position = 2*(barW-pulseW) - position
		}
		lines = append(lines, "   "+th.Dim.Render(strings.Repeat("─", position))+th.Val.Render(strings.Repeat("━", pulseW))+th.Dim.Render(strings.Repeat("─", barW-position-pulseW)))
	}
	if e.Unit == "bytes" {
		text := progressBytes(e.Done)
		if e.Total != nil {
			percent := min(float64(e.Done)/float64(*e.Total), 1.0)
			text = fmt.Sprintf("%.0f%% · %s / %s", percent*100, progressBytes(e.Done), progressBytes(*e.Total))
			barW := max(min(width-2, 40), 1)
			filled := int(percent * float64(barW))
			lines = append(lines, "   "+th.Val.Render(strings.Repeat("━", filled))+th.Dim.Render(strings.Repeat("─", barW-filled)))
		}
		lines = append(lines, "   "+th.Text.Render(ansi.Truncate(text, max(width-2, 1), "…")))
	} else if e.Unit == "nodes" && e.Total != nil {
		lines = append(lines, "   "+th.Dim.Render(fmt.Sprintf("%d / %d validators", e.Done, *e.Total)))
	}
	if e.Detail != "" {
		lines = append(lines, "   "+th.Dim.Render(ansi.Truncate(oneLine(e.Detail), max(width-2, 1), "…")))
	}
	elapsed := "Elapsed " + fmtDur(time.Since(f.started).Round(time.Second))
	if !p.since.IsZero() {
		elapsed += " · this step " + fmtDur(time.Since(p.since).Round(time.Second))
	}
	lines = append(lines, " "+th.Dim.Render(ansi.Truncate(elapsed, width, "…")))
	if len(lines) > room {
		lines = lines[:max(room, 1)]
	}
	available := room - len(lines)
	history := p.completed[max(0, len(p.completed)-min(available, 3)):]
	for _, done := range history {
		lines = append(lines, " "+th.Good.Render("✓ ")+th.Dim.Render(ansi.Truncate(done.Message, max(width-2, 1), "…")))
	}
	return lines
}
