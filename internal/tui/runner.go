package tui

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/kevinpita/forklab/internal/progress"
)

// Command is one forklab invocation: the argv after the program name,
// without --json, which argv adds.
type Command []string

// argv puts --json before any "--", since exec hands what follows it to the
// chain binary.
func (c Command) argv() []string {
	i := slices.Index(c, "--")
	if i < 0 {
		i = len(c)
	}
	return slices.Concat(c[:i], []string{"--json"}, c[i:])
}

// String is the command as a user would type it.
func (c Command) String() string {
	parts := []string{"forklab"}
	for _, a := range c.argv() {
		parts = append(parts, shellQuote(a))
	}
	return strings.Join(parts, " ")
}

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_./:=@%+,-]+$`)

func shellQuote(s string) string {
	if shellSafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Runner executes forklab as a subprocess. The TUI has no other way to read
// or change anything, so every screen is reproducible from a shell.
type Runner struct {
	Exe string
	// Prefix goes before the command's argv; tests use it to re-enter the
	// test binary as a fake forklab.
	Prefix []string
	// Env is the child's environment; nil inherits the TUI's.
	Env []string
}

// CLIError is the error body of a forklab JSON envelope.
type CLIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *CLIError) Error() string { return e.Message }

func isLabNotRunning(err error) bool {
	var ce *CLIError
	return errors.As(err, &ce) && ce.Code == "lab_not_running"
}

// Result is a finished command: the envelope's data, or why it failed.
// A failed exec still carries its data (the child's output).
type Result struct {
	Cmd  Command
	Data json.RawMessage
	Err  error
	Took time.Duration
}

func (r Runner) command(ctx context.Context, c Command) *exec.Cmd {
	x := exec.CommandContext(ctx, r.Exe, slices.Concat(r.Prefix, c.argv())...)
	x.Env = r.Env
	x.WaitDelay = 2 * time.Second
	return x
}

// Run executes c and decodes its envelope.
func (r Runner) Run(ctx context.Context, c Command) Result {
	start := time.Now()
	var stdout, stderr bytes.Buffer
	x := r.command(ctx, c)
	x.Stdout, x.Stderr = &stdout, &stderr
	runErr := x.Run()
	res := Result{Cmd: c, Took: time.Since(start)}
	data, ok, err := decodeEnvelope(stdout.Bytes())
	if !ok {
		res.Err = failure(runErr, stderr.Bytes())
		return res
	}
	res.Data, res.Err = data, err
	return res
}

func labCreate(c Command) bool { return len(c) > 1 && c[0] == "lab" && c[1] == "create" }

func (r Runner) RunWithProgress(ctx context.Context, c Command, report progress.Reporter) Result {
	supportsProgress := labCreate(c) || (len(c) > 1 && c[0] == "upgrade" && c[1] == "schedule")
	if !supportsProgress {
		return r.Run(ctx, c)
	}
	start := time.Now()
	x := r.command(ctx, c)
	x.Args = append(x.Args, "--progress=json")
	var stdout bytes.Buffer
	stderr := &progressWriter{report: report}
	x.Stdout, x.Stderr = &stdout, stderr
	runErr := x.Run()
	stderr.finish()
	res := Result{Cmd: c, Took: time.Since(start)}
	data, ok, err := decodeEnvelope(stdout.Bytes())
	if !ok {
		res.Err = failure(runErr, stderr.diagnostics)
		return res
	}
	res.Data, res.Err = data, err
	return res
}

const progressLineLimit = 8 * 1024

type progressWriter struct {
	report      progress.Reporter
	line        []byte
	oversized   bool
	diagnostics []byte
}

func (w *progressWriter) Write(p []byte) (int, error) {
	n := len(p)
	for _, b := range p {
		if b == '\n' {
			w.finish()
			continue
		}
		if len(w.line) < progressLineLimit {
			w.line = append(w.line, b)
		} else {
			w.oversized = true
		}
	}
	return n, nil
}

func (w *progressWriter) finish() {
	if len(w.line) == 0 && !w.oversized {
		return
	}
	if e, ok := progress.Decode(w.line); ok && !w.oversized {
		if w.report != nil {
			w.report(e)
		}
	} else if !progressRecord(w.line) {
		w.diagnostics = append(w.diagnostics, w.line...)
		w.diagnostics = append(w.diagnostics, '\n')
		if len(w.diagnostics) > progressLineLimit {
			w.diagnostics = w.diagnostics[len(w.diagnostics)-progressLineLimit:]
		}
	}
	w.line = w.line[:0]
	w.oversized = false
}

type envelope struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data"`
	Error *CLIError       `json:"error"`
}

