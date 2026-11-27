package session_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/session"
)

// TestOpen_NoticesThenInfos: a session opens with its notices as warnings,
// then its infos as information.
func TestOpen_NoticesThenInfos(t *testing.T) {
	eng := newFakeEngine(fakeCaps{})
	s, err := session.Open(context.Background(), eng, session.Options{
		Settings: settings(), Notices: []string{"a warning"}, Infos: []string{"a new setting"},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	h := &harness{t: t, eng: eng, s: s}

	w := h.until(isType[session.Notice]).(session.Notice)
	assert.Equal(t, session.LevelWarning, w.Level)
	assert.Equal(t, "a warning", w.Message)
	i := h.until(isType[session.Notice]).(session.Notice)
	assert.Equal(t, session.LevelInfo, i.Level)
	assert.Equal(t, "a new setting", i.Message)
}

// TestOpen_ManyInfos: Open does not wait for a reader to take its notices,
// however many there are.
func TestOpen_ManyInfos(t *testing.T) {
	infos := make([]string, 5000)
	done := make(chan *session.Session, 1)
	go func() {
		s, err := session.Open(context.Background(), newFakeEngine(fakeCaps{}), session.Options{Settings: settings(), Infos: infos})
		assert.NoError(t, err)
		done <- s
	}()
	select {
	case s := <-done:
		t.Cleanup(func() { _ = s.Close() })
	case <-time.After(5 * time.Second):
		t.Fatal("Open blocked on its notices")
	}
}
