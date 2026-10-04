package shellenv

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDsclValue(t *testing.T) {
	assert.Equal(t, "/bin/zsh", dsclValue("UserShell: /bin/zsh\n"))
	assert.Equal(t, "/opt/my shells/fish", dsclValue("UserShell:\n /opt/my shells/fish\n"), "a value with a space is on its own line")
	assert.Empty(t, dsclValue("No such key: UserShell\n"))
	assert.Empty(t, dsclValue(""))
}

func TestPasswdShellReader(t *testing.T) {
	s, err := passwdShell(strings.NewReader("u:x:501:20::/Users/u:/bin/zsh\n"), 501)
	assert.NoError(t, err)
	assert.Equal(t, "/bin/zsh", s, "getent prints one passwd line")
}

func TestDsclShellRefusesPaths(t *testing.T) {
	ctx := t.Context()
	assert.Empty(t, dsclShell(ctx, ""))
	assert.Empty(t, dsclShell(ctx, "../root"))
}
