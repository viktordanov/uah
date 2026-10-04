package composer_test

import "github.com/viktordanov/uah/internal/tui/composer"

// newComposer sets the composer up as uah's bubble package does.
func newComposer(width, maxHeight int) composer.Composer {
	c := composer.New()
	c.Placeholder = "Ask uah to do anything"
	c.SetPrompt(2, func(row int) string {
		if row == 0 {
			return "λ "
		}

		return "  "
	})
	c.SetMaxHeight(maxHeight)
	c.SetWidth(width)

	return c
}
