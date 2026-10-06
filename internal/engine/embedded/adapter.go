package embedded

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/viktordanov/uah-core/harness/llm"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/engine"
)

// switcher is the llm.Adapter the coordinator calls. It applies the live
// model to each request and routes to the priority client when the tier asks
// for it, and to the ultra client at effort ultra, so /model, /fast, and
// /effort apply from the next model request.
type switcher struct {
	guardianMarkers bool
	latestResponse  atomic.Pointer[string]
	build           func(variant) (Client, error)

	mu      sync.Mutex
	model   string
	variant variant
	clients map[variant]Client
	// seen, when set, sees each request sent and the usage reported for it.
	seen func(llm.Request, llm.Usage)
	// cacheKey, when set, replaces the session's ID as the prompt cache key.
	cacheKey string
	// tools, when set, rewrites each request's tools, such as Bash for the
	// run's current permission mode.
	tools func([]llm.Tool) []llm.Tool
	// images, when set, gives the model the images pasted into user
	// messages (images.go).
	images func(llm.Request) llm.Request
	// stream, when set, receives each model request's retries and, for a
	// turn request, its progress, its web searches (websearch.go), and when
	// text is set its text as it arrives (sse.go).
	stream func(core.Event)
	text   bool
	// searches, when set, records the session's web searches and puts
	// them back into later turn requests (searchlog.go).
	searches *searchLog
	// verbosity, when set, is the text.verbosity for a model
	// (Manager.Verbosity); verbosities keeps each model's answer.
	verbosity   func(model string) llm.Verbosity
	verbosities map[string]llm.Verbosity
	// adaptive picks each turn request's effort when adaptive effort is
	// on (adaptive.go); setAdaptive changes it.
	adaptive adaptiveRouter
	// updates, when set, reports whether a model takes effort updates
	// (UAH_EFFORT_UPDATES and the catalog); base is then the effort each
	// request carries, the session's first, and update effortUpdate's
	// choice for the next turn request (adaptive.go). rejected is set once
	// the backend rejected them, and offUpdates saves that for the
	// session's later runs (effortfallback.go).
	updates    func(model string) bool
	base       llm.ReasoningEffort
	update     *effortChoice
	rejected   bool
	offUpdates func(engine.EffortUpdatesOff) error
	// max is the attempt limit; diag gets the diagnostics (modelcall.go).
	max  int
	diag io.Writer
	// calls counts the model requests in flight; idle closes when it falls
	// to zero (modelcall.go).
	callsMu sync.Mutex
	calls   int
	idle    chan struct{}
}

// variant is what a client is built for: priority processing, and effort
// ultra, which the runner cannot carry (llm.ReasoningEffort stops at max),
// so the ultra client sends the reasoning field itself.
type variant struct{ priority, ultra bool }

func newSwitcher(model string, v variant, maxAttempts int, build func(variant) (Client, error)) (*switcher, error) {
	s := &switcher{build: build, model: model, max: maxAttempts, clients: map[variant]Client{}}
	if err := s.use(v); err != nil {
		return nil, err
	}

	return s, nil
}

func (s *switcher) Respond(ctx context.Context, req llm.Request, opts llm.RequestOptions) (llm.Response, error) {
	resp, updates, err := s.respond(ctx, req, opts)
	if err != nil && updates && ctx.Err() == nil {
		return s.fallBack(ctx, req, opts, err)
	}

	return resp, err // the coordinator wraps model errors
}

// respond sends req; updates reports whether it carried effort updates.
func (s *switcher) respond(ctx context.Context, req llm.Request, opts llm.RequestOptions) (resp llm.Response, updates bool, err error) {
	_, compacting := ctx.Value(remoteCallKey{}).(*remoteCall)
	_, retry := ctx.Value(noUpdatesKey{}).(bool)
	s.mu.Lock()
	v, model := s.variant, s.model
	line := effortLine{}
	if !retry && s.updatingLocked(req.Model.ID) {
		// The history's updates set the effort; the request keeps the base,
		// or after a compaction its pin.
		base := requestEffort(ctx, s.base)
		line.effort, line.request = string(lastEffort(req.Input, base)), string(base)
		req.Model.ReasoningEffort = base
		updates = slices.ContainsFunc(req.Input, isUpdate)
		if c := s.update; c != nil && !compacting {
			line.reason, line.update = c.reason, c.updated
			s.update = nil
		}
	} else {
		req = req.WithoutConfigurationUpdates()
		if s.adaptive.steps > 0 && !compacting {
			c := s.adaptive.route(req.Input, req.Model.ReasoningEffort, v.ultra)
			req.Model.ReasoningEffort, v.ultra, line.reason = c.effort, c.ultra, c.reason
		}
		line.effort = string(req.Model.ReasoningEffort)
		if v.ultra {
			line.effort = effortUltra
		}
	}
	client, err := s.clientLocked(v)
	s.mu.Unlock()
	if err != nil {
		return llm.Response{}, false, err
	}
	if model != "" {
		req.Model.ID = model
	}
	req.Model.Verbosity = s.verbosityFor(req.Model)
	if v.ultra {
		req.Model.ReasoningEffort = "" // the client's extension sends ultra
	}
	if s.tools != nil {
		req.Tools = s.tools(req.Tools)
	}
	if s.images != nil {
		req = s.images(req)
	}
	if s.cacheKey != "" {
		opts.CacheKey = s.cacheKey
	}
	ctx, done := s.observe(ctx, kindTurn)
	if c, ok := ctx.Value(callKey{}).(*modelCall); ok {
		c.setEffort(line)
	}
	resp, err = client.Respond(ctx, req, opts)
	err = done(err)
	if err == nil && s.seen != nil && !compacting {
		s.seen(req, resp.Usage)
	}

	return resp, updates, err
}

