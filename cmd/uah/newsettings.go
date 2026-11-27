package main

import (
	"fmt"
	"io"
	"sync"
)

// pendingSettings are the settings new since this home last ran a TUI:
// each session the TUI opens names them until one opens, and that one
// records them, so a session that fails to open, or a resume whose history
// does not load, leaves them for the next. load reads them once; a record
// that fails is written to log, and the next session tries again.
type pendingSettings struct {
	mu     sync.Mutex
	load   func() (notes []string, record func() error)
	log    io.Writer
	loaded bool
	notes  []string
	record func() error
}

// take returns the notes for the session about to open, and opened, to
// call once it did.
func (p *pendingSettings) take() (notes []string, opened func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.loaded {
		p.notes, p.record = p.load()
		p.loaded = true
	}
	if p.record == nil {
		return nil, func() {}
	}

	return p.notes, func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.record == nil {
			return
		}
		if err := p.record(); err != nil {
			fmt.Fprintf(p.log, "the new settings notice was not recorded, so it shows again: %v\n", err)

			return
		}
		p.record, p.notes = nil, nil
	}
}
