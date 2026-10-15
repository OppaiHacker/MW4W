package main

import (
	"math"
	"strings"
	"testing"
)

// Needs a running GlazeWM; skipped otherwise.
func TestGlazeIPC(t *testing.T) {
	g, err := dialGlaze()
	if err != nil {
		t.Skip("GlazeWM not running:", err)
	}
	defer g.c.Close()
	for i := 0; i < 2; i++ { // twice: the connection must stay usable
		b, err := g.query("query windows")
		if err != nil || !strings.Contains(string(b), `"windows"`) {
			t.Fatalf("query windows: %v %.200s", err, b)
		}
	}
}

func TestResizeEdgesFloating(t *testing.T) {
	r := rect{100, 100, 500, 400}
	if sx, sy := resizeEdges(0, r, [2]int32{120, 390}, false); sx != -1 || sy != 1 {
		t.Fatalf("bottom-left grab: got %d,%d", sx, sy)
	}
}

func TestWind(t *testing.T) {
	if math.Abs(wind(0)) > 1e-6 || math.Abs(wind(1)-1) > 1e-6 {
		t.Fatal("wind must run 0 -> 1")
	}
	peak := 0.0
	for i := 0; i <= 100; i++ {
		peak = math.Max(peak, wind(float64(i)/100))
	}
	if peak <= 1 || peak > 1.05 { // y2 = 1.05: a small overshoot, like Hyprland
		t.Fatalf("overshoot %v", peak)
	}
}

func TestTakeOver(t *testing.T) {
	for cmd, want := range map[string]bool{
		"focus --workspace 2":                    true,
		"move --workspace 2|focus --workspace 2": true,
		"move --workspace 2":                     false, // silent move: GlazeWM's
		"@widget launcher":                       true,
		"@mw4w gamemode":                         true,
		"@mw4w empty-workspace":                  true,
		"shell-exec wt":                          true,
		"close":                                  false,
	} {
		if got := takeOver(strings.Split(cmd, "|")); got != want {
			t.Errorf("takeOver(%q) = %v", cmd, got)
		}
	}
}

func TestProgram(t *testing.T) {
	if exe, args := program(`C:\Windows\System32\rundll32.exe user32.dll,LockWorkStation`); !strings.HasSuffix(strings.ToLower(exe), "rundll32.exe") || len(args) != 1 {
		t.Errorf("rundll32: %q %q", exe, args)
	}
	if exe, _ := program("ms-screenclip:"); exe != "" { // GlazeWM's job
		t.Errorf("uri: %q", exe)
	}
}

func TestSplitTouching(t *testing.T) {
	w := func(l, r int32) edgeWin { return edgeWin{vis: rect{l, 0, r, 500}} }
	// [A | B/C]: every window meets the edge at 500/510
	s := split{first: []edgeWin{w(0, 500)}, second: []edgeWin{w(510, 1000), w(510, 1000)}}
	if !s.touching() || s.lo != -400 || s.hi != 390 {
		t.Fatalf("simple split: %v lo=%d hi=%d", s.touching(), s.lo, s.hi)
	}
	// [A|B | C]: A ends at 240, away from the edge: GlazeWM would scale A and B, so no preview
	s = split{first: []edgeWin{w(0, 240), w(250, 500)}, second: []edgeWin{w(510, 1000)}}
	if s.touching() {
		t.Fatal("nested split along the axis must stay live")
	}
}