// decodeEnvelope parses one forklab --json envelope. ok is false when out
// holds no envelope at all.
func decodeEnvelope(out []byte) (data json.RawMessage, ok bool, err error) {
	var env envelope
	if json.NewDecoder(bytes.NewReader(out)).Decode(&env) != nil {
		return nil, false, nil
	}
	switch {
	case env.Error != nil:
		return env.Data, true, env.Error
	case !env.OK:
		return env.Data, true, &CLIError{Code: "error", Message: "command failed"}
	}
	return env.Data, true, nil
}

// failure explains a process that printed no envelope.
func failure(runErr error, stderr []byte) error {
	lines := strings.Split(strings.TrimSpace(string(stderr)), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	switch {
	case runErr != nil && last != "":
		return fmt.Errorf("%w: %s", runErr, last)
	case runErr != nil:
		return runErr
	case last != "":
		return errors.New(last)
	}
	return errors.New("no JSON output")
}

type streamKind int

const (
	streamStatus streamKind = iota
	streamConsensus
	streamLogs
	numStreams
)

// streamEvent is one NDJSON line of a stream, or its end. session tells a
// live stream from one the model already replaced.
type streamEvent struct {
	kind    streamKind
	session int
	line    []byte
	done    bool
	err     error
}

// Stream runs c and sends each stdout line on ch, then a done event when the
// process exits. A canceled stream sends nothing more.
func (r Runner) Stream(ctx context.Context, c Command, kind streamKind, session int, ch chan<- streamEvent) {
	send := func(ev streamEvent) bool {
		ev.kind, ev.session = kind, session
		select {
		case ch <- ev:
			return true
		case <-ctx.Done():
			return false
		}
	}
	var stderr bytes.Buffer
	x := r.command(ctx, c)
	x.Stderr = &stderr
	out, err := x.StdoutPipe()
	if err == nil {
		err = x.Start()
	}
	if err != nil {
		send(streamEvent{done: true, err: err})
		return
	}
	// ReadBytes keeps a line of any length intact where a Scanner would stop.
	br := bufio.NewReaderSize(out, 64*1024)
	for {
		line, rerr := br.ReadBytes('\n')
		if line = bytes.TrimSpace(line); len(line) > 0 && !send(streamEvent{line: line}) {
			break
		}
		if rerr != nil {
			break
		}
	}
	werr := x.Wait()
	if ctx.Err() != nil {
		return
	}
	if werr != nil {
		werr = failure(werr, stderr.Bytes())
	}
	send(streamEvent{done: true, err: werr})
}

// decodeLine parses one stream line into v. Streams report a failed poll as
// {"error":{"code","message"}} and a failed start as an error envelope; both
// decode to a *CLIError.
func decodeLine(line []byte, v any) error {
	var probe struct {
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(line, &probe); err != nil {
		return err
	}
	if len(probe.Error) > 0 {
		ce := &CLIError{}
		if err := json.Unmarshal(probe.Error, ce); err != nil {
			return err
		}
		return ce
	}
	return json.Unmarshal(line, v)
}

// splitArgs splits a typed command line on spaces, honoring single and
// double quotes.
func splitArgs(s string) ([]string, error) {
	var (
		args    []string
		cur     strings.Builder
		quote   rune
		inArg   bool
		escaped bool
	)
	for _, r := range s {
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
			inArg = true
		case r == '\\' && quote != '\'':
			escaped = true
			inArg = true
		case quote != 0 && r == quote:
			quote = 0
		case quote != 0:
			cur.WriteRune(r)
		case r == '\'' || r == '"':
			quote, inArg = r, true
		case r == ' ' || r == '\t':
			if inArg {
				args = append(args, cur.String())
				cur.Reset()
				inArg = false
			}
		default:
			cur.WriteRune(r)
			inArg = true
		}
	}
	if escaped {
		return nil, errors.New("unfinished escape")
	}
	if quote != 0 {
		return nil, errors.New("unterminated quote")
	}
	if inArg {
		args = append(args, cur.String())
	}
	return args, nil
}

var progressPrefix = regexp.MustCompile(`^\s*\{\s*"type"\s*:\s*"forklab\.progress"`)

func progressRecord(line []byte) bool {
	var probe struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(line, &probe) == nil {
		return probe.Type == "forklab.progress"
	}
	return progressPrefix.Match(line)
}
