package main

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/viktordanov/uagent/core"
	"github.com/viktordanov/uagent/stream"

	"github.com/viktordanov/uah/internal/compaction"
	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/session"
)

// jsonlWriter writes run events in uagent's stream schema and session events
// in the same shape: {"v":1,"type":...,"at":...,...}.
type jsonlWriter struct {
	enc *json.Encoder
	err error
}

func newJSONLWriter(w io.Writer) *jsonlWriter {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)

	return &jsonlWriter{enc: enc}
}

func (j *jsonlWriter) write(event core.Event) {
	if j.err != nil {
		return
	}
	dto, ok := stream.EventToDTO(event)
	if !ok {
		dto, ok = sessionEventDTO(event)
	}
	if !ok {
		dto, ok = engineEventDTO(event)
	}
	if !ok {
		return
	}
	if err := j.enc.Encode(dto); err != nil {
		j.err = fmt.Errorf("failed to write event: %w", err)
	}
}

type sessionHeader struct {
	V    int       `json:"v"`
	Type string    `json:"type"`
	At   time.Time `json:"at"`
}

func header(eventType string, at time.Time) sessionHeader {
	return sessionHeader{V: stream.SchemaVersion, Type: eventType, At: at.UTC()}
}

type settingsDTO struct {
	Provider    string `json:"provider"`
	Model       string `json:"model"`
	Effort      string `json:"effort"`
	ServiceTier string `json:"service_tier,omitempty"`
	Workspace   string `json:"workspace"`
}

func toSettingsDTO(s session.Settings) settingsDTO {
	return settingsDTO{Provider: s.Provider, Model: s.Model, Effort: s.Effort, ServiceTier: s.ServiceTier, Workspace: s.Workspace}
}

func sessionEventDTO(event core.Event) (any, bool) {
	switch e := event.(type) {
	case session.SessionOpened:
		return struct {
			sessionHeader

			ID       string      `json:"id"`
			Resumed  bool        `json:"resumed"`
			Engine   string      `json:"engine"`
			Settings settingsDTO `json:"settings"`
		}{header("session_opened", e.At), e.ID, e.Resumed, e.Engine, toSettingsDTO(e.Settings)}, true
	case session.InstructionsLoaded:
		return struct {
			sessionHeader

			Files     []string `json:"files"`
			Bytes     int      `json:"bytes"`
			Truncated bool     `json:"truncated"`
		}{header("instructions_loaded", e.At), e.Files, e.Bytes, e.Truncated}, true
	case session.InputQueued:
		return struct {
			sessionHeader

			ID   string `json:"id"`
			Text string `json:"text"`
		}{header("input_queued", e.At), e.Input.ID, e.Input.Text}, true
	case session.InputSent:
		return struct {
			sessionHeader

			IDs []string `json:"ids"`
		}{header("input_sent", e.At), e.IDs}, true
	case session.InputDelivered:
		return struct {
			sessionHeader

			ID string `json:"id"`
		}{header("input_delivered", e.At), e.ID}, true
	case session.InputFailed:
		return struct {
			sessionHeader

			IDs    []string `json:"ids"`
			Reason string   `json:"reason"`
		}{header("input_failed", e.At), e.IDs, e.Reason}, true
	case session.InputWithdrawn:
		return struct {
			sessionHeader

			ID string `json:"id"`
		}{header("input_withdrawn", e.At), e.ID}, true
	case session.SettingsChanged:
		return struct {
			sessionHeader

			Settings settingsDTO `json:"settings"`
			Applied  string      `json:"applied"`
		}{header("settings_changed", e.At), toSettingsDTO(e.Settings), string(e.Applied)}, true
	case session.Idle:
		return header("idle", e.At), true
	case session.Notice:
		return struct {
			sessionHeader

			Level   string `json:"level"`
			Message string `json:"message"`
		}{header("notice", e.At), e.Level, e.Message}, true
	case engine.Reconnecting:
		return struct {
			sessionHeader

			Attempt     int    `json:"attempt"`
			MaxAttempts int    `json:"max_attempts"`
			DelayMS     int64  `json:"delay_ms"`
			Reason      string `json:"reason"`
			Offline     bool   `json:"offline,omitempty"`
		}{header("reconnecting", e.At), e.Attempt, e.MaxAttempts, e.Delay.Milliseconds(), e.Reason, e.Offline}, true
	case engine.ReconnectEnded:
		return struct {
			sessionHeader

			OK bool `json:"ok"`
		}{header("reconnect_ended", e.At), e.OK}, true
	case engine.EffortUpdatesOff:
		return struct {
			sessionHeader

			Message string `json:"message"`
			Error   string `json:"error"`
		}{header("effort_updates_off", e.At), e.Text(), e.Err}, true
	}

	return nil, false
}

