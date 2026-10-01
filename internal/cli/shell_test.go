package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestShellScripts(t *testing.T) {
	for _, sh := range []string{"bash", "zsh", "fish", "powershell"} {
		code, out, errOut := run("completion", sh)
		if code != 0 || out == "" || errOut != "" || strings.HasPrefix(out, "{") {
			t.Fatalf("%s: exit %d stdout %q stderr %q", sh, code, out, errOut)
		}
	}
	for _, args := range [][]string{{"completion"}, {"completion", "bash", "extra"}, {"shell", "init", "powershell"}, {"shell", "init", "nope"}} {
		code, out, _ := run(args...)
		if code != 2 || out != "" {
			t.Errorf("%v: exit %d stdout %q", args, code, out)
		}
	}
	for _, args := range [][]string{{"completion", "bash", "--json"}, {"shell", "init", "zsh", "--json"}} {
		code, out, stderr := run(args...)
		var envelope struct {
			OK    bool `json:"ok"`
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(out), &envelope); err != nil {
			t.Fatal(err)
		}
		if code != 2 || envelope.OK || envelope.Error.Code != "usage" || stderr != "" {
			t.Errorf("%v: exit %d stdout %q stderr %q", args, code, out, stderr)
		}
	}
}

func TestShellHelpersPreserveArguments(t *testing.T) {
	for _, sh := range []shell{bash, zsh, fish} {
		t.Run(string(sh), func(t *testing.T) {
			runtime, err := exec.LookPath(string(sh))
			if err != nil {
				t.Skipf("%s unavailable", sh)
			}
			dir := t.TempDir()
			stub := "#!/bin/sh\nfor arg do printf '<%s>\\n' \"$arg\"; done\nexit 7\n"
			if err := os.WriteFile(filepath.Join(dir, "forklab"), []byte(stub), 0o755); err != nil {
				t.Fatal(err)
			}
			prelude := ""
			if sh == zsh {
				prelude = "autoload -Uz compinit\ncompinit -d " + filepath.Join(dir, "zcompdump") + "\n"
			}
			code, script, stderr := run("shell", "init", string(sh))
			if code != 0 {
				t.Fatalf("exit %d: %s", code, stderr)
			}
			init := filepath.Join(dir, "init")
			if err := os.WriteFile(init, []byte(prelude+script), 0o600); err != nil {
				t.Fatal(err)
			}
			tests := []struct{ call, want string }{
				{`fl exec -- '' 'a b' '*' '$HOME' '; touch nope'`, "<exec>\n<-->\n<>\n<a b>\n<*>\n<$HOME>\n<; touch nope>\n"},
				{`flab 'my lab'`, "<--lab=my lab>\n<status>\n"},
				{`flab demo exec -- '' '*' 'a b'`, "<--lab=demo>\n<exec>\n<-->\n<>\n<*>\n<a b>\n"},
				{`flogs demo 0 --tail 20`, "<node>\n<logs>\n<--lab=demo>\n<0>\n<--tail>\n<20>\n"},
				{`flogs demo 1 -f '' '*'`, "<node>\n<logs>\n<--lab=demo>\n<1>\n<-f>\n<>\n<*>\n"},
			}
			for _, tt := range tests {
				cmd := exec.Command(runtime, "-c", "source "+init+"\n"+tt.call)
				cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
				out, err := cmd.CombinedOutput()
				if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 7 || string(out) != tt.want {
					t.Errorf("%s: %v stdout %q; want exit 7 stdout %q", tt.call, err, out, tt.want)
				}
			}
			for _, call := range []string{"flab", "flab ''", "flogs demo", "flogs '' 0", "flogs demo ''"} {
				cmd := exec.Command(runtime, "-c", "source "+init+"\n"+call)
				out, err := cmd.CombinedOutput()
				if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 2 || !strings.HasPrefix(string(out), "usage:") {
					t.Errorf("%s: %v stdout %q", call, err, out)
				}
			}
		})
	}
}
