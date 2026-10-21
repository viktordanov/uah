package embedded

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptrace"
	"slices"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/viktordanov/uah-core/harness/primitives"

	"github.com/viktordanov/uah/internal/engine"
)

// DefaultStreamIdleTimeout is how long a model stream may send no data
// line before its request is retried, as Codex's stream_idle_timeout_ms
// (5 minutes there). HTTP/2 pings catch a dead connection within 45
// seconds; this catches a live one whose stream stopped. Healthy streams
// were never quiet for more than 34 seconds in 12,000 requests at every
// effort up to max, so 90 seconds retries a stalled one well before a user
// gives up on it, without retrying a slow one.
const DefaultStreamIdleTimeout = 90 * time.Second

// A model request's limits, which tests shorten: the wait for headers, for
// the next data line when the engine sets none, first for the network, and in all for a host that does not resolve. The
// runner allows 30 minutes without a byte per attempt (responsesapi/
// adapter.go in v0.1.1), which bounds the network wait.
var (
	responseHeaderTimeout = 2 * time.Minute
	streamIdleTimeout     = DefaultStreamIdleTimeout
	offlineWait           = 5 * time.Second
	notFoundWait          = 30 * time.Second
	dialContext           = (&net.Dialer{Timeout: 30 * time.Second, KeepAliveConfig: net.KeepAliveConfig{
		Enable: true, Idle: 30 * time.Second, Interval: 10 * time.Second, Count: 3,
	}}).DialContext
)

// retryPolicy is the runner's backoff and retried statuses.
var retryPolicy = primitives.DefaultRemoteRequest("", "", "").RetryPolicy

// modelTransport is the runner's (primitives.NewRemoteClient) with a header
// timeout, HTTP/2 pings, and keepalive probes: a dead connection times out.
func modelTransport(headerTimeout time.Duration) *http.Transport {
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment, DialContext: dialContext, ForceAttemptHTTP2: true, MaxIdleConns: 100,
		IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: 10 * time.Second, ExpectContinueTimeout: time.Second,
		ResponseHeaderTimeout: headerTimeout,
		HTTP2:                 &http.HTTP2Config{SendPingTimeout: 30 * time.Second, PingTimeout: 15 * time.Second, WriteByteTimeout: time.Minute},
	}
}

// transports are an engine's model transports by header timeout, so its
// runs and their subagents reuse connections; the engine closes their idle
// ones when it closes (a run's client cannot reach them through
// callTransport). A nil one makes a new transport each time.
type transports struct{ m sync.Map }

func (t *transports) get(headerTimeout time.Duration) *http.Transport {
	if t == nil {
		return modelTransport(headerTimeout)
	}
	tr, _ := t.m.LoadOrStore(headerTimeout, modelTransport(headerTimeout))

	return tr.(*http.Transport) //nolint:forcetypeassert // only transports are stored
}

// callTransport hands each attempt of an observed request to its modelCall.
type callTransport struct{ base http.RoundTripper }

func (t callTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	c, _ := req.Context().Value(callKey{}).(*modelCall)
	if c == nil {
		return t.base.RoundTrip(req) // a transport returns its base's errors unchanged
	}
	req, err := c.rewriteBody(req)
	if err != nil {
		return nil, err
	}
	c.start(req.ContentLength)
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{ //nolint:contextcheck // the request's own
		GotConn:              func(httptrace.GotConnInfo) { c.phase(engine.PhaseSending, &c.a.ConnectMS) },
		WroteRequest:         func(httptrace.WroteRequestInfo) { c.phase(engine.PhaseWaiting, nil) },
		GotFirstResponseByte: func() { c.phase(engine.PhaseStreaming, &c.a.FirstByteMS) },
	}))
	for wait := offlineWait; ; wait = min(2*wait, time.Minute) {
		resp, err := t.base.RoundTrip(req)
		switch {
		case err == nil:
			c.response(resp)
		case !offline(err, req) || !c.waitForNetwork(req.Context(), err, wait): //nolint:contextcheck // the request's own
			c.mu.Lock()
			c.failLocked(err.Error(), true, apiError{}, nil)
			c.mu.Unlock()
		case req.GetBody != nil:
			if req.Body, err = req.GetBody(); err == nil {
				continue
			}
		default:
			continue
		}

		return resp, err // a transport returns its base's errors unchanged
	}
}

// offline reports a name that does not resolve, no route, or a dial that
// timed out; a loopback server that fails is down, not offline.
func offline(err error, req *http.Request) bool {
	_, dns := errors.AsType[*net.DNSError](err)
	dial, isDial := errors.AsType[*net.OpError](err)

	return !loopback(req.URL.Hostname()) && (dns || isDial && dial.Op == "dial" && dial.Timeout() || errors.Is(err, syscall.ENETUNREACH) ||
		errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.ENETDOWN) || errors.Is(err, syscall.EADDRNOTAVAIL))
}

