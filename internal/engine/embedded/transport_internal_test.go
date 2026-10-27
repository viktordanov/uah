package embedded

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/viktordanov/uah-core/harness/llm"
	"github.com/viktordanov/uah-core/harness/llm/responsesapi"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/testing/fakellm"
)

// The tests that change the package's limits do not run in parallel.

// setLimit sets a package limit for the test.
func setLimit(t *testing.T, limit *time.Duration, d time.Duration) {
	t.Helper()
	old := *limit
	*limit = d
	t.Cleanup(func() { *limit = old })
}

// ask sends one turn request to url through a switcher whose clients use
// headerTimeout, with diagnostics to diag, and returns the request's events
// and error.
func ask(t *testing.T, url string, headerTimeout time.Duration, diag io.Writer) ([]core.Event, error) {
	t.Helper()

	return askUntil(t, url, headerTimeout, diag, nil)
}

// askStreamIdle is the switcher's stream idle timeout in ask, as the
// engine's StreamIdleTimeout sets it (0: the default).
var askStreamIdle time.Duration

// askUntil is ask that cancels the request at the first event for which
// stop, when set, reports true.
func askUntil(t *testing.T, url string, headerTimeout time.Duration, diag io.Writer, stop func(core.Event) bool) ([]core.Event, error) {
	t.Helper()
	const attempts = 3
	ra, err := newClient(remoteHTTPClient(nil, headerTimeout), ClientConfig{MaxAttempts: attempts}, responsesapi.Config{
		Endpoint: url + "/responses", Headers: map[string][]string{headerContentType: {contentJSON}},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ra.Close() })
	sw, err := newSwitcher("m", variant{}, attempts, func(variant) (Client, error) { return ra, nil })
	require.NoError(t, err)
	sw.streamIdle = askStreamIdle
	var mu sync.Mutex
	var events []core.Event
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	sw.diag, sw.stream = diag, func(e core.Event) {
		mu.Lock()
		events = append(events, e)
		mu.Unlock()
		if stop != nil && stop(e) {
			cancel()
		}
	}
	_, err = sw.Respond(ctx, llm.Request{
		Model: llm.Model{ID: "m"},
		Input: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleUser, Text: "hi"}}},
	}, llm.RequestOptions{})
	mu.Lock()
	defer mu.Unlock()

	return events, err
}

func only[T core.Event](events []core.Event) []T {
	var out []T
	for _, e := range events {
		if v, ok := e.(T); ok {
			out = append(out, v)
		}
	}

	return out
}

// TestModelCall_RetriesVisibly: each way an attempt fails without a lost
// connection is retried, and reported with its reason and the runner's
// delay: a silent stream, an in-band failure, a stream without its
// completed response, and a status with Retry-After.
func TestModelCall_RetriesVisibly(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the runner's backoff, about 6 s")
	}
	setLimit(t, &streamIdleTimeout, 300*time.Millisecond)
	for _, tc := range []struct {
		name   string
		reply  fakellm.Reply
		reason string
		delay  time.Duration
	}{
		{"silent", fakellm.Reply{Deltas: []string{"a"}, Hold: make(chan struct{})}, "no data from the model for 300ms", 2 * time.Second},
		{"in-band", fakellm.Reply{Fail: http.StatusOK, FailCode: "server_error"}, "server_error: fakellm: server_error", 2 * time.Second},
		{"no end", fakellm.Reply{Deltas: []string{"a"}, NoEnd: true}, "the stream ended before the response completed", 2 * time.Second},
		{"retry-after", fakellm.Reply{Fail: http.StatusTooManyRequests, RetryAfter: "1"}, "429 Too Many Requests", time.Second},
		{"overloaded", fakellm.Reply{Fail: http.StatusOK, FailCode: "server_is_overloaded"}, "server_is_overloaded", 10 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel() // each waits out its own backoff
			srv := fakellm.New(t, tc.reply, fakellm.Reply{Text: "back"})
			if tc.delay > 5*time.Second { // only the reported delay is checked
				srv = fakellm.New(t, tc.reply, fakellm.Reply{Fail: http.StatusBadRequest, FailCode: "invalid_prompt"})
			}
			var stop func(core.Event) bool
			if tc.delay > 5*time.Second { // no need to wait it out
				stop = func(e core.Event) bool { _, ok := e.(engine.Reconnecting); return ok }
			}
			events, err := askUntil(t, srv.URL, 0, nil, stop)
			retries := only[engine.Reconnecting](events)
			require.NotEmpty(t, retries)
			assert.Equal(t, 2, retries[0].Attempt)
			assert.Contains(t, retries[0].Reason, tc.reason)
			assert.Equal(t, tc.delay, retries[0].Delay)
			if tc.delay > 5*time.Second {
				return
			}
			require.NoError(t, err)
			ended := only[engine.ReconnectEnded](events)
			require.NotEmpty(t, ended)
			assert.True(t, ended[len(ended)-1].OK)
		})
	}
}

