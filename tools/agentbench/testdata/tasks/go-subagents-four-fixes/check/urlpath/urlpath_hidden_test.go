package urlpath

import "testing"

func TestJoinHidden(t *testing.T) {
	for _, c := range []struct {
		in   []string
		want string
	}{
		{[]string{"/api/", "/v1/", "users"}, "/api/v1/users"},
		{[]string{"api", "v1", "users/"}, "/api/v1/users/"},
		{[]string{"a"}, "/a"},
		{[]string{"/a//", "", "b"}, "/a/b"},
		{[]string{"x", "y/"}, "/x/y/"},
	} {
		if got := Join(c.in...); got != c.want {
			t.Errorf("Join(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
