package binary

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/kevinpita/forklab/internal/profile"
	"github.com/kevinpita/forklab/internal/progress"
)

// buildGit shallow-fetches the ref into the scratch clone work,
// builds there, and copies the output to dst. Fetching by ref instead of
// clone --branch also accepts commit hashes on hosts that allow it.
func buildGit(ctx context.Context, s profile.GitSource, v profile.Vars, work, dst string, log io.Writer, report progress.Reporter) error {
	if err := os.Mkdir(work, 0o755); err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(work) }()
	report.Emit("binary.git", "Fetching Git source", progress.Started)
	env := map[string]string{"GIT_TERMINAL_PROMPT": "0"}
	for _, args := range [][]string{
		{"init", "-q"},
		{"fetch", "-q", "--depth", "1", s.Repo.Expand(v), s.Ref.Expand(v)},
		{"checkout", "-q", "FETCH_HEAD"},
	} {
		if err := run(ctx, work, env, log, "git", args...); err != nil {
			return err
		}
	}
	report.Emit("binary.git", "Git source fetched", progress.Completed)
	report.Emit("binary.build", "Building binary", progress.Started)
	err := build(ctx, work, s.Build, s.Env, s.Out.Expand(v), dst, log)
	if err == nil {
		report.Emit("binary.build", "Binary built", progress.Completed)
	}
	return err
}

func (c Cache) buildSrc(ctx context.Context, s profile.SrcSource, v profile.Vars, dst string, log io.Writer) error {
	dir, err := filepath.Abs(s.Dir.Expand(v))
	if err != nil {
		return err
	}
	physical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	unlock, err := lock(ctx, filepath.Join(c.Dir, fmt.Sprintf(".checkout-%x.lock", sha256.Sum256([]byte(physical)))))
	if err != nil {
		return err
	}
	defer unlock()
	return build(ctx, dir, s.Build, s.Env, s.Out.Expand(v), dst, log)
}

func build(ctx context.Context, dir, script string, env map[string]string, out, dst string, log io.Writer) error {
	if err := run(ctx, dir, env, log, "sh", "-c", script); err != nil {
		return err
	}
	if !filepath.IsAbs(out) {
		out = filepath.Join(dir, out)
	}
	if err := isExecutable(out); err != nil {
		return fmt.Errorf("build output: %w", err)
	}
	src, err := os.Open(out)
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()
	return writeFile(dst, src)
}

// run executes name in dir with env merged over the process environment.
// Output goes to log, and its tail is kept for the error.
func run(ctx context.Context, dir string, env map[string]string, log io.Writer, name string, args ...string) error {
	cmd := command(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	for k, val := range env {
		cmd.Env = append(cmd.Env, k+"="+val)
	}
	t := &tail{}
	w := io.Writer(t)
	if log != nil {
		w = io.MultiWriter(log, t)
	}
	cmd.Stdout, cmd.Stderr = w, w
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %v in %s: %w\n%s", name, args, dir, err, t.b)
	}
	return nil
}

// tail keeps the last 4 KiB written to it.
type tail struct{ b []byte }

func (t *tail) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > 4096 {
		t.b = t.b[len(t.b)-4096:]
	}
	return len(p), nil
}