// TestRetryDelay: the runner's backoff without its jitter, doubling from
// 2 s to 30 s, from 10 s for an overload, or the server's hint.
func TestRetryDelay(t *testing.T) {
	t.Parallel()
	var got []time.Duration
	for n := 1; n <= 6; n++ {
		got = append(got, retryDelay(n, apiError{}, nil))
	}
	assert.Equal(t, []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second}, got)
	overloaded := apiError{Code: "server_is_overloaded"}
	assert.Equal(t, [2]time.Duration{10 * time.Second, 20 * time.Second}, [2]time.Duration{retryDelay(1, overloaded, nil), retryDelay(2, overloaded, nil)})
	assert.Equal(t, 3*time.Second, retryDelay(1, apiError{}, http.Header{"Retry-After": {"3"}}))
}

// TestModelCall_HeaderTimeout: a server that takes the request and never
// answers times out after responseHeaderTimeout, and the retry answers.
func TestModelCall_HeaderTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the runner's backoff, about 2 s")
	}
	t.Parallel()
	srv := fakellm.New(t, fakellm.Reply{Freeze: true}, fakellm.Reply{Text: "back"})
	events, err := ask(t, srv.URL, 300*time.Millisecond, nil)
	require.NoError(t, err)
	retries := only[engine.Reconnecting](events)
	require.Len(t, retries, 1)
	assert.Contains(t, retries[0].Reason, "timeout awaiting response headers")
	assert.Len(t, srv.Requests(), 2)
}

// TestModelCall_WaitsForTheNetwork: while the network is unreachable the
// request waits, within its first attempt, and reports it; then it goes.
func TestModelCall_WaitsForTheNetwork(t *testing.T) {
	srv := fakellm.New(t)
	addr := strings.TrimPrefix(srv.URL, "http://")
	fails := 2
	var mu sync.Mutex
	old := dialContext
	dialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		mu.Lock()
		defer mu.Unlock()
		if fails > 0 {
			fails--

			return nil, &net.OpError{Op: "dial", Net: network, Err: syscall.ENETUNREACH}
		}

		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	t.Cleanup(func() { dialContext = old })
	setLimit(t, &offlineWait, 10*time.Millisecond)

	var diag bytes.Buffer
	events, err := ask(t, "http://model.test", 0, &diag)
	require.NoError(t, err)
	waits := only[engine.Reconnecting](events)
	require.Len(t, waits, 2)
	for _, w := range waits {
		assert.True(t, w.Offline)
		assert.Equal(t, 1, w.Attempt, "waiting uses up no attempt")
		assert.Contains(t, w.Reason, "waiting for network")
	}
	assert.Len(t, srv.Requests(), 1)
	var line struct {
		Diag, Kind, Result string
		Attempt, Status    int
		NetworkWaitMS      int64 `json:"network_wait_ms"`
	}
	require.NoError(t, json.Unmarshal(diag.Bytes(), &line), "one line for the one attempt")
	assert.Equal(t, [3]string{"model_attempt", "turn", "ok"}, [3]string{line.Diag, line.Kind, line.Result})
	assert.Equal(t, [2]int{1, http.StatusOK}, [2]int{line.Attempt, line.Status})
	assert.Positive(t, line.NetworkWaitMS)
	ended := only[engine.ReconnectEnded](events)
	require.Len(t, ended, 1)
	assert.True(t, ended[0].OK)
}

