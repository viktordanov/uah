package agents

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// front is the front matter of a Markdown agent file: Claude Code's keys
// (name, description, tools, model, effort) and Codex's role keys under
// their TOML names, so either kind of definition carries over.
type front struct {
	Name               string    `yaml:"name"`
	Description        string    `yaml:"description"`
	Tools              *nameList `yaml:"tools"`
	Model              string    `yaml:"model"`
	Effort             string    `yaml:"effort"`
	ReasoningEffort    string    `yaml:"model_reasoning_effort"`
	ServiceTier        tierValue `yaml:"service_tier"`
	Fast               *bool     `yaml:"fast"`
	Approve            []string  `yaml:"approve"`
	NicknameCandidates []string  `yaml:"nickname_candidates"`
}

// frontKeys are the keys front decodes; others produce a warning.
var frontKeys = []string{
	"name", "description", "tools", "model", "effort", "model_reasoning_effort",
	"service_tier", "fast", "approve", "nickname_candidates",
}

// claudeModels are Claude Code's model aliases, which name no model on
// uah's providers.
var claudeModels = []string{"sonnet", "opus", "haiku", "fable"}

// nameList is Claude Code's tools value: a comma-separated string or a
// list, in YAML or in a TOML role file.
type nameList []string

func (l *nameList) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		var s string
		if err := n.Decode(&s); err != nil {
			return err // the YAML error names the line
		}
		*l = splitNames(s)

		return nil
	}
	var list []string
	if err := n.Decode(&list); err != nil {
		return err
	}
	*l = append(nameList{}, list...)

	return nil
}

func (l *nameList) UnmarshalTOML(v any) error {
	switch v := v.(type) {
	case string:
		*l = splitNames(v)

		return nil
	case []any:
		*l = nameList{}
		for _, x := range v {
			s, ok := x.(string)
			if !ok {
				return fmt.Errorf("tools has %v; want names", x)
			}
			*l = append(*l, s)
		}

		return nil
	}

	return fmt.Errorf("tools is %v; want a comma-separated string or a list", v)
}

// splitNames splits a comma-separated list, leaving out empty names.
func splitNames(s string) nameList {
	l := nameList{}
	for part := range strings.SplitSeq(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			l = append(l, part)
		}
	}

	return l
}

// tierValue is a service_tier value, in YAML or in a TOML role file:
// Codex's string, or a bool as Claude Code's fast takes it, true for
// priority and false for default.
type tierValue string

func (t *tierValue) set(v any) error {
	switch v := v.(type) {
	case string:
		*t = tierValue(v)
	case bool:
		*t = map[bool]tierValue{true: TierPriority, false: TierDefault}[v]
	default:
		return fmt.Errorf("service_tier is %v; want a string or a bool", v)
	}

	return nil
}

func (t *tierValue) UnmarshalYAML(n *yaml.Node) error {
	var v any
	if err := n.Decode(&v); err != nil {
		return err // the YAML error names the line
	}

	return t.set(v)
}

func (t *tierValue) UnmarshalTOML(v any) error { return t.set(v) }

// readMarkdown parses and checks one Markdown agent file: YAML front
// matter between --- lines, then the instructions.
func readMarkdown(path string) (Role, []string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Role{}, nil, fmt.Errorf("failed to read %s: %w", path, err)
	}
	head, body, err := splitFront(data)
	if err != nil {
		return Role{}, nil, fmt.Errorf("%s: %w", path, err)
	}
	var node yaml.Node
	var f front
	if err := yaml.Unmarshal(head, &node); err != nil {
		return Role{}, nil, fmt.Errorf("failed to parse the front matter of %s: %w", path, err)
	}
	if err := node.Decode(&f); err != nil {
		return Role{}, nil, fmt.Errorf("failed to parse the front matter of %s: %w", path, err)
	}
	r, ignored := f.role(unknownKeys(&node))
	r.Path, r.DeveloperInstructions = path, strings.TrimSpace(string(body))
	warnings := r.finish(ignored)
	if err := r.check(); err != nil {
		return Role{}, nil, fmt.Errorf("%s: %w", path, err)
	}

	return r, warnings, nil
}

// role maps the front matter onto a Role, adding to ignored what it cannot
// use.
func (f front) role(ignored []string) (Role, []string) {
	r := Role{
		Name: f.Name, Description: f.Description, NicknameCandidates: f.NicknameCandidates,
		Model: f.Model, Effort: first(f.ReasoningEffort, f.Effort), Approve: f.Approve,
	}
	if f.Tools != nil {
		r.Tools = []string(*f.Tools)
		if r.Tools == nil {
			r.Tools = []string{}
		}
	}
	switch {
	case r.Model == "inherit":
		r.Model = ""
	case slices.Contains(claudeModels, r.Model):
		ignored = append(ignored, fmt.Sprintf("model = %q (a Claude model alias; the agent uses the parent's model)", r.Model))
		r.Model = ""
	}
	if f.ServiceTier == "" && f.Fast != nil {
		_ = f.ServiceTier.set(*f.Fast) // a bool always sets it
	}
	tier := string(f.ServiceTier)
	if t, ok := serviceTier(tier); ok {
		r.ServiceTier = t
	} else {
		ignored = append(ignored, fmt.Sprintf("service_tier = %q (want priority, fast, or default)", tier))
	}

	return r, ignored
}

// splitFront separates the front matter from the body. The file starts
// with a --- line, and the front matter ends at the next.
func splitFront(data []byte) (head, body []byte, err error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	rest, ok := cutLine(data, "---")
	if !ok {
		return nil, nil, errors.New("an agent file starts with front matter between --- lines")
	}
	for i := 0; i < len(rest); {
		end := bytes.IndexByte(rest[i:], '\n')
		line := rest[i:]
		if end >= 0 {
			line = rest[i : i+end]
		}
		if strings.TrimSpace(string(line)) == "---" {
			if end < 0 {
				return rest[:i], nil, nil
			}

			return rest[:i], rest[i+end+1:], nil
		}
		if end < 0 {
			break
		}
		i += end + 1
	}

	return nil, nil, errors.New("the front matter has no closing --- line")
}

// cutLine removes the first line of data when it is want.
func cutLine(data []byte, want string) ([]byte, bool) {
	line, rest, _ := bytes.Cut(data, []byte("\n"))
	if strings.TrimSpace(string(line)) != want {
		return nil, false
	}

	return rest, true
}

// unknownKeys are the top-level front matter keys front does not decode.
func unknownKeys(doc *yaml.Node) []string {
	if doc.Kind == yaml.DocumentNode && len(doc.Content) == 1 {
		doc = doc.Content[0]
	}
	var out []string
	if doc.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(doc.Content); i += 2 {
		if k := doc.Content[i].Value; !slices.Contains(frontKeys, k) {
			out = append(out, k)
		}
	}

	return out
}
