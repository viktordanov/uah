package shellenv_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/shellenv"
)

func TestLocale(t *testing.T) {
	for _, c := range []struct {
		env     map[string]string
		setting string
		utf8    bool
	}{
		{map[string]string{}, "", false},
		{map[string]string{"LANG": "en_US.UTF-8"}, "LANG=en_US.UTF-8", true},
		{map[string]string{"LANG": "C.utf8"}, "LANG=C.utf8", true},
		{map[string]string{"LANG": "C"}, "LANG=C", false},
		{map[string]string{"LC_CTYPE": "UTF-8"}, "LC_CTYPE=UTF-8", true}, // macOS Terminal's
		{map[string]string{"LANG": "en_US.UTF-8", "LC_ALL": "C"}, "LC_ALL=C", false},
		{map[string]string{"LANG": "C", "LC_CTYPE": "en_US.UTF-8"}, "LC_CTYPE=en_US.UTF-8", true},
		{map[string]string{"LANG": "en_US.ISO8859-1"}, "LANG=en_US.ISO8859-1", false},
	} {
		setting, utf8 := shellenv.Locale(func(k string) string { return c.env[k] })
		assert.Equal(t, c.setting, setting, "%v", c.env)
		assert.Equal(t, c.utf8, utf8, "%v", c.env)
	}
}

func TestMissingToolDirs(t *testing.T) {
	home := t.TempDir()
	local := filepath.Join(home, ".local", "bin")
	gobin := filepath.Join(home, "go", "bin")
	require.NoError(t, os.MkdirAll(local, 0o755))
	require.NoError(t, os.MkdirAll(gobin, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".cargo"), nil, 0o600)) // a file, not a directory

	// The system's own directories are whatever this machine has.
	var system []string
	for _, d := range []string{"/opt/homebrew/bin", "/usr/local/bin"} {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			system = append(system, d)
		}
	}
	missing := func(path string) []string {
		return shellenv.MissingToolDirs(func(k string) string {
			return map[string]string{"HOME": home, "PATH": path}[k]
		})
	}

	assert.Equal(t, append(system, local, gobin), missing("/usr/bin:/bin:/usr/sbin:/sbin"), "a minimal PATH")
	assert.Equal(t, append(system, local, gobin), missing(""), "no PATH")
	assert.Nil(t, missing("/usr/bin:"+gobin+"/:/bin"), "one of them on PATH, spelled with a slash")
	assert.Nil(t, missing(local), "one of them on PATH")
	if len(system) > 0 {
		assert.Nil(t, missing(system[0]+":/usr/bin"), "a system one on PATH")
	}
}