// TestModelCall_NoSuchHost: a host that does not resolve is waited for as
// the network is, notFoundWait in all, then the request fails at once, in
// its first attempt, with no such host; one that resolves in time goes.
func TestModelCall_NoSuchHost(t *testing.T) {
	srv := fakellm.New(t, fakellm.Reply{Text: "back"})
	addr := strings.TrimPrefix(srv.URL, "http://")
	var mu sync.Mutex
	resolves := false
	old := dialContext
	dialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		mu.Lock()
		defer mu.Unlock()
		if !resolves {
			return nil, &net.OpError{Op: "dial", Net: network, Err: &net.DNSError{Err: "no such host", Name: "model.test", IsNotFound: true}}
		}

		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	t.Cleanup(func() { dialContext = old })
	setLimit(t, &offlineWait, 10*time.Millisecond)
	setLimit(t, &notFoundWait, 50*time.Millisecond)

	var diag bytes.Buffer
	start := time.Now()
	events, err := ask(t, "http://model.test", 0, &diag)
	require.Error(t, err)
	assert.Equal(t, "no such host: model.test", err.Error())
	assert.Less(t, time.Since(start), time.Second, "no retries after the wait")
	waits := only[engine.Reconnecting](events)
	require.Len(t, waits, 3, "10, 20, and the last 20 ms")
	for _, w := range waits {
		assert.True(t, w.Offline)
		assert.Equal(t, 1, w.Attempt)
	}
	ended := only[engine.ReconnectEnded](events)
	require.Len(t, ended, 1)
	assert.False(t, ended[0].OK)
	assert.Contains(t, diag.String(), `"result":"failed"`)
	assert.Empty(t, srv.Requests())

	setLimit(t, &notFoundWait, time.Minute)
	go func() { time.Sleep(30 * time.Millisecond); mu.Lock(); resolves = true; mu.Unlock() }()
	events, err = ask(t, "http://model.test", 0, nil)
	require.NoError(t, err)
	assert.NotEmpty(t, only[engine.Reconnecting](events))
	assert.Len(t, srv.Requests(), 1)
}

// TestModelCall_ToolProgress: an apply_patch being written reports its
// file and size, and a progress without a tool once it is written.
func TestModelCall_ToolProgress(t *testing.T) {
	args := "*** Begin Patch\n*** Add File: a.go\n+package a\n*** End Patch\n"
	// The header ends in the second piece; the pause lets its progress out
	// before a later one replaces it in the queue.
	cut := strings.Index(args, "a.go\n") + len("a.go\n")
	srv := fakellm.New(t, fakellm.Reply{
		Calls:     []fakellm.Call{{Name: "apply_patch", Args: args, Custom: true}},
		ArgDeltas: []string{args[:20], args[20:cut], args[cut:]},
		Pace:      100 * time.Millisecond,
	})
	events, err := ask(t, srv.URL, 0, nil)
	require.NoError(t, err)
	var writing, written bool
	for _, p := range only[engine.ModelProgress](events) {
		if p.Tool == "apply_patch" && p.Target == "a.go" {
			writing = true
			assert.Positive(t, p.ToolBytes)
		}
		if writing && p.Tool == "" {
			written = true
		}
	}
	assert.True(t, writing, "the patch's file")
	assert.True(t, written, "cleared once written")
}

