//go:build unix

package patch_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/patch"
)

// TestTargets_SwappedForSymlink checks that a patch whose directory, or
// file, a command swaps for a symlink between the approval and the write
// fails without writing where the symlink leads, and puts back what it
// already wrote. Each case computes the changes, swaps, then writes, as a
// command running between the engine's check and the patch job would.
func TestTargets_SwappedForSymlink(t *testing.T) {
	cases := []struct {
		name string
		body string
		// swap changes the workspace after the approval; outside is a
		// directory no patch may write.
		swap func(t *testing.T, ws, outside string)
		// untouched are files, relative to outside, that must keep
		// their content ("" means absent).
		untouched map[string]string
	}{{
		name:      "a directory to an absolute symlink",
		body:      "*** Add File: sub/x.txt\n+evil",
		swap:      func(t *testing.T, ws, outside string) { swapDir(t, filepath.Join(ws, "sub"), outside) },
		untouched: map[string]string{"x.txt": ""},
	}, {
		name: "a directory to a relative symlink",
		body: "*** Add File: sub/x.txt\n+evil",
		swap: func(t *testing.T, ws, outside string) {
			rel, err := filepath.Rel(ws, outside)
			require.NoError(t, err)
			swapDir(t, filepath.Join(ws, "sub"), rel)
		},
		untouched: map[string]string{"x.txt": ""},
	}, {
		name: "a directory to a dangling symlink",
		body: "*** Add File: sub/deeper/x.txt\n+evil",
		swap: func(t *testing.T, ws, outside string) {
			swapDir(t, filepath.Join(ws, "sub"), filepath.Join(outside, "made"))
		},
		untouched: map[string]string{"made": ""},
	}, {
		name: "a directory to a symlink to its parent",
		body: "*** Add File: sub/x.txt\n+evil",
		swap: func(t *testing.T, ws, _ string) { swapDir(t, filepath.Join(ws, "sub"), "..") },
	}, {
		name: "a directory the patch creates",
		body: "*** Add File: new/deep/x.txt\n+evil",
		swap: func(t *testing.T, ws, outside string) {
			require.NoError(t, os.Symlink(outside, filepath.Join(ws, "new")))
		},
		untouched: map[string]string{"deep": ""},
	}, {
		name: "the updated file itself",
		body: "*** Update File: sub/a.txt\n@@\n-one\n+evil",
		swap: func(t *testing.T, ws, outside string) {
			require.NoError(t, os.Remove(filepath.Join(ws, "sub", "a.txt")))
			require.NoError(t, os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(ws, "sub", "a.txt")))
		},
		untouched: map[string]string{"secret.txt": "secret\n"},
	}, {
		name:      "a deleted file's directory",
		body:      "*** Delete File: sub/a.txt",
		swap:      func(t *testing.T, ws, outside string) { swapDir(t, filepath.Join(ws, "sub"), outside) },
		untouched: map[string]string{"a.txt": "outside\n"},
	}, {
		name: "a move's destination directory, after an earlier write",
		body: "*** Update File: top.txt\n@@\n-top\n+TOP\n*** Update File: sub/a.txt\n*** Move to: dest/a.txt\n@@\n-one\n+uno",
		swap: func(t *testing.T, ws, outside string) {
			require.NoError(t, os.Mkdir(filepath.Join(outside, "dest"), 0o755))
			require.NoError(t, os.Symlink(filepath.Join(outside, "dest"), filepath.Join(ws, "dest")))
		},
		untouched: map[string]string{"dest/a.txt": ""},
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ws, outside := t.TempDir(), t.TempDir()
			write(t, filepath.Join(ws, "sub", "a.txt"), "one\n")
			write(t, filepath.Join(ws, "top.txt"), "top\n")
			write(t, filepath.Join(outside, "secret.txt"), "secret\n")
			write(t, filepath.Join(outside, "a.txt"), "outside\n")
			hunks, err := patch.Parse("*** Begin Patch\n" + tc.body + "\n*** End Patch")
			require.NoError(t, err)
			targets := targetsFor(t, ws, hunks)
			changes, err := targets.Compute(ws, hunks)
			require.NoError(t, err)

			tc.swap(t, ws, outside)
			err = targets.Write(changes)

			require.Error(t, err)
			assert.Contains(t, err.Error(), "symlink")
			for name, want := range tc.untouched {
				path := filepath.Join(outside, name)
				if want == "" {
					assert.NoFileExists(t, path)
					assert.NoDirExists(t, path)

					continue
				}
				assert.Equal(t, want, read(t, path))
			}
			assert.Equal(t, "top\n", read(t, filepath.Join(ws, "top.txt")), "an earlier write is undone")
		})
	}
}

// swapDir moves dir aside and puts a symlink to target in its place.
func swapDir(t *testing.T, dir, target string) {
	t.Helper()
	require.NoError(t, os.Rename(dir, dir+".aside"))
	require.NoError(t, os.Symlink(target, dir))
}

// TestTargets_UnapprovedPath checks that a path without a target fails.
func TestTargets_UnapprovedPath(t *testing.T) {
	ws := t.TempDir()
	hunks, err := patch.Parse("*** Begin Patch\n*** Add File: a.txt\n+a\n*** End Patch")
	require.NoError(t, err)
	changes, err := patch.Targets{}.Compute(ws, hunks)
	require.NoError(t, err, "an added file reads nothing")
	err = patch.Targets{}.Write(changes)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "was not approved")
	assert.NoFileExists(t, filepath.Join(ws, "a.txt"))
}
