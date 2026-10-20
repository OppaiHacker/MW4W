// Workspace homes: every workspace remembers the monitor it belongs on.
//
// GlazeWM moves the workspaces of a monitor that goes away (unplugged, a
// DisplayPort monitor falling asleep, a mode switch that drops it for a
// moment) to another monitor, hidden behind the one shown there, and never
// moves them back: windows "disappear". Like Hyprland, a workspace goes back
// to its monitor as soon as that one is back. Moving a workspace by hand
// (move-workspace) makes the new monitor its home. Plain logic, no Windows
// calls, so it is tested anywhere (homes_test.go).
package main

// homes is saved to cache\workspaces.json, so it survives winmask and GlazeWM restarts.
type homes struct {
	Home      map[string]string `json:"home"`      // workspace -> monitor key
	Displaced map[string]bool   `json:"displaced"` // pushed off its home by a monitor that went away
	Shown     map[string]string `json:"shown"`     // monitor key -> the workspace it showed
}

// wsAt is a workspace in GlazeWM's layout now.
type wsAt struct {
	name, mon string // mon: monitor key
	shown     bool
}

func newHomes() *homes {
	return &homes{Home: map[string]string{}, Displaced: map[string]bool{}, Shown: map[string]string{}}
}

// plan updates h from GlazeWM's layout and says what to do about it:
// workspaces to move (name -> monitor key) and workspaces to show again on
// their monitor once there. forced: monitors came or went (or GlazeWM
// started) since the last look, so a workspace off its home was put there by
// GlazeWM, not by the user. added: the monitors that came (back), where
// GlazeWM shows a new empty workspace. bound: workspaces bound to a monitor
// in the Displays popup (name -> key, "" while that monitor is gone).
func (h *homes) plan(ws []wsAt, present map[string]bool, bound map[string]string, forced bool, added map[string]bool) (moves map[string]string, show []string) {
	moves = map[string]string{}
	exists := map[string]bool{}
	for _, w := range ws {
		exists[w.name] = true
		if k, ok := bound[w.name]; ok {
			if k != "" {
				h.Home[w.name] = k
				delete(h.Displaced, w.name)
				if w.mon != k {
					moves[w.name] = k
				}
			}
			continue // a bound one whose monitor is gone waits wherever GlazeWM put it
		}
		home, ok := h.Home[w.name]
		switch {
		case !ok:
			h.Home[w.name] = w.mon
		case home == w.mon:
			delete(h.Displaced, w.name)
		case !present[home]:
			h.Displaced[w.name] = true
		case h.Displaced[w.name] || forced:
			moves[w.name] = home
		default: // moved by hand
			h.Home[w.name] = w.mon
		}
	}
	for n := range h.Home {
		if !exists[n] { // GlazeWM dropped it (empty)
			delete(h.Home, n)
			delete(h.Displaced, n)
		}
	}
	for k, n := range h.Shown {
		if !present[k] {
			continue // kept for when it's back
		}
		if !exists[n] {
			delete(h.Shown, k)
			continue
		}
		// GlazeWM shows a new empty workspace on a monitor that comes back, behind which ours wait
		if moves[n] == k {
			show = append(show, n)
		} else if added[k] {
			for _, w := range ws {
				if w.name == n && w.mon == k && !w.shown {
					show = append(show, n)
				}
			}
		}
	}
	if !forced {
		for _, w := range ws {
			if w.shown && h.Home[w.name] == w.mon && moves[w.name] == "" {
				h.Shown[w.mon] = w.name
			}
		}
	}
	return moves, show
}