// engineEventDTO is the stream shape of the embedded engine's own events.
func engineEventDTO(event core.Event) (any, bool) {
	switch e := event.(type) {
	case engine.CompactionStarted:
		return struct {
			sessionHeader

			Trigger string `json:"trigger"`
			Tokens  int64  `json:"tokens"`
		}{header("compaction_started", e.At), string(e.Trigger), e.Tokens}, true
	case engine.Compacted:
		return struct {
			sessionHeader

			Trigger     string            `json:"trigger"`
			Summary     string            `json:"summary,omitempty"`
			Error       string            `json:"error,omitempty"`
			Interrupted bool              `json:"interrupted,omitempty"`
			Warning     string            `json:"warning,omitempty"`
			Stats       *compaction.Stats `json:"stats,omitempty"`
		}{header("compacted", e.At), string(e.Trigger), e.Summary, e.Err, e.Interrupted, e.Warning, e.Stats}, true
	case engine.TextDelta:
		return struct {
			sessionHeader

			ItemID string `json:"item_id"`
			Text   string `json:"text"`
			Final  bool   `json:"final,omitempty"`
		}{header("text_delta", e.At), e.ItemID, e.Text, e.Final}, true
	case engine.ReasoningDelta:
		return struct {
			sessionHeader

			ItemID string `json:"item_id"`
			Part   int    `json:"part"`
			Text   string `json:"text"`
		}{header("reasoning_delta", e.At), e.ItemID, e.Part, e.Text}, true
	case engine.StreamReset:
		return header("stream_reset", e.At), true
	case engine.WebSearch:
		return struct {
			sessionHeader

			ItemID  string `json:"item_id"`
			Done    bool   `json:"done"`
			Action  string `json:"action,omitempty"`
			Query   string `json:"query,omitempty"`
			URL     string `json:"url,omitempty"`
			Pattern string `json:"pattern,omitempty"`
		}{header("web_search", e.At), e.ItemID, e.Done, e.Action, e.Query, e.URL, e.Pattern}, true
	case engine.Rewound:
		return struct {
			sessionHeader

			MessageID string `json:"message_id"`
			Tokens    int64  `json:"tokens,omitempty"`
		}{header("rewound", e.At), e.MessageID, e.Tokens}, true
	case engine.AutoReviewed:
		return struct {
			sessionHeader

			Command           string `json:"command"`
			Outcome           string `json:"outcome"`
			Risk              string `json:"risk,omitempty"`
			Reason            string `json:"reason,omitempty"`
			DurationMS        int64  `json:"duration_ms"`
			InputTokens       int64  `json:"input_tokens"`
			CachedInputTokens int64  `json:"cached_input_tokens"`
			OutputTokens      int64  `json:"output_tokens"`
		}{
			header("auto_reviewed", e.At), e.Command, e.Outcome, e.Risk, e.Reason, e.Duration.Milliseconds(),
			e.InputTokens, e.CachedInputTokens, e.OutputTokens,
		}, true
	}

	return nil, false
}
