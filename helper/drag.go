// SUPER+left drag moves and SUPER+right drag resizes the window under the
// cursor, like Hyprland's bindm. Plain title bar / border drags are cancelled.
//
//   - Move, and resize of floating windows: the window follows the mouse and
//     GlazeWM gets the same MOVESIZESTART/END events as for a title bar drag,
//     so a dropped tiling window is re-inserted where the cursor is.
//   - Resize of tiling windows: GlazeWM "resize" commands over its IPC, so the
//     split with the neighbour moves live and an axis without a neighbour
//     (e.g. height with windows only side by side) does nothing.
package main

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

const (
	whMouseLL     = 14
	wmMouseMove   = 0x0200
	wmLButtonDown = 0x0201
	wmLButtonUp   = 0x0202
	wmRButtonDown = 0x0204
	wmRButtonUp   = 0x0205
)

type msllhookstruct struct {
	x, y                   int32
	mouseData, flags, time uint32
	extra                  uintptr
}

type rect struct{ l, t, r, b int32 }

var (
	fromPoint   = user32.NewProc("WindowFromPoint")
	ancestor    = user32.NewProc("GetAncestor")
	windowRect  = user32.NewProc("GetWindowRect")
	setPos      = user32.NewProc("SetWindowPos")
	notify      = user32.NewProc("NotifyWinEvent")
	monitorOf   = user32.NewProc("MonitorFromWindow")
	monitorInfo = user32.NewProc("GetMonitorInfoW")

	// hook thread only
	drag   uintptr
	dragUp uintptr // button-up message that ends the drag

	// The hook only records the newest cursor position; dragger() applies it.
	// Moving the window from the hook on every mouse event (~1000/s) flooded
	// the app and GlazeWM, and a slow hook stalls the cursor system-wide.
	jobMu   sync.Mutex
	job     dragJob
	jobWake = make(chan struct{}, 1)

	draggerThread atomic.Uint32
	dragging      atomic.Bool
	notifyMsg     = user32.NewProc("SendNotifyMessageW")
)

// onMoveSizeStart cancels title bar / border drags: like Hyprland, windows
// only move and resize with SUPER held. Our own drags come from dragger().
func onMoveSizeStart(_, _, hwnd, _, _, thread, _ uintptr) uintptr {
	if uint32(thread) != draggerThread.Load() {
		notifyMsg.Call(hwnd, 0x001F, 0, 0) // WM_CANCELMODE ends the move/size loop; never waits on the app
	}
	return 0
}

type dragJob struct {
	win               uintptr
	resize            bool
	start, pt         [2]int32
	begin, dirty, end bool
}

func post(f func(j *dragJob)) {
	jobMu.Lock()
	f(&job)
	jobMu.Unlock()
	select {
	case jobWake <- struct{}{}:
	default:
	}
}

// dragTarget returns the top-level window under the cursor, or 0 for the
// desktop, taskbar and Zebar widgets.
func dragTarget(x, y int32) uintptr {
	h, _, _ := fromPoint.Call(uintptr(uint32(x)) | uintptr(uint32(y))<<32)
	h, _, _ = ancestor.Call(h, 2) // GA_ROOT
	if h == 0 {
		return 0
	}
	switch className(h) {
	case "Progman", "WorkerW", "Shell_TrayWnd", "Shell_SecondaryTrayWnd":
		return 0
	}
	if strings.HasPrefix(title(h), "Zebar") {
		return 0
	}
	return h
}

func mouseHook(code int, wparam uintptr, lparam uintptr) uintptr {
	if code == 0 {
		m := (*msllhookstruct)(unsafe.Pointer(lparam))
		switch {
		case drag != 0 && wparam == dragUp:
			drag = 0
			post(func(j *dragJob) { j.end = true })
			return 1
		case drag != 0 && wparam == wmMouseMove:
			post(func(j *dragJob) { j.pt, j.dirty = [2]int32{m.x, m.y}, true })
		case drag == 0 && winDown && (wparam == wmLButtonDown || wparam == wmRButtonDown):
			h := dragTarget(m.x, m.y)
			if h == 0 {
				break
			}
			drag, dragUp = h, wmLButtonUp
			if wparam == wmRButtonDown {
				dragUp = wmRButtonUp
			}
			resize := wparam == wmRButtonDown
			post(func(j *dragJob) {
				*j = dragJob{win: h, resize: resize, start: [2]int32{m.x, m.y}, pt: [2]int32{m.x, m.y}, begin: true}
			})
			return 1
		}
	}
	r, _, _ := callNext.Call(0, uintptr(code), wparam, lparam)
	return r
}

