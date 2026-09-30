package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func windowSize(s [2]int) tea.WindowSizeMsg { return tea.WindowSizeMsg{Width: s[0], Height: s[1]} }

func TestCatalogKeysAreUniquePerScope(t *testing.T) {
	seen := map[scope]map[string]action{}
	for _, b := range catalog {
		if seen[b.scope] == nil {
			seen[b.scope] = map[string]action{}
		}
		for _, k := range b.keys {
			if prev, dup := seen[b.scope][k]; dup {
				t.Errorf("key %q bound twice in %s: actions %d and %d", k, scopeTitles[b.scope], prev, b.id)
			}
			seen[b.scope][k] = b.id
		}
	}
}

func TestEveryBindingDoesExactlyOneThing(t *testing.T) {
	for _, b := range catalog {
		_, hasHandler := handlers[b.id]
		if hasHandler == (b.cmd != nil) {
			t.Errorf("%q (action %d): handler %v, command %v; want exactly one", b.name, b.id, hasHandler, b.cmd != nil)
		}
		if len(b.keys) == 0 || b.name == "" {
			t.Errorf("action %d has no key or name", b.id)
		}
		if b.footer > 0 && b.hint == "" {
			t.Errorf("%q ranks in the footer without a hint", b.name)
		}
	}
}

// Every palette entry, in every panel, must be runnable: it has a handler
// or a command, and a command renders as a forklab invocation.
func TestPaletteEntriesAreRunnable(t *testing.T) {
	m := loadedModel(t, buildTheme("ansi", true))
	m.w, m.h = 120, 40
	for p := range numPanels {
		m.panel = p
		for _, b := range m.paletteEntries() {
			if b.cmd != nil {
				if c := b.cmd(m); len(c) == 0 || c.String()[:8] != "forklab " {
					t.Errorf("%s: %q builds %q", panels[p].title, b.name, c)
				}
				continue
			}
			if handlers[b.id] == nil {
				t.Errorf("%s: palette entry %q has no handler", panels[p].title, b.name)
			}
		}
	}
}

func TestFooterShowsOnlyEnabledBindings(t *testing.T) {
	m := loadedModel(t, buildTheme("ansi", true))
	hints := func() map[string]bool {
		out := map[string]bool{}
		for _, b := range m.footerBindings() {
			if !b.enabled(m) {
				t.Errorf("footer shows disabled %q", b.name)
			}
			out[b.hint] = true
		}
		return out
	}
	// node0 runs, node1 exited.
	if h := hints(); !h["stop"] || !h["kill"] || h["start"] {
		t.Errorf("running node footer = %v", h)
	}
	m.cursor[panelNodes] = 1
	if h := hints(); h["stop"] || h["kill"] || !h["start"] {
		t.Errorf("exited node footer = %v", h)
	}
	m.panel = panelLabs
	if h := hints(); !h["down"] || h["up"] || h["stop"] {
		t.Errorf("running lab footer = %v", h)
	}
	m.openOverlay(overlayConfirm)
	if h := hints(); len(h) != 2 || !h["confirm"] || !h["cancel"] {
		t.Errorf("confirm footer = %v", h)
	}
}

func TestDisabledBindingFallsThrough(t *testing.T) {
	m := loadedModel(t, buildTheme("ansi", true))
	m.panel = panelProposals
	if b, ok := m.match("s"); ok {
		t.Errorf("s matched %q outside the Nodes panel", b.name)
	}
	if b, ok := m.match("r"); ok {
		t.Errorf("r matched %q outside the Nodes panel", b.name)
	}
}
