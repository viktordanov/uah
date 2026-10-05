package state

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/viktordanov/uah/internal/approval"
	"github.com/viktordanov/uah/internal/session"
)

// ConfigValue is one key's effective value and where it came from, as
// `uah config` reports them.
type ConfigValue struct {
	Value  string
	Source string
}

// Sources /config tells apart; SourceUser is where it saves.
const (
	SourceUser    = "user file"
	sourceDefault = "default"
	sourceSession = "session"
)

// The keys /config changes.
const (
	keyAutoCompact  = "auto_compact_percent"
	keyTokenLimit   = "model_auto_compact_token_limit"
	keyCompactModel = "compact_model"
	keyModel        = "model"
	keyEffort       = "effort"
	keyFast         = "fast"
	keyMode         = "permission_mode"
	keyDetails      = "tui.details"
	keyMouse        = "tui.mouse"
	keyFileLinks    = "tui.file_links"
	keyWebSearch    = "web_search"
	keyAdaptive     = "adaptive_effort"
)

// rowKind is how a /config row changes.
type rowKind int

const (
	rowToggle rowKind = iota // on and off
	rowChoice                // cycles through Choices
	rowNumber                // typed digits; empty or 0 removes the key
	rowModel                 // cycles through the model catalog, or typed
)

// ConfigRow is one line of the /config panel.
type ConfigRow struct {
	Key    string
	Label  string
	Value  string // as shown
	Source string
	// Help says what the setting does, under the panel while it is
	// selected ("" for none).
	Help string
	kind rowKind
}

// Toggle reports whether the row switches on and off.
func (r ConfigRow) Toggle() bool { return r.kind == rowToggle }

// configKeys are the /config rows, in order.
var configKeys = []struct {
	key, label string
	kind       rowKind
	help       string
}{
	{keyAutoCompact, "Auto-compact", rowChoice, ""},
	{keyTokenLimit, "Auto-compact token limit", rowNumber, ""},
	{keyCompactModel, "Compaction model", rowModel, ""},
	{keyModel, "Model", rowModel, ""},
	{keyEffort, "Effort", rowChoice, ""},
	{keyFast, "Fast mode", rowToggle, ""},
	{keyAdaptive, "Adaptive effort", rowChoice, adaptiveHelp},
	{keyMode, "Permission mode", rowChoice, ""},
	{keyWebSearch, "Web search", rowChoice, ""},
	{keyDetails, "Details view", rowToggle, ""},
	{keyMouse, "Mouse", rowToggle, ""},
	{keyFileLinks, "File links", rowChoice, fileLinksHelp},
}

// autoPercents are the auto-compact choices; 0 is off.
var autoPercents = []int{0, 50, 60, 70, 80, 85, 90, 95}

// cycledModes are the permission modes /config steps through, as
// shift+tab does without --yolo; yolo is never saved.
var cycledModes = []string{string(approval.ModeReadOnly), string(approval.ModeWorkspace), string(approval.ModeAuto)}

// webSearchModes are web_search's values (docs/design/web-search.md).
var webSearchModes = []string{"live", "disabled"}

// shownOff is how the panel shows a setting that is off.
const shownOff = "off"

// adaptiveHelp says what adaptive effort does.
const adaptiveHelp = "Adaptive effort: think one or two effort levels less on follow-up turns after tool results, and start a new session with the workspace's context"

// fileLinksHelp says what a click on a file path does.
const fileLinksHelp = "File links: a click on a file path peeks at it in an overlay, opens it in $VISUAL or $EDITOR at its line, or with the system's default app; off draws paths as plain text"

// sessionModel is the compaction model's choice for "the session's model".
const sessionModel = "session model"

// ConfigRows are the panel's rows with their values.
func (s State) ConfigRows() []ConfigRow {
	if s.Config == nil {
		return nil
	}
	rows := make([]ConfigRow, 0, len(configKeys))
	for _, k := range configKeys {
		v := s.Config.Values[k.key]
		rows = append(rows, ConfigRow{Key: k.key, Label: k.label, Value: shown(k.key, v), Source: v.Source, Help: k.help, kind: k.kind})
	}

	return rows
}

