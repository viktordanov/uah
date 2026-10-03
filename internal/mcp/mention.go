package mcp

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/viktordanov/uah/internal/images"
)

// "@server:uri" in a message names an MCP resource, as in Claude Code: the
// composer offers the resources after "@" beside the files, and on send
// each named resource is read and added to the message. Codex has no
// resource mentions.

// Mention is one "@server:uri" in a message.
type Mention struct {
	Server, URI string
}

// mentionTrim is what can end a sentence after a mention without being
// part of the URI.
const mentionTrim = ".,;:!?)]}'\""

// Mentions finds the resources a message names, once each, in order:
// "@server:uri" words whose server is one of servers.
func Mentions(text string, servers []string) []Mention {
	var out []Mention
	for word := range strings.FieldsSeq(text) {
		rest, ok := strings.CutPrefix(word, "@")
		if !ok {
			continue
		}
		server, uri, ok := strings.Cut(rest, ":")
		uri = strings.TrimRight(uri, mentionTrim)
		if !ok || uri == "" || !slices.Contains(servers, server) {
			continue
		}
		m := Mention{Server: server, URI: uri}
		if !slices.Contains(out, m) {
			out = append(out, m)
		}
	}

	return out
}

// Blob is binary content for the message.
type Blob struct {
	MIMEType string
	Data     []byte
}

// Attach attaches an image to the message and returns its placeholder
// ("[Image #2]"), or "" when it cannot.
type Attach func(Blob) string

// ResourceBlock is a read resource as the message carries it: each
// content in a <resource> block naming the server, URI, and MIME type,
// with its text, an attached image's placeholder, or a note for other
// binary content. err, when set, is why it could not be read.
func ResourceBlock(server, uri string, r *sdk.ReadResourceResult, err error, attach Attach) string {
	if err != nil {
		return fmt.Sprintf("<resource server=%q uri=%q error=%q></resource>", server, uri, err.Error())
	}
	var parts []string
	for _, c := range r.Contents {
		if c != nil {
			parts = append(parts, contentsText(server, c, attach))
		}
	}
	if len(parts) == 0 {
		return fmt.Sprintf("<resource server=%q uri=%q></resource>", server, uri)
	}

	return strings.Join(parts, "\n")
}

// contentsText is one resource content in a block.
func contentsText(server string, c *sdk.ResourceContents, attach Attach) string {
	open := fmt.Sprintf("<resource server=%q uri=%q", server, c.URI)
	if c.MIMEType != "" {
		open += fmt.Sprintf(" mimeType=%q", c.MIMEType)
	}
	if c.Blob == nil {
		return open + ">\n" + strings.TrimSuffix(c.Text, "\n") + "\n</resource>"
	}

	return open + ">" + blobText(Blob{MIMEType: c.MIMEType, Data: c.Blob}, attach) + "</resource>"
}

// blobText is an attached image's placeholder, or what the content is.
func blobText(b Blob, attach Attach) string {
	if strings.HasPrefix(b.MIMEType, "image/") && attach != nil {
		if label := attach(b); label != "" {
			return label
		}
	}

	return fmt.Sprintf("(%d bytes of %s, not shown)", len(b.Data), cmp.Or(b.MIMEType, "binary content"))
}

// resourcesStart starts the resource blocks a message gets after its
// text when it names resources (the TUI's withResources).
const resourcesStart = "\n\n<resource server="

// ResourcesOmitted stands for a message's resource blocks in
// WithoutResources.
const ResourcesOmitted = "[MCP resources attached, not shown]"

// WithoutResources is a message without the resource blocks its mentions
// added, which an MCP server wrote and not the user, so the auto-reviewer
// does not take them for the user's words. The mentions and image tags
// stay; a text the user typed with such a block in it is cut there too.
func WithoutResources(text string) string {
	body, imgs := images.Split(text)
	words, _, ok := strings.Cut(body, resourcesStart)
	if !ok {
		return text
	}

	return images.Join(words+"\n\n"+ResourcesOmitted, imgs)
}

// PromptText turns a prompt's messages into one user message, as Claude
// Code puts a prompt's result into the conversation: the messages' text in
// order, an assistant message marked as such, embedded resources as
// resource blocks, and images attached.
func PromptText(server string, r *sdk.GetPromptResult, attach Attach) string {
	var parts []string
	for _, msg := range r.Messages {
		if msg == nil || msg.Content == nil {
			continue
		}
		var text string
		switch c := msg.Content.(type) {
		case *sdk.TextContent:
			text = c.Text
		case *sdk.ImageContent:
			text = blobText(Blob{MIMEType: c.MIMEType, Data: c.Data}, attach)
		case *sdk.AudioContent:
			text = blobText(Blob{MIMEType: c.MIMEType, Data: c.Data}, nil)
		case *sdk.ResourceLink:
			text = fmt.Sprintf("[resource %s: %s]", c.Name, c.URI)
		case *sdk.EmbeddedResource:
			if c.Resource == nil {
				continue
			}
			text = contentsText(server, c.Resource, attach)
		default:
			continue
		}
		if msg.Role == "assistant" {
			text = "Assistant: " + text
		}
		parts = append(parts, text)
	}

	return strings.Join(parts, "\n\n")
}

// SplitArgs splits a slash command's arguments at spaces, keeping what is
// in double or single quotes together, as Claude Code reads
// /mcp__jira__create_issue "Bug in login flow" high.
func SplitArgs(s string) []string {
	var (
		out    []string
		cur    strings.Builder
		quote  rune
		inWord bool
	)
	for _, r := range s {
		switch {
		case quote != 0 && r == quote:
			quote = 0
		case quote != 0:
			cur.WriteRune(r)
		case r == '"' || r == '\'':
			quote, inWord = r, true
		case r == ' ' || r == '\t' || r == '\n':
			if inWord {
				out = append(out, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	if inWord {
		out = append(out, cur.String())
	}

	return out
}