func loopback(host string) bool {
	ip := net.ParseIP(host)

	return host == "localhost" || ip != nil && ip.IsLoopback()
}

// idleTimeout is how long the call's stream may send no data line.
func (c *modelCall) idleTimeout() time.Duration {
	return cmp.Or(c.streamIdle, streamIdleTimeout)
}

// errNoSuchHost ends a request whose host did not resolve for notFoundWait.
var errNoSuchHost = errors.New("no such host")

// waitForNetwork reports a wait of about wait ±10% within the same
// attempt, and whether it ended before ctx did. A name that does not
// resolve may be offline (macOS reports no network so) or a typo, so the
// request waits notFoundWait for it in all, then ends with no such host:
// the runner would retry the error with nothing new to learn.
func (c *modelCall) waitForNetwork(ctx context.Context, err error, wait time.Duration) bool {
	wait += time.Duration((rand.Float64()*0.2 - 0.1) * float64(wait)) // jitter
	if dns, ok := errors.AsType[*net.DNSError](err); ok && dns.IsNotFound {
		c.mu.Lock()
		wait = min(wait, notFoundWait-c.notFound)
		c.notFound += max(wait, 0)
		c.mu.Unlock()
		if wait <= 0 {
			c.stop(fmt.Errorf("%w: %s", errNoSuchHost, dns.Name))

			return false
		}
	}
	c.mu.Lock()
	c.retrying, c.a.NetworkWaitMS = true, c.a.NetworkWaitMS+wait.Milliseconds()
	c.pushLocked(engine.Reconnecting{At: time.Now(), Attempt: c.a.Attempt, MaxAttempts: c.max, Delay: wait, Reason: "waiting for network: " + err.Error(), Offline: true})
	c.mu.Unlock()
	select {
	case <-time.After(wait):
		return true
	case <-ctx.Done():
		return false
	}
}

// idleError is a quiet stream's net.Error timeout, which the runner retries.
type idleError struct{ after time.Duration }

func (e idleError) Error() string { return "no data from the model for " + e.after.String() }
func (idleError) Timeout() bool   { return true }
func (idleError) Temporary() bool { return true }

// attemptBody passes a 2xx stream to the runner a line at a time, after the
// scanner (sse.go), and closes it when no data line comes for the call's
// idle timeout.
type attemptBody struct {
	io.ReadCloser

	c     *modelCall
	idle  atomic.Bool
	timer *time.Timer
	once  sync.Once
	line  []byte
	out   bytes.Buffer
	err   error
}

// response starts an answer: a 2xx ends the retries, a retried status fails.
func (c *modelCall) response(resp *http.Response) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.a.Status = resp.StatusCode; resp.StatusCode/100 != 2 {
		if slices.Contains(retryPolicy.RetryableStatusCodes, resp.StatusCode) {
			c.failLocked(resp.Status, false, apiError{}, resp.Header)
		}

		return
	}
	if c.retrying {
		c.retrying = false
		c.pushLocked(engine.ReconnectEnded{At: time.Now(), OK: true})
	}
	b := &attemptBody{ReadCloser: resp.Body, c: c}
	b.timer = time.AfterFunc(c.idleTimeout(), func() { b.idle.Store(true); _ = b.ReadCloser.Close() })
	resp.Body = b
}

func (b *attemptBody) Read(p []byte) (int, error) {
	for b.out.Len() == 0 && b.err == nil {
		n, err := b.ReadCloser.Read(p)
		for data := p[:n]; len(data) > 0; {
			i := bytes.IndexByte(data, '\n') + 1
			if i == 0 {
				i = len(data)
			}
			b.line, data = append(b.line, data[:i]...), data[i:]
			if b.line[len(b.line)-1] == '\n' {
				b.endLine()
			}
		}
		if err != nil {
			b.endLine()
			b.err = b.finish(err)
		}
	}
	n, _ := b.out.Read(p)
	if b.out.Len() == 0 {
		return n, b.err
	}

	return n, nil
}

func (b *attemptBody) endLine() {
	if bytes.HasPrefix(b.line, dataPrefix) {
		b.timer.Reset(b.c.idleTimeout())
	}
	b.out.Write(b.c.line(b.line))
	b.line = b.line[:0]
}

func (b *attemptBody) Close() error {
	err := b.ReadCloser.Close()
	_ = b.finish(nil)

	return err // a body returns its base's errors unchanged
}

// finish reports the attempt's end: EOF, a read error, or a close.
func (b *attemptBody) finish(err error) error {
	b.timer.Stop()
	if b.idle.Load() {
		err = idleError{b.c.idleTimeout()}
	}
	b.once.Do(func() { b.c.ended(err) })

	return err
}
