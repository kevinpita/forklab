package lab

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// tomlDoc edits TOML text line by line, so comments and key order survive.
// It reads [section] headers and single-line `key = value` pairs, which is
// all the CometBFT and SDK config keys forklab sets use. A key is addressed
// by its section and name joined with dots, as in p2p.laddr.
type tomlDoc struct {
	lines []string
}

func parseTOML(data []byte) *tomlDoc {
	return &tomlDoc{lines: strings.Split(string(data), "\n")}
}

func (d *tomlDoc) bytes() []byte {
	return []byte(strings.Join(d.lines, "\n"))
}

// find returns the line index of key and its raw value.
func (d *tomlDoc) find(key string) (int, string, bool) {
	section, name := "", key
	if i := strings.LastIndex(key, "."); i >= 0 {
		section, name = key[:i], key[i+1:]
	}
	current := ""
	for i, line := range d.lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			current = strings.TrimSpace(strings.Trim(trimmed, "[]"))
			continue
		}
		if current != section {
			continue
		}
		k, v, ok := strings.Cut(trimmed, "=")
		if ok && strings.TrimSpace(k) == name {
			return i, strings.TrimSpace(v), true
		}
	}
	return 0, "", false
}

// set replaces key's value with the TOML literal value. A missing key is an
// error: forklab only sets keys the chain's own config already has.
func (d *tomlDoc) set(key, value string) error {
	i, _, ok := d.find(key)
	if !ok {
		return fmt.Errorf("key %s not found", key)
	}
	name := key[strings.LastIndex(key, ".")+1:]
	indent := d.lines[i][:len(d.lines[i])-len(strings.TrimLeft(d.lines[i], " \t"))]
	d.lines[i] = indent + name + " = " + value
	return nil
}

func (d *tomlDoc) setString(key, value string) error {
	return d.set(key, strconv.Quote(value))
}

// setPort keeps the host and scheme of key's address and replaces its port.
// An empty address means the listener is disabled and stays that way.
func (d *tomlDoc) setPort(key string, port int) (bool, error) {
	_, raw, ok := d.find(key)
	if !ok {
		return false, fmt.Errorf("key %s not found", key)
	}
	addr, err := strconv.Unquote(raw)
	if err != nil {
		return false, fmt.Errorf("key %s: %s is not a string", key, raw)
	}
	if addr == "" {
		return false, nil
	}
	scheme, hostPort := "", addr
	if i := strings.Index(addr, "://"); i >= 0 {
		scheme, hostPort = addr[:i+3], addr[i+3:]
	}
	host, _, err := net.SplitHostPort(hostPort)
	if err != nil {
		return false, fmt.Errorf("key %s: %w", key, err)
	}
	return true, d.setString(key, scheme+net.JoinHostPort(host, strconv.Itoa(port)))
}
