package bubble

import "github.com/viktordanov/uah/internal/tui/term"

// calls runs the session calls one after another, in the order Update made
// them: term runs each command in its own goroutine, so two tab presses
// could otherwise reach the session, and be queued, in either order.
type calls struct{ tail chan struct{} }

// next returns a command that runs fn after the command next returned
// before it has finished. Only Update's goroutine may call it.
func (c *calls) next(fn func() term.Msg) term.Cmd {
	prev, done := c.tail, make(chan struct{})
	c.tail = done

	return func() term.Msg {
		if prev != nil {
			<-prev
		}
		defer close(done)

		return fn()
	}
}
