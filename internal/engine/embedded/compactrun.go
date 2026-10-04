package embedded

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/viktordanov/uah-core/harness/llm"

	"github.com/viktordanov/uah/internal/compaction"
	"github.com/viktordanov/uah/internal/engine"
)

// run summarizes the history as the model would see it, records the
// compaction, and reports it. On failure the request goes out uncompacted.
func (c *compactor) run(ctx context.Context, job *compactionJob, req llm.Request, opts llm.RequestOptions, ask compactionAsk) {
	defer close(job.done)
	defer job.cancel()
	trigger := ask.trigger
	c.emit(engine.CompactionStarted{At: time.Now(), Trigger: trigger, Tokens: ask.used})
	rec, err := c.compact(ctx, req, opts, ask)
	interrupted := err != nil && ctx.Err() != nil
	job.interrupted = interrupted
	if err != nil {
		c.mu.Lock()
		c.job = nil
		if trigger == compaction.TriggerAuto && !interrupted {
			c.autoFailures++
		}
		if !interrupted {
			c.failed = err
		}
		c.mu.Unlock()
		if interrupted {
			err = fmt.Errorf("interrupted: %w", err)
		}
		c.emit(engine.Compacted{At: time.Now(), Trigger: trigger, Err: err.Error(), Interrupted: interrupted})

		return
	}
	warning := c.stillFull(req.Input, rec, trigger)
	c.mu.Lock()
	c.job = nil
	c.record, c.stale, c.used = &rec, false, 0
	c.autoFailures, c.failed = 0, nil
	if warning != "" {
		// Another compaction cannot shrink what stays: the system prompt,
		// the kept user messages, and a summary.
		c.autoFailures = maxAutoFailures
	}
	c.mu.Unlock()
	if c.logger != nil && rec.Stats != nil {
		c.logger.LogAttrs(ctx, slog.LevelInfo, "compacted the context",
			append([]slog.Attr{slog.String("trigger", string(trigger))}, rec.Stats.Attrs()...)...)
	}
	c.emit(engine.Compacted{At: time.Now(), Trigger: trigger, Summary: rec.Summary, Warning: warning, Stats: rec.Stats})
}

// stillFull warns when an automatic compaction left the context at or
// above the automatic limit: without the warning, and the stop that goes
// with it, every later request would compact again.
func (c *compactor) stillFull(input []llm.Item, rec compaction.Record, trigger compaction.Trigger) string {
	limit := c.autoLimit()
	if trigger != compaction.TriggerAuto || limit == 0 {
		return ""
	}
	view, err := compaction.Apply(input, rec)
	if err != nil || compaction.EstimateTokens(view) < limit {
		return ""
	}

	return fmt.Sprintf("the context is still about %d tokens after compacting, above the automatic limit of %d; "+
		"automatic compaction stops for this run (/compact still works)", compaction.EstimateTokens(view), limit)
}

func (c *compactor) compact(ctx context.Context, req llm.Request, opts llm.RequestOptions, ask compactionAsk) (compaction.Record, error) {
	start := time.Now()
	if ask.trigger == compaction.TriggerClear {
		return c.clear(req.Input, ask, start)
	}
	c.mu.Lock()
	stale := c.stale
	c.mu.Unlock()
	if stale {
		c.reportStale(compaction.ErrMismatch, true)
	}
	if c.before != nil {
		if err := c.before(ctx, ask.trigger); err != nil {
			return compaction.Record{}, fmt.Errorf("compaction was stopped: %w", err)
		}
	}
	if ask.trigger == compaction.TriggerAuto {
		if rec, ok, err := c.elide(req.Input, ask, start); ok || err != nil {
			return rec, err
		}
	}
	if c.usesRemote(ask) {
		if rec, ok, err := c.tryRemote(ctx, req, opts, ask, start); ok {
			return rec, err
		}
	}
	covered := c.coverage(req.Input)
	if covered == 0 {
		return compaction.Record{}, errNothingToCompact
	}
	summarize := c.summarize
	if summarize == nil {
		summarize = c.localSummary(req.Model.ReasoningEffort, opts.CacheKey, ask.focus)
	}
	summary, err := summarize(ctx, c.apply(req.Input[:1+covered]))
	if err != nil {
		return compaction.Record{}, err
	}
	rec, err := compaction.NewRecordCovering(req.Input, covered, summary.Text, ask.trigger, c.summaryModel(), time.Now().UTC())
	if err != nil {
		return compaction.Record{}, err
	}
	window := compaction.ContextWindow(c.next.currentModel(), c.window, c.windows)
	rec.Keep, rec.Focus = c.settings.KeepFor(window), strings.TrimSpace(ask.focus)
	c.carry(&rec, req.Input)
	rec.Ledger = compaction.Ledger(req.Input[1+rec.Floor:1+rec.Covered], rec.Focus)
	stats := c.measure(req.Input, rec, ask, start, compaction.StrategyLocal)
	stats.SummaryTokens, stats.Call = int64(compaction.ApproxTokens(summary.Text)), compaction.UsageOf(summary.Usage)
	stats.LedgerTokens, stats.Elided = int64(compaction.ApproxTokens(rec.Ledger)), len(rec.Elided)
	rec.Stats = &stats
	if err := c.log.Append(rec); err != nil {
		return compaction.Record{}, err
	}

	return rec, nil
}

