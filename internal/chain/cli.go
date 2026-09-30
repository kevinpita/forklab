package chain

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
)

// CLI runs the chain binary as a client of one lab node: queries against
// Node, transactions signed from the lab's test keyring.
type CLI struct {
	Bin string
	// Home is a node home, used for the client config the binary reads.
	Home string
	// Node is the RPC address in the form the binary takes, tcp://host:port.
	Node       string
	ChainID    string
	KeyringDir string
	GasPrices  string
}

// CommandError is a chain binary run that exited non-zero.
type CommandError struct {
	Args     []string
	ExitCode int
	Stderr   string
}

func (e *CommandError) Error() string {
	cmd := e.Args
	if i := slices.IndexFunc(cmd, func(a string) bool { return strings.HasPrefix(a, "-") }); i >= 0 {
		cmd = cmd[:i]
	}
	return fmt.Sprintf("%s: exit %d: %s", strings.Join(cmd, " "), e.ExitCode, errorLine(e.Stderr))
}

// errorLine picks the error out of a failed run's stderr, which may also hold
// the usage text: some binaries print "Error: ..." before it, others print
// the bare error as the last line.
func errorLine(stderr string) string {
	var last string
	for line := range strings.Lines(stderr) {
		line = strings.TrimSpace(line)
		if msg, ok := strings.CutPrefix(line, "Error: "); ok {
			return msg
		}
		if line != "" {
			last = line
		}
	}
	return last
}

func (c CLI) run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, c.Bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if exit := (*exec.ExitError)(nil); errors.As(err, &exit) {
		return nil, &CommandError{Args: args, ExitCode: exit.ExitCode(), Stderr: stderr.String()}
	}
	if err != nil {
		return nil, err
	}
	return stdout.Bytes(), nil
}

// query runs `<bin> q <args>` against Node and returns its JSON output.
func (c CLI) query(ctx context.Context, args ...string) ([]byte, error) {
	return c.run(ctx, append(append([]string{"q"}, args...), "--output", "json", "--node", c.Node, "--home", c.Home)...)
}

// TxFlags are the flags every lab transaction takes after `tx <module> <msg>`.
func (c CLI) TxFlags(from string) []string {
	return []string{
		"--from", from, "--chain-id", c.ChainID, "--node", c.Node, "--home", c.Home,
		"--keyring-backend", "test", "--keyring-dir", c.KeyringDir,
		"--gas", "auto", "--gas-adjustment", "1.5", "--gas-prices", c.GasPrices,
		"--yes", "--output", "json",
	}
}

// TxError is a transaction the node rejected, in CheckTx or in the block.
type TxError struct {
	Hash      string
	Code      uint32
	Codespace string
	Log       string
}

func (e *TxError) Error() string {
	return fmt.Sprintf("tx %s failed with code %d (%s): %s", e.Hash, e.Code, e.Codespace, e.Log)
}

// Broadcast signs args (such as bank send a b 1stake) from the key named
// from, broadcasts it, and returns the hash once the node accepted it into
// its mempool. Use Client.WaitTx for inclusion.
func (c CLI) Broadcast(ctx context.Context, from string, args ...string) (string, error) {
	out, err := c.run(ctx, append(append([]string{"tx"}, args...), c.TxFlags(from)...)...)
	if err != nil {
		return "", err
	}
	return parseBroadcast(out)
}

func parseBroadcast(data []byte) (string, error) {
	var r struct {
		TxHash    string `json:"txhash"`
		Code      uint32 `json:"code"`
		Codespace string `json:"codespace"`
		RawLog    string `json:"raw_log"`
	}
	if err := json.Unmarshal(data, &r); err != nil || r.TxHash == "" {
		return "", fmt.Errorf("unreadable broadcast output: %s", strings.TrimSpace(string(data)))
	}
	if r.Code != 0 {
		return "", &TxError{Hash: r.TxHash, Code: r.Code, Codespace: r.Codespace, Log: r.RawLog}
	}
	return r.TxHash, nil
}

// ExecArgs adds the flags that point a user's binary invocation at this lab
// node and keyring. Which flags apply depends on the command; a flag the
// user already passed is left alone.
func (c CLI) ExecArgs(args []string) []string {
	want := []string{"--home", c.Home}
	if ExecNeedsNode(args) {
		want = append(want, "--node", c.Node)
	}
	switch cmds := commands(args); {
	case len(cmds) > 1 && cmds[0] == "tx":
		want = append(want, "--chain-id", c.ChainID, "--keyring-backend", "test", "--keyring-dir", c.KeyringDir)
	case len(cmds) > 0 && cmds[0] == "keys":
		want = append(want, "--keyring-backend", "test", "--keyring-dir", c.KeyringDir)
	}
	out := slices.Clone(args)
	for i := 0; i < len(want); i += 2 {
		if !hasFlag(args, want[i]) {
			out = append(out, want[i], want[i+1])
		}
	}
	return out
}

// ExecNeedsNode reports whether args run a command that talks to a node:
// status, or a query or tx subcommand. A bare q or tx only prints help.
func ExecNeedsNode(args []string) bool {
	cmds := commands(args)
	if len(cmds) == 0 {
		return false
	}
	switch cmds[0] {
	case "status":
		return true
	case "q", "query", "tx":
		return len(cmds) > 1
	}
	return false
}

// commands are the positional args before any --.
func commands(args []string) []string {
	var out []string
	for _, a := range args {
		if a == "--" {
			break
		}
		if !strings.HasPrefix(a, "-") {
			out = append(out, a)
		}
	}
	return out
}

func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == name || strings.HasPrefix(a, name+"=") {
			return true
		}
	}
	return false
}
