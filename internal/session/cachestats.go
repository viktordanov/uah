package session

import (
	"bufio"
	"cmp"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/viktordanov/uagent/core"
	"github.com/viktordanov/uagent/harness"

	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/usage/cachestats"
)

// CacheRequests lists the model requests of a session's runs in order, for
// the prompt cache accounting (internal/cachestats). It reads only what the
// runs recorded, so it works for any past session: the tokens and times
// from each response, the model from the run's request, and the effort
// from the request's attempt line in the run's stderr.log (with adaptive
// effort a request's own), else the run's latest settings; the history
// before the first run (IsPrior) is not uah's and is left out. A compaction
// that succeeded and a rewind mark the next request Rewritten.
func CacheRequests(runs []LoadedRun) []cachestats.Request {
	var out []cachestats.Request
	rewritten := false
	for _, run := range runs {
		if run.IsPrior() {
			continue // history from before uah ran it, such as another harness's: not uah's requests
		}
		req := run.Record.Result.Request
		effort, attempts := req.Effort, readAttempts(filepath.Join(run.Record.Dir, harness.StderrFile))
		var start time.Time
		opener := false
		for _, e := range run.Events {
			switch v := e.(type) {
			case core.ControlInput:
				if v.Mode == "settings" && v.Effort != "" {
					effort = v.Effort
				}
			case core.UserMessage:
				opener = true
			case core.TurnStarted:
				start = v.At
			case engine.Compacted:
				rewritten = rewritten || v.Err == ""
			case engine.Rewound:
				rewritten = true
			case core.ModelResponded:
				if v.Usage.InputTokens == 0 {
					continue // a failed request, not billed
				}
				from := cmp.Or(start, v.At.Add(-v.Duration))
				u := v.Usage
				out = append(out, cachestats.Request{
					Start: from, End: v.At, Model: req.Model, Effort: cmp.Or(attempts.effort(from, v.At), effort),
					Input: u.InputTokens, Cached: u.CachedInputTokens, Output: u.OutputTokens,
					Rewritten: rewritten, Opener: opener,
				})
				rewritten, opener, start = false, false, time.Time{}
			}
		}
	}

	return out
}

// CacheStats is the cache accounting of the session id in stateDir.
func CacheStats(stateDir, id string) ([]cachestats.Attributed, error) {
	runs, err := Load(stateDir, id)
	if err != nil {
		return nil, err
	}

	return cachestats.Attribute(CacheRequests(runs), cachestats.TTL), nil
}

// attempt is the part of a model_attempt line of a run's stderr.log
// (internal/engine/embedded/modelcall.go) that says a request's effort.
// With effort updates, RequestEffort is the effort the request carried,
// which keys the prompt cache, and Effort the one an update in its history
// set.
type attempt struct {
	Diag          string    `json:"diag"`
	At            time.Time `json:"at"`
	Kind          string    `json:"kind"`
	Effort        string    `json:"effort"`
	RequestEffort string    `json:"request_effort"`
	Result        string    `json:"result"`
}

type attempts []attempt

// readAttempts reads the successful turn attempts that name their effort;
// a run without the file, or from before uah logged the effort, has none.
func readAttempts(path string) attempts {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out attempts
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<16), 1<<20)
	for sc.Scan() {
		var a attempt
		if json.Unmarshal(sc.Bytes(), &a) == nil && a.Diag == "model_attempt" && a.Kind == "turn" && a.Result == "ok" && a.Effort != "" {
			out = append(out, a)
		}
	}

	return out
}

// attemptSlack is how far before the turn's record its attempt may start:
// the two are taken a few milliseconds apart.
const attemptSlack = 2 * time.Second

// effort is the effort that keys the prompt cache of the last attempt
// between start and end, "" when none: the request's, which an effort
// update leaves as it is.
func (as attempts) effort(start, end time.Time) string {
	e := ""
	for _, a := range as {
		if !a.At.Before(start.Add(-attemptSlack)) && !a.At.After(end) {
			e = cmp.Or(a.RequestEffort, a.Effort)
		}
	}

	return e
}