// coverage is how many items a summary covers: all but the new messages
// and the last compact_keep_recent_calls tool calls, which stay verbatim;
// never less than the latest compaction covers, and everything when the
// kept calls would take more than a quarter of the window.
func (c *compactor) coverage(input []llm.Item) int {
	all := compaction.Coverable(input)
	covered := compaction.CoverableKeeping(input, c.settings.KeepCalls)
	c.mu.Lock()
	if c.record != nil && !c.stale {
		covered = max(covered, c.record.Covered)
	}
	c.mu.Unlock()
	window := compaction.ContextWindow(c.next.currentModel(), c.window, c.windows)
	if covered == 0 || compaction.EstimateTokens(input[1+covered:]) > window/4 {
		return all
	}

	return min(covered, all)
}

// elideEnough is how far under the automatic limit an elision pass must
// bring the context to replace a summary, in quarters: otherwise the next
// few requests would compact again.
const elideEnough = 3

// elide tries the cheaper step first: the old tool outputs the elision
// rules pick become stubs, on top of the latest compaction. ok is false
// when elision is off, picks nothing, or would leave the context above
// three quarters of the automatic limit; the summary runs then.
func (c *compactor) elide(input []llm.Item, ask compactionAsk, start time.Time) (compaction.Record, bool, error) {
	c.mu.Lock()
	var base compaction.Record
	if c.record != nil && !c.stale {
		base = *c.record
	}
	c.mu.Unlock()
	view, err := compaction.Apply(input, base)
	if err != nil {
		return compaction.Record{}, false, nil // the summary handles a mismatch
	}
	ids := c.settings.Elision.Elidable(view, base.Elided)
	if len(ids) == 0 {
		return compaction.Record{}, false, nil
	}
	rec := base.WithElided(ids)
	rec.Trigger, rec.At, rec.Stats = compaction.TriggerAuto, time.Now().UTC(), nil
	stats := c.measure(input, rec, ask, start, compaction.StrategyElide)
	if stats.After > c.autoLimit()*elideEnough/4 {
		return compaction.Record{}, false, nil
	}
	stats.Elided = len(rec.Elided)
	rec.Stats = &stats
	if err := c.log.Append(rec); err != nil {
		return compaction.Record{}, false, err
	}

	return rec, true, nil
}

// measure is a compaction's stats before its summary's: the context in use
// before, the estimate of the request after, the phase, and the time since
// start.
func (c *compactor) measure(input []llm.Item, rec compaction.Record, ask compactionAsk, start time.Time, strategy compaction.Strategy) compaction.Stats {
	stats := compaction.Stats{Strategy: strategy, Phase: compaction.PhaseOf(input), Before: ask.used, Duration: time.Since(start).Milliseconds()}
	if view, err := compaction.Apply(input, rec); err == nil {
		stats.After = compaction.EstimateTokens(view)
	}

	return stats
}

// clear records a /clear: every item so far is dropped from what the model
// sees, with no summary and no model call. The session file keeps them.
func (c *compactor) clear(input []llm.Item, ask compactionAsk, start time.Time) (compaction.Record, error) {
	if compaction.Coverable(input) == 0 {
		return compaction.Record{}, errNothingToCompact
	}
	rec, err := compaction.NewClear(input, time.Now().UTC())
	if err != nil {
		return compaction.Record{}, err
	}
	stats := c.measure(input, rec, ask, start, compaction.StrategyClear)
	rec.Stats = &stats
	if err := c.log.Append(rec); err != nil {
		return compaction.Record{}, err
	}

	return rec, nil
}

// summaryModel is the model that writes the summary: compact_model, or the
// session's current model, as Codex's local compaction uses.
func (c *compactor) summaryModel() string {
	if c.settings.Model != "" {
		return c.settings.Model
	}

	return c.next.currentModel()
}

// localSummary asks the summary model with the configured prompt and the
// user's focus. The effort is compact_effort, else the session's; the
// window is the summary model's.
func (c *compactor) localSummary(effort llm.ReasoningEffort, cacheKey, focus string) compaction.Summarizer {
	if c.settings.Effort != "" {
		effort = c.settings.Effort
	}
	window := compaction.ContextWindow(c.next.currentModel(), c.window, c.windows)
	if c.settings.Model != "" && c.settings.Model != c.next.currentModel() {
		window = compaction.ContextWindow(c.settings.Model, 0, c.windows)
	}

	return func(ctx context.Context, view []llm.Item) (compaction.Summary, error) {
		return compaction.Summarize(ctx, compaction.SummaryCall{ // Summarize wraps its errors
			Adapter: c.next.direct(), Model: c.settings.Model, Effort: effort, CacheKey: cacheKey,
			Window: window, Prompt: c.settings.SummaryPrompt(focus),
		}, view)
	}
}
