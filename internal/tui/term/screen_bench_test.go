package term

import (
	"bytes"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
)

// What a frame of the streamed answer costs to put on the terminal, with
// each optimization on and off, and with ultraviolet's cell renderer as
// Bubble Tea's flush drives it, for reference.

func BenchmarkScreenFrame(b *testing.B) {
	frames := streamFrames(400)
	for _, v := range []variant{{}, {noScroll: true}, {noSpans: true}, {noScroll: true, noSpans: true}} {
		b.Run(strings.TrimSuffix(v.String(), ",sync=false"), func(b *testing.B) {
			b.ReportAllocs()
			bytesOut, n := 0, 0
			for b.Loop() {
				s := v.screen(fw, fh)
				for _, ls := range frames {
					bytesOut += len(s.Frame(ls, streamCursor))
					n++
				}
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(n), "ns/frame")
			b.ReportMetric(float64(bytesOut)/float64(n), "B/frame")
		})
	}
	b.Run("ultraviolet", func(b *testing.B) {
		joined := make([]string, len(frames))
		for i, ls := range frames {
			joined[i] = strings.Join(ls, "\n")
		}
		b.ReportAllocs()
		bytesOut, n := 0, 0
		for b.Loop() {
			u := newUVFlush()
			for _, fr := range joined {
				bytesOut += u.frame(fr)
				n++
			}
		}
		b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(n), "ns/frame")
		b.ReportMetric(float64(bytesOut)/float64(n), "B/frame")
	})
}

// uvFlush is cursedRenderer.flush's work for one frame in the alt screen.
type uvFlush struct {
	buf uv.ScreenBuffer
	scr *uv.TerminalRenderer
	out bytes.Buffer
}

func newUVFlush() *uvFlush {
	u := &uvFlush{buf: uv.NewScreenBuffer(fw, fh)}
	u.scr = uv.NewTerminalRenderer(&u.out, []string{"TERM=xterm-256color", "COLORTERM=truecolor"})
	u.scr.SetFullscreen(true)
	u.scr.SetRelativeCursor(false)
	u.scr.SetTabStops(-1)
	u.scr.SetScrollOptim(true)
	u.scr.Resize(fw, fh)
	u.scr.Erase()

	return u
}

func (u *uvFlush) frame(content string) int {
	u.buf.Clear()
	uv.NewStyledString(content).Draw(u.buf, u.buf.Bounds())
	u.scr.Render(u.buf.RenderBuffer)
	u.scr.MoveTo(streamCursor.X, streamCursor.Y)
	_ = u.scr.Flush()
	n := u.out.Len()
	u.out.Reset()

	return n
}
