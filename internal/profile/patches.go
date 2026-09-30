package profile

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Profile validates the document and resolves its optional patch file.
// Relative paths use the working directory; Store and LoadFile instead use
// the profile's directory. The error is Errors listing invalid fields.
func (d Document) Profile() (Profile, error) { return profileAt(d, ".") }

// LoadFile resolves relative patch files beside the profile being validated.
func LoadFile(path string) (Document, Profile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Document{}, Profile{}, err
	}
	d, err := Decode(data)
	if err != nil {
		return d, Profile{}, err
	}
	p, err := profileAt(d, filepath.Dir(path))
	return d, p, err
}

// profileAt validates the original document before resolving the optional
// patch file. File filters run first, then inline profile filters.
func profileAt(d Document, dir string) (Profile, error) {
	p, err := d.validate()
	if err != nil || d.PatchesFile == "" {
		return p, err
	}
	var data []byte
	if name, ok := strings.CutPrefix(d.PatchesFile, "builtin:"); ok {
		data, err = builtinFS.ReadFile("builtin/patches/" + name)
	} else {
		path := d.PatchesFile
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, path)
		}
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return Profile{}, Errors{{Path: "patches_file", Message: fmt.Sprintf("%s: %v", d.PatchesFile, err)}}
	}
	var file struct {
		FreshPatches []string `yaml:"fresh_patches"`
		ForkPatches  []string `yaml:"fork_patches"`
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&file); err != nil {
		return Profile{}, Errors{{Path: "patches_file", Message: fmt.Sprintf("%s: %v", d.PatchesFile, err)}}
	}
	if err := dec.Decode(new(yaml.Node)); !errors.Is(err, io.EOF) {
		return Profile{}, Errors{{Path: "patches_file", Message: d.PatchesFile + ": must be a single YAML document"}}
	}
	d.FreshPatches = append(file.FreshPatches, d.FreshPatches...)
	d.ForkPatches = append(file.ForkPatches, d.ForkPatches...)
	p, err = d.validate()
	if err != nil {
		return Profile{}, Errors{{Path: "patches_file", Message: fmt.Sprintf("%s: %v", d.PatchesFile, err)}}
	}
	return p, nil
}
