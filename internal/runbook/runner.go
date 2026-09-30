package runbook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"text/template"
	"time"

	"github.com/itchyny/gojq"
	"github.com/kevinpita/forklab/internal/chain"
)

// Runtime is the boundary between a portable recipe and a particular chain.
// Transactions return both CheckTx and committed execution failures as results;
// transport and signing failures return errors.
type Runtime interface {
	WaitHeight(context.Context, int64) (int64, error)
	Tx(context.Context, string, []string) (Transaction, error)
	Query(context.Context, []string) (any, error)
	Store(context.Context, chain.StoreRequest) (any, error)
	Pause(context.Context, int64) (any, error)
	Resume(context.Context) error
	WaitResumed(context.Context) error
}

type Transaction struct {
	Hash      string `json:"hash"`
	Height    int64  `json:"height"`
	Code      uint32 `json:"code"`
	Codespace string `json:"codespace,omitempty"`
	Log       string `json:"log,omitempty"`
}

type StepResult struct {
	Index    int    `json:"index"`
	ID       string `json:"id,omitempty"`
	Action   string `json:"action"`
	Duration string `json:"duration"`
	Output   any    `json:"output,omitempty"`
	Error    string `json:"error,omitempty"`
}

type Report struct {
	Name  string       `json:"name,omitempty"`
	Steps []StepResult `json:"steps"`
	Error string       `json:"error,omitempty"`
}

type Runner struct {
	Runtime Runtime
	// Values supplies accounts, chain_id and denoms from the current lab.
	Values      map[string]any
	Dir         string
	Env         []string
	StepTimeout time.Duration
	OnStep      func(StepResult)
}

func (r Runner) Run(ctx context.Context, d Document) (Report, error) {
	report := Report{Name: d.Name, Steps: []StepResult{}}
	if err := d.Validate(); err != nil {
		return report, err
	}
	values := map[string]any{}
	for k, v := range r.Values {
		values[k] = v
	}
	values["vars"], values["steps"] = d.Vars, map[string]any{}
	timeout := r.StepTimeout
	if timeout <= 0 {
		timeout = time.Minute
	}
	for i, s := range d.Steps {
		duration := timeout
		if s.Timeout != "" {
			duration, _ = time.ParseDuration(s.Timeout)
		}
		var stepCtx context.Context
		var cancel context.CancelFunc
		if s.Hold && s.Timeout == "" {
			stepCtx, cancel = context.WithCancel(ctx)
		} else {
			stepCtx, cancel = context.WithTimeout(ctx, duration)
		}
		started := time.Now()
		result := StepResult{Index: i + 1, ID: s.ID}
		if err := stepCtx.Err(); err != nil {
			result.Action, result.Error = "cancelled", err.Error()
		}
		if result.Error == "" && s.AtHeight > 0 {
			_, err := r.Runtime.WaitHeight(stepCtx, s.AtHeight)
			if err != nil {
				result.Action, result.Error = "at_height", err.Error()
			}
		}
		var err error
		if result.Error == "" {
			result.Action, result.Output, err = r.execute(stepCtx, s, values)
			if err != nil {
				result.Error = err.Error()
			}
		}
		cancel()
		result.Duration = time.Since(started).String()
		report.Steps = append(report.Steps, result)
		if s.ID != "" {
			values["steps"].(map[string]any)[s.ID] = result.Output
		}
		if r.OnStep != nil {
			r.OnStep(result)
		}
		if result.Error != "" {
			report.Error = fmt.Sprintf("step %d (%s): %s", i+1, result.Action, result.Error)
			return report, fmt.Errorf("%s", report.Error)
		}
	}
	return report, nil
}

func render(s string, values map[string]any) (string, error) {
	t, err := template.New("arg").Option("missingkey=error").Parse(s)
	if err != nil {
		return "", err
	}
	var out bytes.Buffer
	if err := t.Execute(&out, values); err != nil {
		return "", err
	}
	return out.String(), nil
}