// dragger applies the newest cursor position at most every 5ms.
func dragger() {
	runtime.LockOSThread()
	t, _, _ := curThread.Call()
	draggerThread.Store(uint32(t))
	var (
		d       dragJob
		r0      rect
		sx, sy  int32    // resize: -1 moves the left/top edge, 1 the right/bottom, 0 = move
		ipc     *glaze   // GlazeWM connection, kept open: dialing costs 10-35ms a drag
		id      string   // set while resizing a tiling window: its GlazeWM container id
		splits  []*split // tiling resize drawn by us (preview), see split
		preview bool
		gh      *ghost   // preview drawn on the overlay, else the windows move themselves
		sent    [2]int32 // live tiling resize: delta already sent
	)
	// preview without the overlay: SWP_ASYNCWINDOWPOS, a slow app repaints late but never holds up the drag
	moveWin := func(w edgeWin, r rect) {
		setPos.Call(w.h, 0, uintptr(r.l), uintptr(r.t), uintptr(r.r-r.l), uintptr(r.b-r.t), 0x4000|0x10|0x4) // ASYNCWINDOWPOS|NOACTIVATE|NOZORDER
	}
	for range jobWake {
		jobMu.Lock()
		j := job
		job.begin, job.dirty, job.end = false, false, false
		jobMu.Unlock()

		if j.begin {
			d, sx, sy, sent, id = j, 0, 0, [2]int32{}, ""
			dragging.Store(true)
			windowRect.Call(d.win, uintptr(unsafe.Pointer(&r0)))
			if d.resize {
				for try := 0; try < 2; try++ { // a second time after GlazeWM restarted
					if ipc == nil {
						if ipc, _ = dialGlaze(); ipc == nil {
							break
						}
					}
					var err error
					if id, splits, preview, err = tilingWindow(ipc, d.win); err == nil {
						break
					}
					ipc.c.Close()
					ipc = nil
				}
				sx, sy = resizeEdges(d.win, r0, d.start, id != "")
				if id != "" && preview {
					gh = newGhost(splits)
				}
			}
			if id == "" {
				notify.Call(0x000A, d.win, 0, 0) // EVENT_SYSTEM_MOVESIZESTART
			}
		}
		if j.dirty && d.win != 0 {
			dx, dy := j.pt[0]-d.start[0], j.pt[1]-d.start[1]
			if id != "" && preview {
				f := moveWin
				if gh != nil {
					f = gh.show
				}
				for _, s := range splits {
					if s.vertical {
						s.move(dy, f)
					} else {
						s.move(dx, f)
					}
				}
			} else if id != "" {
				w, h := dx*sx-sent[0], dy*sy-sent[1]
				cmd := "command --id " + id + " resize"
				if w != 0 {
					cmd += fmt.Sprintf(" --width %+dpx", w)
				}
				if h != 0 {
					cmd += fmt.Sprintf(" --height %+dpx", h)
				}
				// waits for GlazeWM's reply, so it is never flooded
				if (w != 0 || h != 0) && ipc.ask(cmd) == nil {
					sent = [2]int32{sent[0] + w, sent[1] + h}
				}
			} else {
				r := r0
				flags := uintptr(0x4 | 0x10) // SWP_NOZORDER|SWP_NOACTIVATE
				if !d.resize {
					r = rect{r.l + dx, r.t + dy, r.r + dx, r.b + dy}
					flags |= 0x1 // SWP_NOSIZE
				} else {
					// ponytail: no min size, apps clamp it via WM_GETMINMAXINFO
					if sx < 0 {
						r.l += dx
					} else {
						r.r += dx
					}
					if sy < 0 {
						r.t += dy
					} else {
						r.b += dy
					}
				}
				setPos.Call(d.win, 0, uintptr(r.l), uintptr(r.t), uintptr(r.r-r.l), uintptr(r.b-r.t), flags)
			}
		}
		if j.end && d.win != 0 {
			if id != "" && preview {
				cmd := ""
				for _, s := range splits {
					if s.moved != 0 {
						cmd += fmt.Sprintf(" --%s %+dpx", map[bool]string{false: "width", true: "height"}[s.vertical], s.grow*s.moved)
					}
				}
				if cmd != "" && ipc != nil {
					ipc.ask("command --id " + id + " resize" + cmd) // GlazeWM lays out what the preview showed
				}
				if gh != nil {
					gh.done()
					gh = nil
				}
			} else if id == "" {
				notify.Call(0x000B, d.win, 0, 0) // EVENT_SYSTEM_MOVESIZEEND, after the last frame
			}
			d.win = 0
			dragging.Store(false)
		}
		if !preview || id == "" {
			time.Sleep(5 * time.Millisecond) // live: don't flood the app or GlazeWM
		}
	}
}

