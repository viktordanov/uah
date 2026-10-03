package bubble

import (
	"context"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/viktordanov/uah/internal/images"
	"github.com/viktordanov/uah/internal/mcp"
	"github.com/viktordanov/uah/internal/tui/state"
)

// resourceListTimeout bounds listing the resources for the "@" menu.
const resourceListTimeout = 10 * time.Second

// runMCP runs the MCP prompt and resource effects; ok is false for any
// other effect.
func (m Model) runMCP(e state.Effect) (tea.Cmd, bool) {
	sess, ctx, store := m.sess, m.ctx, m.deps.Images
	manager := func() *mcp.Manager {
		if sess == nil {
			return nil
		}

		return sess.MCP()
	}
	switch e := e.(type) {
	case state.EffLoadMCPPrompts:
		return func() tea.Msg {
			prompts := []mcp.Prompt{}
			if mg := manager(); mg != nil {
				prompts = append(prompts, mg.Prompts()...)
			}

			return state.MCPPromptsLoaded{Prompts: prompts}
		}, true
	case state.EffLoadMCPResources:
		return func() tea.Msg {
			resources := []mcp.ResourceRef{}
			if mg := manager(); mg != nil {
				ctx, cancel := context.WithTimeout(ctx, resourceListTimeout)
				defer cancel()
				resources = append(resources, mg.Resources(ctx)...)
			}

			return state.MCPResourcesLoaded{Resources: resources}
		}, true
	case state.EffRunPrompt:
		return m.calls.next(func() tea.Msg {
			mg := manager()
			if mg == nil {
				return state.Failed{Err: errNoSession}
			}
			r, err := mg.GetPrompt(ctx, e.Prompt, e.Args)
			if err != nil {
				return state.Failed{Err: err}
			}
			var imgs []images.Image
			text := mcp.PromptText(e.Prompt.Server, r, attacher(store, &imgs))
			if strings.TrimSpace(text) == "" {
				return state.Failed{Err: errEmptyPrompt}
			}
			if _, err := sess.Submit(images.Join(text, imgs)); err != nil {
				return state.Failed{Err: err}
			}

			return nil
		}), true
	}

	return nil, false
}

// withResources reads the MCP resources a message names ("@server:uri")
// and adds them after its text, as Claude Code attaches them: a
// <resource> block each, images attached. A resource that cannot be read
// says why in its block, so the message still goes.
func withResources(ctx context.Context, mg *mcp.Manager, store *images.Store, text string) string {
	if mg == nil || !strings.Contains(text, "@") {
		return text
	}
	body, imgs := images.Split(text)
	mentions := mcp.Mentions(body, mg.Names())
	if len(mentions) == 0 {
		return text
	}
	attach := attacher(store, &imgs)
	blocks := make([]string, 0, len(mentions))
	for _, mn := range mentions {
		_, r, err := mg.ReadResource(ctx, mn.Server, mn.URI)
		blocks = append(blocks, mcp.ResourceBlock(mn.Server, mn.URI, r, err, attach))
	}

	return images.Join(strings.TrimRight(body, "\n")+"\n\n"+strings.Join(blocks, "\n"), imgs)
}

// attacher stores an MCP image for the message and labels it after the
// message's other images.
func attacher(store *images.Store, imgs *[]images.Image) mcp.Attach {
	return func(b mcp.Blob) string {
		if store == nil {
			return ""
		}
		img, err := store.Put(b.Data)
		if err != nil {
			return ""
		}
		for n := len(*imgs) + 1; ; n++ {
			label := images.Label(n)
			if !slices.ContainsFunc(*imgs, func(i images.Image) bool { return i.Label == label }) {
				img.Label = label

				break
			}
		}
		*imgs = append(*imgs, img)

		return img.Label
	}
}
