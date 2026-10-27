package embedded

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/viktordanov/uah-core/harness/llm"
	"github.com/viktordanov/uah-core/harness/llm/responsesapi"

	"github.com/viktordanov/uah/testing/fakellm"
)

// The tests that compress bodies do not run in parallel: a fallback turns
// compression off for the process.

// askZstd sends one turn request with text to url through the codex
// client's compressing transport, with diagnostics to diag.
func askZstd(t *testing.T, url, text string, diag *bytes.Buffer) {
	t.Helper()
	hc := &http.Client{Transport: callTransport{base: zstdTransport{base: modelTransport(0)}}}
	ra, err := newClient(hc, ClientConfig{MaxAttempts: 1}, responsesapi.Config{
		Endpoint: url + "/responses", Headers: map[string][]string{headerContentType: {contentJSON}},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ra.Close() })
	sw, err := newSwitcher("m", variant{}, 1, func(variant) (Client, error) { return ra, nil })
	require.NoError(t, err)
	sw.diag = diag
	_, err = sw.Respond(t.Context(), llm.Request{
		Model: llm.Model{ID: "m"},
		Input: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleUser, Text: text}}},
	}, llm.RequestOptions{})
	require.NoError(t, err)
}

// diagLines decodes the diagnostics lines.
func diagLines(t *testing.T, diag *bytes.Buffer) []attemptDiag {
	t.Helper()
	var lines []attemptDiag
	for line := range strings.SplitSeq(strings.TrimSpace(diag.String()), "\n") {
		var a attemptDiag
		require.NoError(t, json.Unmarshal([]byte(line), &a))
		lines = append(lines, a)
	}

	return lines
}

// TestZstd_TheServerReadsTheBody: the body goes out compressed, the server
// reads the request from it, and the attempt's line has the bytes sent and
// the body's size.
func TestZstd_TheServerReadsTheBody(t *testing.T) {
	t.Cleanup(func() { zstdOff.Store(false) })
	srv := fakellm.New(t, fakellm.Reply{Text: "ok"})
	text := strings.Repeat("a long and repetitive history ", 2000)
	var diag bytes.Buffer
	askZstd(t, srv.URL, text, &diag)

	arrivals := srv.Arrivals()
	require.Len(t, arrivals, 1)
	assert.Equal(t, "zstd", arrivals[0].Encoding)
	assert.Equal(t, []string{text}, srv.Requests()[0].UserTexts)
	lines := diagLines(t, &diag)
	require.Len(t, lines, 1)
	assert.EqualValues(t, arrivals[0].Bytes, lines[0].SentBytes)
	assert.Greater(t, lines[0].BodyBytes, int64(len(text)))
	assert.Less(t, lines[0].SentBytes*10, lines[0].BodyBytes)
}

// TestZstd_FallsBackForTheProcess: a backend that answers 415 to a
// compressed body gets the attempt again uncompressed, and later requests
// uncompressed, with one line that says so.
func TestZstd_FallsBackForTheProcess(t *testing.T) {
	t.Cleanup(func() { zstdOff.Store(false) })
	srv := fakellm.New(t, fakellm.Reply{Text: "ok"}, fakellm.Reply{Text: "ok"})
	srv.RejectZstd = true
	var diag bytes.Buffer
	askZstd(t, srv.URL, "hi", &diag)
	askZstd(t, srv.URL, "hi", &diag)

	var encodings []string
	for _, a := range srv.Arrivals() {
		encodings = append(encodings, a.Encoding)
	}
	assert.Equal(t, []string{"", ""}, encodings, "the 415 is not a request the script answers")
	assert.Equal(t, 1, strings.Count(diag.String(), `"diag":"request_compression_off"`))
	assert.Contains(t, diag.String(), `"status":415`)
	assert.Len(t, srv.Requests(), 2)
}

// TestZstd_KeepsCompressingAfterABadRequest: a 400 that the body
// uncompressed gets too is the request's, not the compression's.
func TestZstd_KeepsCompressingAfterABadRequest(t *testing.T) {
	t.Cleanup(func() { zstdOff.Store(false) })
	srv := fakellm.New(t, fakellm.Reply{Fail: http.StatusBadRequest, FailCode: "invalid_prompt"},
		fakellm.Reply{Fail: http.StatusBadRequest, FailCode: "invalid_prompt"})
	hc := &http.Client{Transport: zstdTransport{base: modelTransport(0)}}
	resp, err := hc.Post(srv.URL+"/responses", contentJSON, strings.NewReader(`{"input":[]}`))
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.False(t, zstdOff.Load())
	require.Len(t, srv.Arrivals(), 2)
	assert.Equal(t, [2]string{"zstd", ""}, [2]string{srv.Arrivals()[0].Encoding, srv.Arrivals()[1].Encoding})
}

// BenchmarkZstd compresses request bodies as the codex client does: the
// files in UAH_BENCH_BODIES (comma-separated, such as bodies captured from
// real sessions), else a made-up one of 2 MB, half source text in tool
// results and half encrypted reasoning, which does not compress.
func BenchmarkZstd(b *testing.B) {
	bodies := map[string][]byte{}
	for p := range strings.SplitSeq(os.Getenv("UAH_BENCH_BODIES"), ",") {
		if p == "" {
			continue
		}
		body, err := os.ReadFile(p)
		require.NoError(b, err)
		bodies[filepath.Base(filepath.Dir(p))] = body
	}
	if len(bodies) == 0 {
		bodies["made-up"] = madeUpBody(2 << 20)
	}
	for name, body := range bodies {
		b.Run(name, func(b *testing.B) {
			b.SetBytes(int64(len(body)))
			var n int
			for b.Loop() {
				n = len(zstdEncoder().EncodeAll(body, make([]byte, 0, len(body)/4)))
			}
			b.ReportMetric(float64(len(body))/float64(n), "ratio")
		})
	}
}

// madeUpBody is a request body of about size bytes: tool results of Go
// source and reasoning items of random base64, in equal parts.
func madeUpBody(size int) []byte {
	src, err := os.ReadFile("transport.go")
	if err != nil {
		panic(err)
	}
	rnd := rand.New(rand.NewPCG(1, 2))
	blob := make([]byte, 2<<10)
	var buf bytes.Buffer
	buf.WriteString(`{"model":"m","input":[`)
	for i := 0; buf.Len() < size; i++ {
		for j := range blob {
			blob[j] = byte(rnd.Uint32())
		}
		start := i * 97 % (len(src) - 3<<10)
		text, _ := json.Marshal(string(src[start : start+3<<10]))
		fmt.Fprintf(&buf, `{"type":"reasoning","id":"rs_%d","summary":[],"encrypted_content":"%s"},`, i, base64.StdEncoding.EncodeToString(blob))
		fmt.Fprintf(&buf, `{"type":"function_call_output","call_id":"call_%d","output":[{"type":"input_text","text":%s}]},`, i, text)
	}
	buf.Truncate(buf.Len() - 1)
	buf.WriteString(`]}`)

	return buf.Bytes()
}
