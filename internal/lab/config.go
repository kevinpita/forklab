package lab

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// nodeSettings is what configure writes into one node's config files.
type nodeSettings struct {
	ports []Port
	// peers are the other nodes as id@host:port.
	peers     []string
	blockTime time.Duration
	feeDenom  string
}

// configure edits config.toml and app.toml in home and returns the ports it
// set. Optional ports the chain's config lacks, and listeners the chain
// ships disabled, are left out.
func configure(home string, s nodeSettings) ([]Port, error) {
	docs := map[string]*tomlDoc{}
	for _, file := range []string{"config.toml", "app.toml"} {
		data, err := os.ReadFile(filepath.Join(home, "config", file))
		if err != nil {
			return nil, err
		}
		docs[file] = parseTOML(data)
	}
	var set []Port
	for _, p := range s.ports {
		doc, ok := docs[p.File]
		if !ok {
			return nil, fmt.Errorf("%s: unknown config file", p.File)
		}
		if _, _, found := doc.find(p.Key); !found && p.optional {
			continue
		}
		applied, err := doc.setPort(p.Key, p.Port)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p.File, err)
		}
		if applied {
			set = append(set, p)
		}
	}
	cfg, app := docs["config.toml"], docs["app.toml"]
	edits := []struct {
		file string
		err  error
	}{
		{"config.toml", cfg.setString("p2p.persistent_peers", strings.Join(s.peers, ","))},
		{"config.toml", cfg.set("p2p.allow_duplicate_ip", "true")},
		{"config.toml", cfg.set("p2p.addr_book_strict", "false")},
		{"config.toml", cfg.setString("consensus.timeout_commit", s.blockTime.String())},
		{"app.toml", app.set("api.enable", "true")},
		{"app.toml", app.setString("minimum-gas-prices", "0"+s.feeDenom)},
	}
	for _, e := range edits {
		if e.err != nil {
			return nil, fmt.Errorf("%s: %w", e.file, e.err)
		}
	}
	for file, doc := range docs {
		if err := os.WriteFile(filepath.Join(home, "config", file), doc.bytes(), 0o644); err != nil {
			return nil, err
		}
	}
	return set, nil
}

// nodeID derives the CometBFT node ID from config/node_key.json: the hex of
// the first 20 bytes of sha256 over the ed25519 public key.
func nodeID(home string) (string, error) {
	path := filepath.Join(home, "config", "node_key.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var key struct {
		PrivKey struct {
			Type  string `json:"type"`
			Value string `json:"value"`
		} `json:"priv_key"`
	}
	if err := json.Unmarshal(data, &key); err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	priv, err := base64.StdEncoding.DecodeString(key.PrivKey.Value)
	if err != nil || len(priv) != ed25519.PrivateKeySize {
		return "", fmt.Errorf("%s: %s is not an ed25519 private key", path, key.PrivKey.Type)
	}
	sum := sha256.Sum256(ed25519.PrivateKey(priv).Public().(ed25519.PublicKey))
	return hex.EncodeToString(sum[:20]), nil
}

// ConsensusKey is a node's CometBFT validator key, from
// config/priv_validator_key.json. PubKey is base64, as CometBFT reports it.
type ConsensusKey struct {
	Address string
	PubKey  string
}

func ReadConsensusKey(home string) (ConsensusKey, error) {
	path := filepath.Join(home, "config", "priv_validator_key.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return ConsensusKey{}, err
	}
	var key struct {
		Address string `json:"address"`
		PubKey  struct {
			Value string `json:"value"`
		} `json:"pub_key"`
	}
	if err := json.Unmarshal(data, &key); err != nil {
		return ConsensusKey{}, fmt.Errorf("%s: %w", path, err)
	}
	if key.Address == "" || key.PubKey.Value == "" {
		return ConsensusKey{}, fmt.Errorf("%s: no address or pub_key", path)
	}
	return ConsensusKey{Address: strings.ToUpper(key.Address), PubKey: key.PubKey.Value}, nil
}
