package sandbox_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/viktordanov/uah/internal/sandbox"
)

func TestEnvPolicyApply(t *testing.T) {
	environ := []string{
		"PATH=/usr/bin",
		"HOME=/home/u",
		"OPENAI_API_KEY=sk-1",
		"GITHUB_TOKEN=gh",
		"AWS_SECRET_ACCESS_KEY=aws",
		"EDITOR=vi",
		"lang=C",
		"EMPTY=",
		"WEIRD=a=b",
	}
	no := false
	yes := true
	cases := []struct {
		name   string
		policy sandbox.EnvPolicy
		want   []string
	}{
		{"default inherits everything", sandbox.EnvPolicy{}, environ},
		{"explicit all", sandbox.EnvPolicy{Inherit: sandbox.InheritAll, IgnoreDefaultExcludes: &yes}, environ},
		{"core", sandbox.EnvPolicy{Inherit: sandbox.InheritCore}, []string{"PATH=/usr/bin", "HOME=/home/u", "lang=C"}},
		{"none", sandbox.EnvPolicy{Inherit: sandbox.InheritNone}, []string{}},
		{
			"default excludes",
			sandbox.EnvPolicy{IgnoreDefaultExcludes: &no},
			[]string{"PATH=/usr/bin", "HOME=/home/u", "EDITOR=vi", "lang=C", "EMPTY=", "WEIRD=a=b"},
		},
		{
			"exclude is case-insensitive",
			sandbox.EnvPolicy{Exclude: []string{"*_key", "ed?tor", "LANG"}},
			[]string{"PATH=/usr/bin", "HOME=/home/u", "GITHUB_TOKEN=gh", "EMPTY=", "WEIRD=a=b"},
		},
		{
			"set overrides and adds",
			sandbox.EnvPolicy{Inherit: sandbox.InheritCore, Set: map[string]string{"PATH": "/bin", "CI": "1", "A": "2"}},
			[]string{"PATH=/bin", "HOME=/home/u", "lang=C", "A=2", "CI=1"},
		},
		{
			"set survives exclude but not include_only",
			sandbox.EnvPolicy{Exclude: []string{"*"}, Set: map[string]string{"CI": "1", "X_TOKEN": "t"}, IncludeOnly: []string{"ci", "path"}},
			[]string{"CI=1"},
		},
		{
			"include_only",
			sandbox.EnvPolicy{IncludeOnly: []string{"PATH", "*_TOKEN"}},
			[]string{"PATH=/usr/bin", "GITHUB_TOKEN=gh"},
		},
		{
			"set runs after default excludes",
			sandbox.EnvPolicy{Inherit: sandbox.InheritNone, IgnoreDefaultExcludes: &no, Set: map[string]string{"MY_TOKEN": "x"}},
			[]string{"MY_TOKEN=x"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, c.policy.Apply(environ))
		})
	}
}

func TestEnvPolicyGetenv(t *testing.T) {
	env := map[string]string{"LANG": "en_US.UTF-8", "PATH": "/usr/bin:/opt/homebrew/bin", "HOME": "/home/u"}
	getenv := func(k string) string { return env[k] }

	same := sandbox.EnvPolicy{}.Getenv(getenv, "LANG", "PATH")
	assert.Equal(t, "en_US.UTF-8", same("LANG"))
	assert.Equal(t, "/usr/bin:/opt/homebrew/bin", same("PATH"))
	assert.Empty(t, same("HOME"), "a key not asked for")

	p := sandbox.EnvPolicy{Inherit: sandbox.InheritNone, Set: map[string]string{"LC_ALL": "C", "PATH": "/bin"}}
	get := p.Getenv(getenv, "LANG", "LC_ALL", "PATH")
	assert.Empty(t, get("LANG"), "not inherited")
	assert.Equal(t, "C", get("LC_ALL"), "set by the policy")
	assert.Equal(t, "/bin", get("PATH"))

	only := sandbox.EnvPolicy{IncludeOnly: []string{"PATH"}}.Getenv(getenv, "LANG", "PATH")
	assert.Empty(t, only("LANG"))
	assert.Equal(t, "/usr/bin:/opt/homebrew/bin", only("PATH"))
}