// resizeEdges picks the edges to drag. Floating: the corner nearest the cursor.
// Tiling: the side facing a neighbour, i.e. away from the monitor edge.
func resizeEdges(h uintptr, r rect, pt [2]int32, tiling bool) (sx, sy int32) {
	sx, sy = 1, 1
	if pt[0] < (r.l+r.r)/2 {
		sx = -1
	}
	if pt[1] < (r.t+r.b)/2 {
		sy = -1
	}
	if !tiling {
		return
	}
	m, _, _ := monitorOf.Call(h, 2) // MONITOR_DEFAULTTONEAREST
	_, work := monitorRects(m)
	side := func(before, after, cursor int32) int32 {
		// ponytail: 40px tells "gap to a neighbour" from "outer gap"; fine for gaps < 40px
		switch {
		case before > after+40:
			return -1
		case after > before+40:
			return 1
		}
		return cursor
	}
	return side(r.l-work.l, work.r-r.r, sx), side(r.t-work.t, work.b-r.b, sy)
}

// monitorRects returns a monitor's full and work area rects.
func monitorRects(m uintptr) (mon, work rect) {
	var mi struct {
		size      uint32
		mon, work rect
		flags     uint32
	}
	mi.size = uint32(unsafe.Sizeof(mi))
	monitorInfo.Call(m, uintptr(unsafe.Pointer(&mi)))
	return mi.mon, mi.work
}

// node is a container in GlazeWM's "query workspaces" tree.
type node struct {
	ID              string
	Type            string // workspace, split, window
	TilingDirection string
	Handle          uintptr
	State           struct{ Type string }
	Children        []node
}

// path is the containers from n down to window hwnd, or nil.
func (n *node) path(hwnd uintptr) []*node {
	if n.Type == "window" && n.Handle == hwnd {
		return []*node{n}
	}
	for i := range n.Children {
		if p := n.Children[i].path(hwnd); p != nil {
			return append([]*node{n}, p...)
		}
	}
	return nil
}

func (n *node) tiling() bool {
	return n.Type == "split" || n.Type == "window" && n.State.Type == "tiling"
}

// windows are the tiling windows under n, with their rects now.
func (n *node) windows() (out []edgeWin) {
	if n.Type == "window" && n.tiling() {
		w := edgeWin{h: n.Handle}
		windowRect.Call(n.Handle, uintptr(unsafe.Pointer(&w.r)))
		dwmAttr.Call(n.Handle, dwmBounds, uintptr(unsafe.Pointer(&w.vis)), unsafe.Sizeof(w.vis))
		return []edgeWin{w}
	}
	for i := range n.Children {
		out = append(out, n.Children[i].windows()...)
	}
	return
}

type edgeWin struct {
	h      uintptr
	r, vis rect // window rect, visible frame (without the invisible resize border)
}

// split is the edge between two containers that a preview drag moves: like
// Hyprland, both sides follow the mouse at once, and GlazeWM gets one resize
// when the button is released. Only when that resize gives the same layout:
// GlazeWM spreads it over all siblings and scales nested splits, so the split
// must have exactly two containers whose every window touches the edge.
type split struct {
	vertical      bool
	first, second []edgeWin // left/top, right/bottom
	grow          int32     // +1: the dragged window is in first
	lo, hi, moved int32     // allowed edge shift, current shift
}

