package csvq

import "testing"

func TestFieldHidden(t *testing.T) {
	for in, want := range map[string]string{
		"plain":      "plain",
		"a,b":        `"a,b"`,
		`say "hi"`:   `"say ""hi"""`,
		"two\nlines": "\"two\nlines\"",
		"cr\rhere":   "\"cr\rhere\"",
		"":           "",
	} {
		if got := Field(in); got != want {
			t.Errorf("Field(%q) = %q, want %q", in, got, want)
		}
	}
	if got := Line([]string{"x", `y"`, "z,w"}); got != `x,"y""","z,w"` {
		t.Errorf("Line = %q", got)
	}
}