// shown is how the panel shows a value.
func shown(key string, v ConfigValue) string {
	switch {
	case v.Value == "":
		return "…"
	case key == keyAutoCompact && v.Value == "0":
		return shownOff
	case key == keyAutoCompact:
		return "on at " + v.Value + "%"
	case key == keyTokenLimit && v.Value == "0":
		return "none"
	case key == keyCompactModel && v.Source == sourceDefault:
		return sessionModel + " (" + v.Value + ")"
	case key == keyAdaptive:
		return strings.Replace(v.Value, "-", " ", 1)
	case v.Value == "true":
		return "on"
	case v.Value == "false":
		return shownOff
	}

	return v.Value
}

// next is the value a change by delta (+1 or -1) saves for the row; ok is
// false when the row needs typing instead. A nil value removes the key.
func (s State) next(row ConfigRow, delta int) (value any, ok bool) {
	current := s.Config.Values[row.Key].Value
	switch row.kind {
	case rowToggle:
		return current != "true", true
	case rowNumber:
		return nil, false
	case rowModel:
		return s.nextModel(row, current, delta)
	}
	switch row.Key {
	case keyEffort:
		return cycle(session.Efforts, current, delta), true
	case keyMode:
		return cycle(cycledModes, current, delta), true
	case keyWebSearch:
		return cycle(webSearchModes, current, delta), true
	case keyAdaptive:
		return cycle(session.AdaptiveEfforts, current, delta), true
	case keyFileLinks:
		return cycle(LinksModes, current, delta), true
	}
	percent, _ := strconv.Atoi(current)
	i := slices.Index(autoPercents, percent)
	if i < 0 {
		i = slices.Index(autoPercents, 90)
	}

	return autoPercents[((i+delta)%len(autoPercents)+len(autoPercents))%len(autoPercents)], true
}

// nextModel cycles through the provider's visible models; the compaction
// model also offers the session's. Without a model list, it is typed.
func (s State) nextModel(row ConfigRow, current string, delta int) (any, bool) {
	c, ok := s.catalog()
	if !ok || len(c.Visible()) == 0 {
		return nil, false
	}
	var choices []string
	if row.Key == keyCompactModel {
		choices = append(choices, sessionModel)
		if row.Source == sourceDefault {
			current = sessionModel
		}
	}
	for _, m := range c.Visible() {
		choices = append(choices, m.ID)
	}
	picked := cycle(choices, current, delta)
	if picked == sessionModel {
		return nil, true
	}

	return picked, true
}

// cycle is the choice delta steps from current, wrapping; an unknown
// current starts before the first.
func cycle(choices []string, current string, delta int) string {
	i := slices.Index(choices, current)
	if i < 0 {
		i = -1
		if delta < 0 {
			i = 0
		}
	}
	n := len(choices)

	return choices[((i+delta)%n+n)%n]
}

// typed checks a value typed into a row: digits for a number (empty or 0
// removes the key), a model name otherwise.
func typed(row ConfigRow, text string) (any, error) {
	text = strings.TrimSpace(text)
	if row.kind == rowNumber {
		if text == "" || text == "0" {
			return nil, nil //nolint:nilnil // nil removes the key
		}
		n, err := strconv.ParseInt(text, 10, 64)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("%s wants a number of tokens, not %q", row.Label, text)
		}

		return n, nil
	}
	if text == "" || text == sessionModel {
		if row.Key == keyModel {
			return nil, fmt.Errorf("%s needs a model name", row.Label)
		}

		return nil, nil //nolint:nilnil // nil removes the key
	}
	check := session.Settings{Provider: session.Providers[0], Model: text, Workspace: "."}
	if err := check.Validate(); err != nil {
		return nil, err // the message is for the user as is
	}

	return text, nil
}

// valueText is a saved value as `uah config` shows it.
func valueText(value any) string {
	if value == nil {
		return ""
	}

	return fmt.Sprint(value)
}
