package supervisor_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kevinpita/forklab/internal/supervisor"
)

func TestTailOffsetShowsTheLastLines(t *testing.T) {
	// Long enough to span several 64 KiB read chunks.
	var lines []string
	for i := range 20000 {
		lines = append(lines, strings.Repeat("x", i%13)+"|"+string(rune('a'+i%26)))
	}
	tests := []struct {
		name, content string
		n             int
		want          []string
	}{
		{"last two", "a\nb\nc\n", 2, []string{"b", "c"}},
		{"more than there are", "a\nb\n", 5, []string{"a", "b"}},
		{"unterminated last line counts", "a\nb\nc", 2, []string{"b", "c"}},
		{"zero means all", "a\nb\n", 0, []string{"a", "b"}},
		{"across chunks", strings.Join(lines, "\n") + "\n", 3, lines[len(lines)-3:]},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "node.log")
			if err := os.WriteFile(path, []byte(tt.content), 0o644); err != nil {
				t.Fatal(err)
			}
			off, err := supervisor.TailOffset(path, tt.n)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			if err := supervisor.Tail(path, off, closed(), func(l string) { got = append(got, l) }); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func closed() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}