func renderArgs(args []string, values map[string]any) ([]string, error) {
	out := make([]string, len(args))
	for i, a := range args {
		v, err := render(a, values)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

func asValue(v any) (any, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	err = dec.Decode(&out)
	return out, err
}

func (r Runner) execute(ctx context.Context, s Step, v map[string]any) (string, any, error) {
	switch {
	case s.Tx != nil:
		args, err := renderArgs(s.Tx.Args, v)
		if err != nil {
			return "tx", nil, err
		}
		from, err := render(s.Tx.From, v)
		if err != nil {
			return "tx", nil, err
		}
		tx, err := r.Runtime.Tx(ctx, from, args)
		value, encodeErr := asValue(tx)
		if err != nil {
			return "tx", value, err
		}
		if encodeErr != nil {
			return "tx", nil, encodeErr
		}
		if tx.Code != s.Tx.ExpectCode {
			return "tx", value, fmt.Errorf("tx %s: got code %d, expected %d: %s", tx.Hash, tx.Code, s.Tx.ExpectCode, tx.Log)
		}
		return "tx", value, nil
	case s.Query != nil:
		args, err := renderArgs(s.Query, v)
		if err != nil {
			return "query", nil, err
		}
		out, err := r.Runtime.Query(ctx, args)
		return "query", out, err
	case s.Assert != "":
		q, err := gojq.Parse(s.Assert)
		if err != nil {
			return "assert", nil, err
		}
		code, err := gojq.Compile(q)
		if err != nil {
			return "assert", nil, err
		}
		input, err := asValue(v)
		if err != nil {
			return "assert", nil, err
		}
		it := code.RunWithContext(ctx, input)
		out, ok := it.Next()
		if e, yes := out.(error); yes {
			return "assert", nil, e
		}
		if !ok || out != true {
			return "assert", out, fmt.Errorf("assertion must produce exactly true: %s", s.Assert)
		}
		if _, ok := it.Next(); ok {
			return "assert", nil, fmt.Errorf("assertion produced multiple values: %s", s.Assert)
		}
		return "assert", true, nil
	case s.WaitHeight != nil:
		h, err := r.Runtime.WaitHeight(ctx, *s.WaitHeight)
		return "wait_height", h, err
	case s.Pause != nil:
		out, err := r.Runtime.Pause(ctx, *s.Pause)
		return "pause", out, err
	case s.Hold:
		return "hold", nil, r.Runtime.WaitResumed(ctx)
	case s.Resume:
		return "resume", nil, r.Runtime.Resume(ctx)
	case s.Store != nil:
		request := *s.Store
		var err error
		request.Name, err = render(request.Name, v)
		if err != nil {
			return "store", nil, err
		}
		request.KeyHex, err = render(request.KeyHex, v)
		if err != nil {
			return "store", nil, err
		}
		out, err := r.Runtime.Store(ctx, request)
		if err != nil {
			return "store", nil, err
		}
		out, err = asValue(out)
		return "store", out, err
	case s.Script != nil:
		args, err := renderArgs(s.Script, v)
		if err != nil {
			return "script", nil, err
		}
		input, err := json.Marshal(v)
		if err != nil {
			return "script", nil, err
		}
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		cmd.Dir, cmd.Env, cmd.Stdin = r.Dir, r.Env, bytes.NewReader(input)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error {
			err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			if err == syscall.ESRCH {
				return os.ErrProcessDone
			}
			return err
		}
		defer func() {
			if cmd.Process != nil {
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
		}()
		cmd.WaitDelay = time.Second
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err = cmd.Run()
		result := map[string]any{"stdout": stdout.String(), "stderr": stderr.String(), "exit_code": 0}
		if err != nil {
			if ctx.Err() != nil {
				err = ctx.Err()
			}
			result["exit_code"] = -1
			if exit, ok := err.(*exec.ExitError); ok {
				result["exit_code"] = exit.ExitCode()
			}
			return "script", result, fmt.Errorf("script failed: %w: %s", err, stderr.String())
		}
		var decoded any
		dec := json.NewDecoder(&stdout)
		dec.UseNumber()
		if dec.Decode(&decoded) == nil && dec.Decode(new(any)) == io.EOF {
			result["json"] = decoded
		}
		return "script", result, nil
	}
	return "", nil, fmt.Errorf("step has no action")
}

func (r Report) WriteHuman(w io.Writer) error {
	for _, s := range r.Steps {
		status := "ok"
		if s.Error != "" {
			status = s.Error
		}
		if _, err := fmt.Fprintf(w, "%s %s %s: %s (%s)\n", strconv.Itoa(s.Index), s.ID, s.Action, status, s.Duration); err != nil {
			return err
		}
	}
	return nil
}
