package embedded

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/klauspost/compress/zstd"
)

// zstdTransport sends each request body compressed with zstd at level 3,
// as Codex does for the ChatGPT backend (http-client/src/request.rs,
// core/src/client.rs responses_request_compression). A long session's
// request is megabytes of JSON; requests of four real sessions (1 to 4 MB)
// came out 1.4 to 2.6 times smaller, at 150 to 300 MB/s on an M4 Max
// (BenchmarkZstd), since their images and encrypted reasoning, a quarter
// to four fifths of the bytes, are compressed already.
//
// A backend that answers 415 to a compressed body, or 400 where the same
// body uncompressed is not, cannot read it: the attempt is sent again
// uncompressed, and no later request of the process is compressed.
type zstdTransport struct{ base http.RoundTripper }

// zstdOff is set once a backend could not read a compressed body.
var zstdOff atomic.Bool

// zstdEncoder is shared: EncodeAll may be called concurrently.
var zstdEncoder = sync.OnceValue(func() *zstd.Encoder {
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		panic(err) // the options are constant
	}

	return enc
})

func (t zstdTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if zstdOff.Load() || req.Body == nil || req.Body == http.NoBody || req.GetBody == nil || req.Header.Get("Content-Encoding") != "" {
		return t.base.RoundTrip(req)
	}
	body, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("failed to read the request body: %w", err)
	}
	compressed := zstdEncoder().EncodeAll(body, make([]byte, 0, len(body)/4))
	clone := req.Clone(req.Context())
	clone.Header.Set("Content-Encoding", "zstd")
	clone.Body, clone.ContentLength = io.NopCloser(bytes.NewReader(compressed)), int64(len(compressed))
	clone.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(compressed)), nil }
	c, _ := req.Context().Value(callKey{}).(*modelCall)
	c.sent(int64(len(compressed)))
	resp, err := t.base.RoundTrip(clone)
	if err != nil || resp.StatusCode != http.StatusUnsupportedMediaType && resp.StatusCode != http.StatusBadRequest {
		return resp, err // a transport returns its base's errors unchanged
	}
	discard(resp)
	plain := req.Clone(req.Context())
	plain.Body, plain.ContentLength = io.NopCloser(bytes.NewReader(body)), int64(len(body))
	plain.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	c.sent(int64(len(body)))
	retry, err := t.base.RoundTrip(plain)
	if resp.StatusCode == http.StatusUnsupportedMediaType || err == nil && retry.StatusCode != resp.StatusCode {
		if zstdOff.CompareAndSwap(false, true) {
			c.note(compressionOff{Diag: "request_compression_off", At: time.Now(), Status: resp.StatusCode})
		}
	}

	return retry, err
}

// compressionOff is the diagnostics line written when a backend could not
// read a compressed body.
type compressionOff struct {
	Diag   string    `json:"diag"`
	At     time.Time `json:"at"`
	Status int       `json:"status"`
}

func discard(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
}

// sent records the bytes the attempt puts on the wire for its body.
func (c *modelCall) sent(n int64) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.a.SentBytes = n
}

// note writes a diagnostics line of its own.
func (c *modelCall) note(v any) {
	if c == nil || c.diag == nil {
		return
	}
	if line, err := json.Marshal(v); err == nil {
		c.mu.Lock()
		defer c.mu.Unlock()
		_, _ = c.diag.Write(append(line, '\n'))
	}
}