// tilingWindow returns the GlazeWM id of tiling window hwnd ("" if it isn't
// one) and its preview splits; preview is false when a resize must stay live.
func tilingWindow(g *glaze, hwnd uintptr) (id string, splits []*split, preview bool, err error) {
	var res struct{ Data struct{ Workspaces []node } }
	b, err := g.query("query workspaces")
	if err == nil {
		err = json.Unmarshal(b, &res)
	}
	if err != nil {
		return
	}
	var path []*node
	for i := range res.Data.Workspaces {
		if path = res.Data.Workspaces[i].path(hwnd); path != nil {
			break
		}
	}
	if path == nil || !path[len(path)-1].tiling() {
		return "", nil, false, nil // not a tiling window
	}
	id, preview = path[len(path)-1].ID, true
	for _, dir := range []string{"horizontal", "vertical"} {
		// the container GlazeWM resizes along this axis: the nearest one in a split of that direction
		for i := len(path) - 1; i > 0; i-- {
			if path[i-1].TilingDirection != dir {
				continue
			}
			var kids []*node
			for k := range path[i-1].Children {
				if c := &path[i-1].Children[k]; c.tiling() {
					kids = append(kids, c)
				}
			}
			s := &split{vertical: dir == "vertical", grow: 1}
			if len(kids) != 2 {
				preview = false
				break
			}
			if kids[1].ID == path[i].ID {
				s.grow = -1
			}
			s.first, s.second = kids[0].windows(), kids[1].windows()
			if !s.touching() {
				preview = false
				break
			}
			splits = append(splits, s)
			break
		}
	}
	return
}

// touching checks every window meets the edge and sets the shift limits (min 100px a window).
func (s *split) touching() bool {
	lo, hi := func(r rect) int32 { return r.l }, func(r rect) int32 { return r.r }
	if s.vertical {
		lo, hi = func(r rect) int32 { return r.t }, func(r rect) int32 { return r.b }
	}
	if len(s.first) == 0 || len(s.second) == 0 {
		return false
	}
	e, f := hi(s.first[0].vis), lo(s.second[0].vis)
	s.lo, s.hi = -1<<30, 1<<30
	for _, w := range s.first {
		if hi(w.vis) < e-2 || hi(w.vis) > e+2 {
			return false
		}
		s.lo = max(s.lo, 100-(hi(w.vis)-lo(w.vis)))
	}
	for _, w := range s.second {
		if lo(w.vis) < f-2 || lo(w.vis) > f+2 {
			return false
		}
		s.hi = min(s.hi, hi(w.vis)-lo(w.vis)-100)
	}
	return s.lo <= s.hi
}

// move shifts the edge by d from where the drag started and calls f with each
// window's new rect.
func (s *split) move(d int32, f func(w edgeWin, r rect)) {
	s.moved = min(max(d, s.lo), s.hi)
	for i, ws := range [][]edgeWin{s.first, s.second} {
		for _, w := range ws {
			r := w.r
			switch {
			case i == 0 && s.vertical:
				r.b += s.moved
			case i == 0:
				r.r += s.moved
			case s.vertical:
				r.t += s.moved
			default:
				r.l += s.moved
			}
			f(w, r)
		}
	}
}

// ghost draws a preview drag like Hyprland: the overlay shows live DWM
// thumbnails of the windows, the dragged ones stretched to their new size,
// and the apps resize only once, at the end. Zen, Explorer and friends take
// far longer to repaint than a mouse move, so resizing them live lags.
type ghost struct {
	ov     rect
	thumbs []thumb
	of     map[uintptr]int // window -> its thumb
}

// newGhost takes the overlay, or returns nil (a slide has it, or a window has no thumbnail).
func newGhost(splits []*split) *ghost {
	if !overlayMu.TryLock() {
		return nil
	}
	g := &ghost{ov: overlayRect(), of: map[uintptr]int{}}
	g.thumbs = thumbs(g.ov)
	for i, t := range g.thumbs {
		g.of[t.h] = i
	}
	for _, s := range splits {
		for _, w := range append(s.first, s.second...) {
			if _, ok := g.of[w.h]; !ok {
				g.close()
				return nil
			}
		}
	}
	place(g.thumbs, 0)
	showOverlay(g.ov)
	return g
}

