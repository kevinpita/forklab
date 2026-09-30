package lab

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kevinpita/forklab/internal/profile"
)

// fakeKeysAdd answers `keys add --help` and prints key JSON with a mnemonic
// for `keys add <name>`, the way stock SDK binaries do.
const fakeKeysAdd = `#!/bin/sh
case " $* " in *" --help "*) echo "--key-type"; exit 0;; esac
echo "{\"name\":\"$3\",\"address\":\"cosmos1$3\",\"mnemonic\":\"abandon secret words of $3\"}"
`

func TestAddKeysKeepsMnemonicsOutOfTheLog(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "chaind")
	if err := os.WriteFile(bin, []byte(fakeKeysAdd), 0o755); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	b := builder{
		in:      CreateInput{Validators: 1, TestAccounts: 1},
		p:       profile.Profile{KeyAlgo: "secp256k1"},
		partial: dir,
		cli:     chainCLI{ctx: context.Background(), bin: bin, log: &log},
	}
	keys, err := b.addKeys(ModeFresh)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 3 || keys[0].Mnemonic != "abandon secret words of val0" {
		t.Fatalf("keys = %+v, want val0, gov, test0 with their mnemonics", keys)
	}
	if strings.Contains(log.String(), "abandon secret") {
		t.Fatalf("create log holds a mnemonic:\n%s", log.String())
	}
	if !strings.Contains(log.String(), "keys added: val0, gov, test0") {
		t.Fatalf("create log does not record the added keys:\n%s", log.String())
	}
	fi, err := os.Stat(MnemonicsPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mnemonics.json mode %o, want 600", fi.Mode().Perm())
	}
}

func TestLockNameExcludesAndRemovesItsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".demo.lock")
	unlock, err := lockName(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockName(path); err == nil {
		t.Fatal("second lock succeeded while the first is held")
	}
	unlock()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("lock file left after unlock: %v", err)
	}
	unlock, err = lockName(path)
	if err != nil {
		t.Fatalf("lock after unlock: %v", err)
	}
	unlock()
}