// TestModelCall_ToolProgressKeepsQuotes: an apply_patch's input arrives
// raw, so a quote in its file's name stays in the name.
func TestModelCall_ToolProgressKeepsQuotes(t *testing.T) {
	input := "*** Begin Patch\n*** Add File: say \"hi\".go\n+package a\n*** End Patch"
	cut := strings.Index(input, ".go\n") + len(".go\n")
	srv := fakellm.New(t, fakellm.Reply{
		Calls:     []fakellm.Call{{Name: "apply_patch", Args: input, Custom: true}},
		ArgDeltas: []string{input[:20], input[20:cut], input[cut:]},
		Pace:      100 * time.Millisecond,
	})
	events, err := ask(t, srv.URL, 0, nil)
	require.NoError(t, err)
	var targets []string
	for _, p := range only[engine.ModelProgress](events) {
		if p.Tool == "apply_patch" && p.Target != "" {
			targets = append(targets, p.Target)
			assert.Positive(t, p.ToolBytes)
		}
	}
	require.NotEmpty(t, targets)
	assert.Equal(t, `say "hi".go`, targets[len(targets)-1])
}

// TestModelTransport: both kinds of model client, keyed and codex (under
// the login's transport), have the timeouts and share an engine's
// transport; a loopback server gets no header timeout.
func TestModelTransport(t *testing.T) {
	var ts transports
	shared := ts.get(responseHeaderTimeout)
	for _, hc := range []*http.Client{remoteHTTPClient(&ts, headerTimeout("https://api.openai.com/v1")), codexHTTPClient(&ts, nil, "", nil, nil)} {
		ct, ok := hc.Transport.(callTransport)
		require.True(t, ok)
		base := reflect.ValueOf(ct.base)
		for base.Kind() == reflect.Struct { // the codex login's transport, then compression's
			base = base.FieldByName("base").Elem()
		}
		assert.Equal(t, reflect.ValueOf(shared).Pointer(), base.Pointer())
		tr := base.Elem()
		assert.Equal(t, int64(responseHeaderTimeout), tr.FieldByName("ResponseHeaderTimeout").Int())
		h2 := tr.FieldByName("HTTP2").Elem()
		assert.Equal(t, int64(30*time.Second), h2.FieldByName("SendPingTimeout").Int())
		assert.Equal(t, int64(15*time.Second), h2.FieldByName("PingTimeout").Int())
		assert.Equal(t, int64(time.Minute), h2.FieldByName("WriteByteTimeout").Int())
		assert.True(t, tr.FieldByName("DialContext").IsValid())
	}
	assert.Zero(t, headerTimeout("http://127.0.0.1:11434"))
	assert.NotSame(t, shared, ts.get(0))
	assert.NotSame(t, shared, (*transports)(nil).get(responseHeaderTimeout))
}

// TestModelCall_StreamIdleFromTheEngine: the engine's StreamIdleTimeout
// closes a silent stream, in place of the default, and the attempt's line
// records the silence after its last data line, which longest_gap_ms
// between data lines leaves out.
func TestModelCall_StreamIdleFromTheEngine(t *testing.T) {
	setLimit(t, &askStreamIdle, 200*time.Millisecond)
	srv := fakellm.New(t, fakellm.Reply{Deltas: []string{"a"}, Hold: make(chan struct{})}, fakellm.Reply{Text: "back"})
	var diag bytes.Buffer
	events, err := ask(t, srv.URL, 0, &diag)
	require.NoError(t, err)
	retries := only[engine.Reconnecting](events)
	require.NotEmpty(t, retries)
	assert.Equal(t, "no data from the model for 200ms", retries[0].Reason)
	var first attemptDiag
	require.NoError(t, json.Unmarshal(bytes.SplitN(diag.Bytes(), []byte("\n"), 2)[0], &first))
	assert.Equal(t, "retry", first.Result)
	assert.GreaterOrEqual(t, first.TrailingGapMS, int64(200), "the stall up to the timeout")
	assert.Less(t, first.TrailingGapMS, int64(1500), "not the 2 s backoff after it")
	assert.Less(t, first.LongestGapMS, int64(200), "between data lines it was short")
}
