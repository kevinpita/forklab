// Package output is the CLI's stable contract: the --json envelope, human
// rendering, and the mapping from errors to process exit codes.
package output

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Code is a process exit code. Every command exits with one of these.
type Code int

const (
	CodeOK            Code = 0
	CodeError         Code = 1
	CodeUsage         Code = 2
	CodeLabNotRunning Code = 3
)

func (c Code) String() string {
	switch c {
	case CodeOK:
		return "ok"
	case CodeUsage:
		return "usage"
	case CodeLabNotRunning:
		return "lab_not_running"
	default:
		return "error"
	}
}

// MarshalText makes a Code serialize as its stable name in the JSON envelope.
func (c Code) MarshalText() ([]byte, error) {
	return []byte(c.String()), nil
}

// ErrLabNotRunning marks any error, however wrapped, as exit code 3.
var ErrLabNotRunning = errors.New("lab not running")

type usageError struct{ err error }

func (e usageError) Error() string { return e.err.Error() }
func (e usageError) Unwrap() error { return e.err }

// Usagef returns an error that exits with CodeUsage.
func Usagef(format string, args ...any) error {
	return usageError{fmt.Errorf(format, args...)}
}

// Usage marks err as a usage error. A nil err stays nil.
func Usage(err error) error {
	if err == nil {
		return nil
	}
	return usageError{err}
}

// ExitStatus is an exit code passed through from a child process, as exec
// does with the chain binary's. The child already reported its failure, so
// nothing more is printed.
type ExitStatus int

func (e ExitStatus) Error() string { return fmt.Sprintf("exit status %d", int(e)) }

// ExitCode is the single place that maps an error to an exit code.
func ExitCode(err error) Code {
	switch {
	case err == nil:
		return CodeOK
	case errors.Is(err, ErrLabNotRunning):
		return CodeLabNotRunning
	case errors.As(err, new(usageError)):
		return CodeUsage
	default:
		return CodeError
	}
}

// Result is what a command produces. Under --json it is marshaled as the
// envelope's data; otherwise WriteHuman renders it.
type Result interface {
	WriteHuman(w io.Writer) error
}

type envelope struct {
	OK    bool       `json:"ok"`
	Data  any        `json:"data,omitempty"`
	Error *errorBody `json:"error,omitempty"`
}

type errorBody struct {
	Code    Code   `json:"code"`
	Message string `json:"message"`
}

// Print writes a successful result to w. A nil result is a command with
// nothing to report: human output is empty and JSON data is {}.
func Print(w io.Writer, asJSON bool, r Result) error {
	if asJSON {
		var data any = struct{}{}
		if r != nil {
			data = r
		}
		return writeJSON(w, envelope{OK: true, Data: data})
	}
	if r == nil {
		return nil
	}
	return r.WriteHuman(w)
}

// PrintFailedJSON writes r as the data of a JSON envelope with ok false, for
// a command whose result describes its own failure, such as a child process
// that exited non-zero.
func PrintFailedJSON(w io.Writer, r Result) error {
	return writeJSON(w, envelope{OK: false, Data: r})
}

// PrintError reports err and returns its exit code. JSON goes to stdout so a
// caller parses one stream; human text goes to stderr.
func PrintError(stdout, stderr io.Writer, asJSON bool, err error) Code {
	code := ExitCode(err)
	if asJSON {
		_ = writeJSON(stdout, envelope{Error: &errorBody{Code: code, Message: err.Error()}})
		return code
	}
	_, _ = fmt.Fprintf(stderr, "Error: %v\n", err)
	if code == CodeUsage {
		_, _ = fmt.Fprintln(stderr, "Run with --help for usage.")
	}
	return code
}

// WriteStreamError writes err as one NDJSON line of a streaming command,
// {"error":{"code","message"}}, typed the same way as an envelope's error.
func WriteStreamError(w io.Writer, err error) error {
	return json.NewEncoder(w).Encode(struct {
		Error errorBody `json:"error"`
	}{errorBody{Code: ExitCode(err), Message: err.Error()}})
}

func writeJSON(w io.Writer, v envelope) error {
	return json.NewEncoder(w).Encode(v)
}
