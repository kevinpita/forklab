// Package forklab provides the bundled agent skill.
package forklab

import _ "embed"

//go:embed .agents/skills/forklab/SKILL.md
var skill string

// Skill returns the agent skill bundled with this build.
func Skill() string { return skill }