// show draws window w's thumbnail at screen rect r.
func (g *ghost) show(w edgeWin, r rect) {
	t := &g.thumbs[g.of[w.h]]
	t.r = rect{r.l - g.ov.l, r.t - g.ov.t, r.r - g.ov.l, r.b - g.ov.t}
	place([]thumb{*t}, 0)
}

// done waits (up to 0.5s) for the real windows to reach the ghost's rects
// and paint, then removes the overlay.
func (g *ghost) done() {
	for end := time.Now().Add(500 * time.Millisecond); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		ok := true
		for _, t := range g.thumbs {
			var r rect
			windowRect.Call(t.h, uintptr(unsafe.Pointer(&r)))
			if abs(r.r-g.ov.l-t.r.r) > 2 || abs(r.l-g.ov.l-t.r.l) > 2 || abs(r.b-g.ov.t-t.r.b) > 2 || abs(r.t-g.ov.t-t.r.t) > 2 {
				ok = false
			}
		}
		if ok {
			break
		}
	}
	dwmFlush.Call() // a frame for the apps to paint at their new size
	dwmFlush.Call()
	hideOverlay()
	g.close()
}

func (g *ghost) close() {
	unregister(g.thumbs)
	overlayMu.Unlock()
}

func abs(v int32) int32 { return max(v, -v) }

// glaze is a minimal client for GlazeWM's IPC (WebSocket on port 6123): one
// text message out, wait for the reply.
type glaze struct {
	c net.Conn
	r *bufio.Reader
}

func dialGlaze() (*glaze, error) {
	c, err := net.DialTimeout("tcp", "127.0.0.1:6123", time.Second)
	if err != nil {
		return nil, err
	}
	c.SetDeadline(time.Now().Add(2 * time.Second))
	io.WriteString(c, "GET / HTTP/1.1\r\nHost: 127.0.0.1:6123\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Key: bWw0dy13aW5tYXNrIGtleQ==\r\nSec-WebSocket-Version: 13\r\n\r\n")
	g := &glaze{c, bufio.NewReader(c)}
	line, err := g.r.ReadString('\n')
	for err == nil && !strings.Contains(line, " 101 ") {
		err = fmt.Errorf("glazewm ipc: %q", line)
	}
	for err == nil && line != "\r\n" {
		line, err = g.r.ReadString('\n')
	}
	if err != nil {
		c.Close()
		return nil, err
	}
	return g, nil
}

func (g *glaze) ask(msg string) error {
	_, err := g.query(msg)
	return err
}

// query sends msg and returns the next text message.
func (g *glaze) query(msg string) ([]byte, error) {
	g.c.SetDeadline(time.Now().Add(2 * time.Second))
	p := []byte(msg)
	f := []byte{0x81} // FIN + text
	if len(p) < 126 {
		f = append(f, 0x80|byte(len(p)))
	} else {
		f = append(f, 0x80|126, byte(len(p)>>8), byte(len(p)))
	}
	f = append(f, 0, 0, 0, 0) // zero mask key: the payload goes unchanged
	if _, err := g.c.Write(append(f, p...)); err != nil {
		return nil, err
	}
	var out []byte
	for {
		var h [2]byte
		if _, err := io.ReadFull(g.r, h[:]); err != nil {
			return nil, err
		}
		n := uint64(h[1] & 0x7f)
		switch n {
		case 126:
			var e [2]byte
			if _, err := io.ReadFull(g.r, e[:]); err != nil {
				return nil, err
			}
			n = uint64(binary.BigEndian.Uint16(e[:]))
		case 127:
			var e [8]byte
			if _, err := io.ReadFull(g.r, e[:]); err != nil {
				return nil, err
			}
			n = binary.BigEndian.Uint64(e[:])
		}
		b := make([]byte, n)
		if _, err := io.ReadFull(g.r, b); err != nil {
			return nil, err
		}
		op := h[0] & 0x0f
		if op == 1 || op == 0 { // text or its continuation; control frames are skipped
			out = append(out, b...)
			if h[0]&0x80 != 0 {
				return out, nil
			}
		}
	}
}
