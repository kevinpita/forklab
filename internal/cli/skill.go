package cli

import (
	"io"

	"github.com/kevinpita/forklab"
	"github.com/spf13/cobra"
)

type skillInfo struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

func (s skillInfo) WriteHuman(w io.Writer) error {
	_, err := io.WriteString(w, s.Content)
	return err
}

func newSkillCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "skill",
		Short: "Print the forklab agent skill",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.print(cmd, skillInfo{Name: "forklab", Content: forklab.Skill()})
		},
	}
}
