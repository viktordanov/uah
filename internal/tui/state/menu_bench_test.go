package state_test

import (
	"go/build"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/viktordanov/uah/internal/tui/state"
)

// goFiles is 20,000 paths for "@", as a large workspace has: Go's own
// source files, then again under third_party/.
func goFiles(b *testing.B) []string {
	b.Helper()
	src := filepath.Join(build.Default.GOROOT, "src")
	var files []string
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(src, path)
			files = append(files, rel)
		}

		return err
	})
	if err != nil || len(files) == 0 {
		b.Skip("no Go source to list:", err)
	}
	for i := 0; len(files) < 20000; i++ {
		files = append(files, "third_party/"+files[i])
	}

	return files[:20000]
}

// BenchmarkMenuFiles is the "@" menu over 20,000 files: a typed key (the
// shell's menu check, the draft's change, and the frame after it), a menu
// key (the check, the move, and the frame), and a frame alone.
func BenchmarkMenuFiles(b *testing.B) {
	s, _ := apply(opened(), state.FilesLoaded{Paths: goFiles(b)})
	drafts := []string{"see @net/http/serv", "see @net/http/serve"}
	b.Run("type", func(b *testing.B) {
		s, _ := apply(s, state.DraftChanged{Draft: drafts[0]})
		b.ReportAllocs()
		i := 0
		for b.Loop() {
			prev, next := drafts[i%2], drafts[(i+1)%2]
			s.MenuOpen(prev)
			s, _ = state.Reduce(s, state.DraftChanged{Draft: next})
			s.Suggestions(next)
			i++
		}
	})
	s, _ = apply(s, state.DraftChanged{Draft: drafts[0]})
	b.Run("move", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			s.MenuOpen(drafts[0])
			s, _ = state.Reduce(s, state.MenuMove{Draft: drafts[0], Delta: 1})
			s.Suggestions(drafts[0])
		}
	})
	b.Run("frame", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			s.Suggestions(drafts[0])
		}
	})
}