// verbosityFor is the request's verbosity, else the model's (verbosity).
func (s *switcher) verbosityFor(m llm.Model) llm.Verbosity {
	if m.Verbosity != "" || s.verbosity == nil {
		return m.Verbosity
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.verbosities[m.ID]
	if !ok {
		v = s.verbosity(m.ID)
		if s.verbosities == nil {
			s.verbosities = map[string]llm.Verbosity{}
		}
		s.verbosities[m.ID] = v
	}

	return v
}

func (s *switcher) setModel(model string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.model = model
}

// setAdaptive sets adaptive effort's steps for the next request (0: off).
func (s *switcher) setAdaptive(steps int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.adaptive.steps = steps
}

// setPriority switches priority processing on or off.
func (s *switcher) setPriority(priority bool) error {
	v := s.current()
	v.priority = priority

	return s.use(v)
}

// setUltra switches effort ultra on or off.
func (s *switcher) setUltra(ultra bool) error {
	v := s.current()
	v.ultra = ultra

	return s.use(v)
}

func (s *switcher) current() variant {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.variant
}

// use switches to the variant, building its client on first use.
func (s *switcher) use(v variant) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.clientLocked(v); err != nil {
		return err
	}
	s.variant = v

	return nil
}

func (s *switcher) clientLocked(v variant) (Client, error) {
	if c, ok := s.clients[v]; ok {
		return c, nil
	}
	c, err := s.build(v)
	if err != nil {
		if v.priority {
			return nil, fmt.Errorf("failed to enable priority processing: %w", err)
		}

		return nil, err
	}
	s.clients[v] = c

	return c, nil
}

func (s *switcher) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var errs []error
	for _, c := range s.clients {
		errs = append(errs, c.Close())
	}

	return errors.Join(errs...)
}

// currentModel is the model the next request goes to.
func (s *switcher) currentModel() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.model
}

// direct is the current client without the live model override, for
// one-shot calls that pick their own model; an empty model is the live one.
func (s *switcher) direct() llm.Adapter { return s.directKind(kindDirect) }

func (s *switcher) directKind(kind string) llm.Adapter {
	return adapterFunc(func(ctx context.Context, req llm.Request, opts llm.RequestOptions) (llm.Response, error) {
		s.mu.Lock()
		v, model := s.variant, s.model
		if kind == kindReview {
			v.priority = false
		}
		// At ultra the runner's effort is max: a call that kept it goes at
		// ultra, one that picked its own effort does not, unless it picked
		// ultra (compact_effort).
		v.ultra = req.Model.ReasoningEffort == effortUltra || v.ultra && req.Model.ReasoningEffort == llm.ReasoningEffortMax
		client, err := s.clientLocked(v)
		s.mu.Unlock()
		if err != nil {
			return llm.Response{}, err
		}
		if v.ultra {
			req.Model.ReasoningEffort = ""
		}
		if req.Model.ID == "" {
			req.Model.ID = model
		}
		// A one-shot call picks its own effort; a compaction summary's
		// history may carry updates (adaptive.go).
		req = req.WithoutConfigurationUpdates()
		req.Model.Verbosity = s.verbosityFor(req.Model)
		if s.images != nil {
			req = s.images(req) // a compaction summary sees the pasted images too
		}
		if s.cacheKey != "" {
			opts.CacheKey = s.cacheKey
		}
		ctx, done := s.observe(ctx, kind)
		resp, err := client.Respond(ctx, req, opts)

		return resp, done(err) // llmcall wraps model errors
	})
}

type adapterFunc func(context.Context, llm.Request, llm.RequestOptions) (llm.Response, error)

func (f adapterFunc) Respond(ctx context.Context, req llm.Request, opts llm.RequestOptions) (llm.Response, error) {
	return f(ctx, req, opts)
}
