package app

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/viktordanov/uah/internal/shellenv"
)

func TestShellNotice(t *testing.T) {
	assert.Empty(t, shellNotice(shellenv.Shell{Path: "/bin/zsh", Source: shellenv.FromEnv}))
	assert.Equal(t, "SHELL is unset; commands run in /bin/zsh (your login shell)",
		shellNotice(shellenv.Shell{Path: "/bin/zsh", Source: shellenv.FromLogin}))
	assert.Equal(t, "SHELL (/usr/bin/fish) is not an executable file; commands run in /bin/zsh (your login shell)",
		shellNotice(shellenv.Shell{Path: "/bin/zsh", Source: shellenv.FromLogin, Env: "/usr/bin/fish"}))
	assert.Equal(t, "SHELL is unset and the login shell could not be read; commands run in /bin/sh",
		shellNotice(shellenv.Shell{Path: "/bin/sh", Source: shellenv.FromDefault}))
}
