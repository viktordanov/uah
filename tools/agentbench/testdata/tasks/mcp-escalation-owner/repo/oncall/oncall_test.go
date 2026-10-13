package oncall

import (
	"strings"
	"testing"
)

func TestPage(t *testing.T) {
	if !strings.Contains(Page(), Owner) || !strings.Contains(Page(), Service) {
		t.Fatalf("Page() = %q", Page())
	}
}
