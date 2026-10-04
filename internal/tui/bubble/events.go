package bubble

import (
	"time"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/tui/term"
)

// next waits for the next batch of a session's events. Update re-arms it
// after each batch, so exactly one waits at a time and order is kept.
func next(gen int, batches <-chan []core.Event) term.Cmd {
	return func() term.Msg {
		events, ok := <-batches
		if !ok {
			return sessionClosedMsg{gen: gen}
		}

		return eventsMsg{gen: gen, events: events, batches: batches}
	}
}

// batch groups session events into 16 ms batches, so a burst costs one
// update and one frame. It closes out when the session's events end.
func batch(events <-chan core.Event, out chan<- []core.Event) {
	defer close(out)
	for first := range events {
		batchOut := []core.Event{first}
		timer := time.NewTimer(batchWindow)
	collect:
		for {
			select {
			case e, ok := <-events:
				if !ok {
					break collect
				}
				batchOut = append(batchOut, e)
			case <-timer.C:
				break collect
			}
		}
		timer.Stop()
		out <- batchOut
	}
}
