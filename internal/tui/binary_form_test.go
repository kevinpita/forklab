package tui

import (
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestLabBinarySourceCommands(t *testing.T) {
	m := loadedModel(t, buildTheme("ansi", true))
	for _, kind := range []string{"profile", "path", "url", "git", "src"} {
		t.Run(kind, func(t *testing.T) {
			f := newForm(m.th, 1, labCreateSpec(m, true))
			fill(f, map[string]string{"profile": "simd", "version": "0.53.8", "bin-version": "1.2.3", "bin-kind": kind, "bin-location": "/my sources/chain", "bin-ref": "feature/test", "bin-build": "make build", "bin-out": "build/chain"})
			c := f.command()
			if kind == "profile" {
				if slices.Contains(c, "--binary") || c.String() != "forklab lab create devnet --profile simd --version 0.53.8 --validators 2 --json" {
					t.Fatalf("configured command %s", c)
				}
				return
			}
			i := slices.Index(c, "--binary")
			if i < 0 || c[i+1] != "1.2.3="+kind+":/my sources/chain" {
				t.Fatalf("custom command %s", c)
			}
			if slices.Contains(c, "--binary-ref") != (kind == "git") || slices.Contains(c, "--binary-build") != (kind == "git" || kind == "src") || slices.Contains(c, "--binary-out") != (kind == "git" || kind == "src") {
				t.Fatalf("metadata leaked or missing: %s", c)
			}
		})
	}
}

func TestWizardNavigationValidatesAndNeverWrapsReview(t *testing.T) {
	m := loadedModel(t, buildTheme("ansi", true))
	m.run = fixtureRunner()
	m.w, m.h = 120, 40
	cmd := m.openForm(labCreateSpec(m, true))
	drive(t, m, cmd, func() bool { return m.form.values()["version"] != "" })
	press(m, "tab")
	if m.form.focused().key != "bin-kind" {
		t.Fatal("Tab skipped source")
	}
	for _, want := range []string{"Use a profile version", "Use a binary file", "Download from a URL", "Build from Git", "Build local source", "↑↓ choose"} {
		if !strings.Contains(ansi.Strip(m.render()), want) {
			t.Fatalf("source chooser hides %s", want)
		}
	}
	press(m, "down")
	if m.form.values()["bin-kind"] != "path" || m.form.focused().key != "bin-kind" {
		t.Fatal("down did not choose path")
	}
	press(m, "tab")
	if m.form.focused().key != "bin-version" {
		t.Fatal("Tab did not open custom version")
	}
	press(m, "enter")
	if m.form.focused().key != "bin-version" {
		t.Fatal("empty version advanced")
	}
	fill(m.form, map[string]string{"bin-version": "1.2.3"})
	press(m, "enter")
	if m.form.focused().key != "bin-location" {
		t.Fatal("Enter skipped executable path")
	}
	fill(m.form, map[string]string{"bin-location": "/tmp/my binary"})
	press(m, "tab", "shift+tab")
	if m.form.focused().key != "bin-location" {
		t.Fatal("back skipped path")
	}
	press(m, "enter", "tab", "enter", "tab", "enter")
	if !m.form.reviewing() {
		t.Fatalf("did not reach review: step %d focus %d", m.form.step, m.form.focus)
	}
	press(m, "tab")
	if !m.form.reviewing() || m.form.running != nil {
		t.Fatal("review Tab submitted or wrapped")
	}
	press(m, "shift+tab")
	if m.form.focused().key != "name" {
		t.Fatal("review back did not reach name")
	}
	press(m, "enter")
	if !m.form.reviewing() {
		t.Fatal("Enter after back skipped review")
	}
}

func TestSourcePickerExplainsSelectionAndKeepsItVisible(t *testing.T) {
	m := loadedModel(t, buildTheme("ansi", true))
	m.w, m.h = 80, 24
	_ = m.openForm(labCreateSpec(m, true))
	fill(m.form, map[string]string{"profile": "simd", "version": "0.53.8"})
	press(m, "tab")
	initial := strings.Split(ansi.Strip(strings.Join(m.formView(), "\n")), "\n")
	press(m, "down", "down", "down", "down")
	view := ansi.Strip(strings.Join(m.formView(), "\n"))
	if strings.Count(view, "› ") != 1 || !strings.Contains(view, "› Build local source") {
		t.Fatalf("source selection is ambiguous:\n%s", view)
	}
	text := strings.Join(strings.Fields(strings.ReplaceAll(view, "│", " ")), " ")
	if !strings.Contains(text, "Build code in a source folder on this machine.") || strings.Contains(text, "Choose a version configured in this profile.") {
		t.Fatalf("source explanation does not follow selection:\n%s", view)
	}
	changed := strings.Split(view, "\n")
	for _, label := range []string{"Use a profile version", "Use a binary file", "Download from a URL", "Build from Git", "Build local source"} {
		for row, line := range initial {
			if strings.Contains(line, label) && !strings.Contains(changed[row], label) {
				t.Fatalf("choosing a source moved %q to another row", label)
			}
		}
	}
	fill(m.form, map[string]string{"bin-version": "0.53.8", "bin-location": "./source", "bin-build": "make build", "bin-out": "build/simd"})
	for _, size := range [][2]int{{59, 20}, {59, 15}, {40, 12}} {
		m.w, m.h = size[0], size[1]
		view := ansi.Strip(strings.Join(m.formView(), "\n"))
		if !strings.Contains(view, "› Build local source") {
			t.Fatalf("cropped source selection is hidden at %v:\n%s", size, view)
		}
	}
}
