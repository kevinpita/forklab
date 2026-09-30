package profile

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

//go:embed builtin/*.yaml builtin/patches/*.yaml
var builtinFS embed.FS

// ErrNotFound means no user or built-in profile has the name.
var ErrNotFound = errors.New("profile not found")

type Origin string

const (
	OriginUser    Origin = "user"
	OriginBuiltin Origin = "builtin"
)

// Store holds user profiles as <dir>/<name>.yaml. A user profile shadows the
// built-in profile of the same name.
type Store struct {
	Dir string
}

// Entry is one resolved profile.
type Entry struct {
	Origin Origin
	// Path is the user profile file; empty for built-ins.
	Path    string
	Doc     Document
	Profile Profile
}

// Snapshot makes the profile self-contained for a lab. Later edits to or
// removal of a patch file cannot change the corrections the lab was built with.
func (e Entry) Snapshot() Document {
	d := e.Doc
	d.PatchesFile = ""
	d.FreshPatches = slices.Clone(e.Profile.FreshPatches)
	d.ForkPatches = slices.Clone(e.Profile.ForkPatches)
	return d
}

// DefaultStore is $FORKLAB_CONFIG_DIR/profiles, else
// $XDG_CONFIG_HOME/forklab/profiles, else ~/.config/forklab/profiles.
func DefaultStore() (Store, error) {
	if d := os.Getenv("FORKLAB_CONFIG_DIR"); d != "" {
		return Store{Dir: filepath.Join(d, "profiles")}, nil
	}
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return Store{Dir: filepath.Join(d, "forklab", "profiles")}, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Store{}, err
	}
	return Store{Dir: filepath.Join(home, ".config", "forklab", "profiles")}, nil
}

// Load decodes and validates profile YAML.
func Load(data []byte) (Document, Profile, error) {
	d, err := Decode(data)
	if err != nil {
		return d, Profile{}, err
	}
	p, err := profileAt(d, ".")
	return d, p, err
}

func (s Store) path(name string) string {
	return filepath.Join(s.Dir, name+".yaml")
}

func checkName(name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("%q is not a valid profile name (lowercase letters, digits, - and _)", name)
	}
	return nil
}

// Get returns the user profile named name, else the built-in one. An invalid
// user profile is an error, never a silent fallback to the built-in.
func (s Store) Get(name string) (Entry, error) {
	if err := checkName(name); err != nil {
		return Entry{}, err
	}
	path := s.path(name)
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		e := Entry{Origin: OriginUser, Path: path}
		e.Doc, err = Decode(data)
		if err == nil && e.Doc.Name != name {
			err = Errors{{Path: "name", Message: fmt.Sprintf("%q does not match file name %s", e.Doc.Name, filepath.Base(path))}}
		}
		if err == nil {
			e.Profile, err = profileAt(e.Doc, filepath.Dir(path))
		}
		if err != nil {
			return Entry{}, fmt.Errorf("profile %s (%s):\n%w", name, path, err)
		}
		return e, nil
	case !errors.Is(err, fs.ErrNotExist):
		return Entry{}, err
	}
	return builtin(name)
}

func builtin(name string) (Entry, error) {
	data, err := builtinFS.ReadFile("builtin/" + name + ".yaml")
	if err != nil {
		return Entry{}, fmt.Errorf("profile %s: %w", name, ErrNotFound)
	}
	d, p, err := Load(data)
	return Entry{Origin: OriginBuiltin, Doc: d, Profile: p}, err
}

// Listing is one row of List. Error is set when a user profile is invalid.
type Listing struct {
	Name           string `json:"name"`
	Origin         Origin `json:"origin"`
	Path           string `json:"path,omitempty"`
	ShadowsBuiltin bool   `json:"shadows_builtin,omitempty"`
	Error          string `json:"error,omitempty"`
}

// List returns every visible profile sorted by name.
func (s Store) List() ([]Listing, error) {
	builtins, err := fs.Glob(builtinFS, "builtin/*.yaml")
	if err != nil {
		return nil, err
	}
	byName := map[string]Listing{}
	for _, f := range builtins {
		name := strings.TrimSuffix(filepath.Base(f), ".yaml")
		byName[name] = Listing{Name: name, Origin: OriginBuiltin}
	}
	users, err := filepath.Glob(filepath.Join(s.Dir, "*.yaml"))
	if err != nil {
		return nil, err
	}
	for _, f := range users {
		name := strings.TrimSuffix(filepath.Base(f), ".yaml")
		_, shadows := byName[name]
		l := Listing{Name: name, Origin: OriginUser, Path: f, ShadowsBuiltin: shadows}
		if _, err := s.Get(name); err != nil {
			l.Error = err.Error()
		}
		byName[name] = l
	}
	out := make([]Listing, 0, len(byName))
	for _, l := range byName {
		out = append(out, l)
	}
	slices.SortFunc(out, func(a, b Listing) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// Save validates d and writes it as a user profile, replacing any existing one.
func (s Store) Save(d Document) (Entry, error) {
	p, err := profileAt(d, s.Dir)
	if err != nil {
		return Entry{}, err
	}
	data, err := d.Marshal()
	if err != nil {
		return Entry{}, err
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return Entry{}, err
	}
	path := s.path(d.Name)
	tmp, err := os.CreateTemp(s.Dir, "."+d.Name+"-*.yaml")
	if err != nil {
		return Entry{}, err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return Entry{}, err
	}
	if err := tmp.Close(); err != nil {
		return Entry{}, err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return Entry{}, err
	}
	return Entry{Origin: OriginUser, Path: path, Doc: d, Profile: p}, nil
}

// HasUser reports whether a user profile named name exists.
func (s Store) HasUser(name string) bool {
	_, err := os.Stat(s.path(name))
	return err == nil
}

// Delete removes the user profile named name. Built-ins cannot be deleted; a
// shadowed built-in becomes visible again.
func (s Store) Delete(name string) (path string, err error) {
	if err := checkName(name); err != nil {
		return "", err
	}
	path = s.path(name)
	err = os.Remove(path)
	if errors.Is(err, fs.ErrNotExist) {
		if IsBuiltin(name) {
			return "", fmt.Errorf("profile %s is built in and cannot be deleted", name)
		}
		return "", fmt.Errorf("profile %s: %w", name, ErrNotFound)
	}
	return path, err
}

// IsBuiltin reports whether a built-in profile named name exists.
func IsBuiltin(name string) bool {
	_, err := builtinFS.ReadFile("builtin/" + name + ".yaml")
	return err == nil
}
