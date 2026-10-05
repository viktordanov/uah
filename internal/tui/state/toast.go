package state

import "time"

// Toast is a short note drawn over the transcript's corner for a moment,
// such as "copied 3 lines": an overlay, so it moves nothing and leaves the
// status line alone. The clock ticks while one shows, and the tick that
// reaches Until takes it away.
type Toast struct {
	Text  string
	Until time.Time
}

// toastFor is how long a toast shows.
const toastFor = 2 * time.Second

// ShowToast shows text as the toast from at, in place of any other.
func (s *State) ShowToast(text string, at time.Time) {
	s.Toast = &Toast{Text: text, Until: at.Add(toastFor)}
}

func (s *State) expireToast() {
	if s.Toast != nil && !s.Now.Before(s.Toast.Until) {
		s.Toast = nil
	}
}
