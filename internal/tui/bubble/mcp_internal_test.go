package bubble

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/images"
	"github.com/viktordanov/uah/internal/mcp"
	"github.com/viktordanov/uah/testing/harnesstest"
)

// A message that names MCP resources ("@server:uri") gets each one after
// its text, images attached after the images it already had; one that
// cannot be read says why, and a message without mentions is unchanged.
func TestWithResources(t *testing.T) {
	m, err := mcp.NewManager(map[string]mcp.ServerConfig{"docs": {Command: harnesstest.MCPServer(t)}}, mcp.Options{Workspace: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.Close() })
	_, err = m.Tools(context.Background())
	require.NoError(t, err)
	store := &images.Store{Dir: t.TempDir()}
	_, pixel, err := m.ReadResource(context.Background(), "docs", "test://pixel")
	require.NoError(t, err)
	pasted, err := store.Put(pixel.Contents[0].Blob)
	require.NoError(t, err)
	pasted.Label = images.Label(1)

	text := images.Join("look at @docs:test://greeting and @docs:test://pixel, [Image #1] and @docs:test://missing", []images.Image{pasted})
	got := withResources(context.Background(), m, store, text)
	body, imgs := images.Split(got)
	require.Len(t, imgs, 2)
	assert.Equal(t, "[Image #2]", imgs[1].Label)
	assert.True(t, strings.HasPrefix(body, "look at @docs:test://greeting and @docs:test://pixel, [Image #1] and @docs:test://missing\n\n"+
		"<resource server=\"docs\" uri=\"test://greeting\" mimeType=\"text/plain\">\nhello from the resource\n</resource>\n"+
		"<resource server=\"docs\" uri=\"test://pixel\" mimeType=\"image/png\">[Image #2]</resource>\n"+
		"<resource server=\"docs\" uri=\"test://missing\" error=\"resources/read failed:"), body)

	assert.Equal(t, "mail me @ home or @someone:x", withResources(context.Background(), m, store, "mail me @ home or @someone:x"))
	assert.Equal(t, "@docs:test://greeting", withResources(context.Background(), nil, store, "@docs:test://greeting"))
}
