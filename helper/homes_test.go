package main

import (
	"reflect"
	"sort"
	"testing"
)

func both(ks ...string) map[string]bool {
	m := map[string]bool{}
	for _, k := range ks {
		m[k] = true
	}
	return m
}

func TestHomesMonitorComesBack(t *testing.T) {
	h := newHomes()
	// A shows 1, B shows 2 (3 hidden on B)
	h.plan([]wsAt{{"1", "A", true}, {"2", "B", true}, {"3", "B", false}}, both("A", "B"), nil, false, nil)
	// B goes away: GlazeWM parks 2 and 3 on A, behind 1
	moves, show := h.plan([]wsAt{{"1", "A", true}, {"2", "A", false}, {"3", "A", false}}, both("A"), nil, true, nil)
	if len(moves) != 0 || len(show) != 0 {
		t.Fatalf("nowhere to go yet: %v %v", moves, show)
	}
	// still gone, nothing changes on a later look either
	if moves, _ := h.plan([]wsAt{{"1", "A", true}, {"2", "A", false}, {"3", "A", false}}, both("A"), nil, false, nil); len(moves) != 0 {
		t.Fatalf("moved while B is gone: %v", moves)
	}
	// B is back with a new empty workspace 6 shown on it
	moves, show = h.plan([]wsAt{{"1", "A", true}, {"2", "A", false}, {"3", "A", false}, {"6", "B", true}}, both("A", "B"), nil, true, both("B"))
	if !reflect.DeepEqual(moves, map[string]string{"2": "B", "3": "B"}) {
		t.Fatalf("moves %v", moves)
	}
	if !reflect.DeepEqual(show, []string{"2"}) {
		t.Fatalf("show %v", show)
	}
	// once moved, everything is home and stays put
	moves, show = h.plan([]wsAt{{"1", "A", true}, {"2", "B", true}, {"3", "B", false}}, both("A", "B"), nil, false, nil)
	if len(moves) != 0 || len(show) != 0 || h.Displaced["2"] || h.Shown["B"] != "2" {
		t.Fatalf("settled: %v %v %+v", moves, show, h)
	}
}

func TestHomesMovedByHand(t *testing.T) {
	h := newHomes()
	h.plan([]wsAt{{"1", "A", true}, {"2", "B", true}}, both("A", "B"), nil, false, nil)
	// SUPER+ALT+arrow: 1 goes to B
	if moves, _ := h.plan([]wsAt{{"1", "B", true}, {"2", "B", false}}, both("A", "B"), nil, false, nil); len(moves) != 0 {
		t.Fatalf("a move by hand was undone: %v", moves)
	}
	if h.Home["1"] != "B" {
		t.Fatalf("home of 1: %q", h.Home["1"])
	}
	// a display change later (same monitors) keeps it there
	if moves, _ := h.plan([]wsAt{{"1", "B", true}, {"2", "B", false}, {"3", "A", true}}, both("A", "B"), nil, true, nil); len(moves) != 0 {
		t.Fatalf("moved after a mode switch: %v", moves)
	}
}

func TestHomesBlipWithSameMonitors(t *testing.T) {
	h := newHomes()
	h.plan([]wsAt{{"1", "A", true}, {"2", "B", true}}, both("A", "B"), nil, false, nil)
	// B dropped and came back between two looks: same monitors, but 2 got parked on A
	moves, show := h.plan([]wsAt{{"1", "A", true}, {"2", "A", false}, {"6", "B", true}}, both("A", "B"), nil, true, both("B"))
	if !reflect.DeepEqual(moves, map[string]string{"2": "B"}) || !reflect.DeepEqual(show, []string{"2"}) {
		t.Fatalf("%v %v", moves, show)
	}
}

func TestHomesBound(t *testing.T) {
	h := newHomes()
	bound := map[string]string{"4": "B"}
	moves, _ := h.plan([]wsAt{{"1", "A", true}, {"4", "A", false}}, both("A", "B"), bound, false, nil)
	if !reflect.DeepEqual(moves, map[string]string{"4": "B"}) {
		t.Fatalf("bound one stays off its monitor: %v", moves)
	}
	// its monitor gone: it waits, and isn't rehomed
	moves, _ = h.plan([]wsAt{{"1", "A", true}, {"4", "A", false}}, both("A"), map[string]string{"4": ""}, true, nil)
	if len(moves) != 0 || h.Home["4"] != "B" {
		t.Fatalf("%v %q", moves, h.Home["4"])
	}
}

func TestHomesShownAgain(t *testing.T) {
	h := newHomes()
	h.plan([]wsAt{{"1", "A", true}, {"2", "B", true}, {"4", "B", false}}, both("A", "B"), map[string]string{"2": "B"}, false, nil)
	// B came back; GlazeWM moved the bound 2 back itself, but behind its new 6
	moves, show := h.plan([]wsAt{{"1", "A", true}, {"2", "B", false}, {"4", "A", false}, {"6", "B", true}}, both("A", "B"), map[string]string{"2": "B"}, true, both("B"))
	if !reflect.DeepEqual(moves, map[string]string{"4": "B"}) || !reflect.DeepEqual(show, []string{"2"}) {
		t.Fatalf("%v %v", moves, show)
	}
	// a monitor that stayed keeps what the user picked
	_, show = h.plan([]wsAt{{"1", "A", false}, {"5", "A", true}, {"2", "B", true}}, both("A", "B"), nil, true, both("B"))
	if len(show) != 0 {
		t.Fatalf("show %v", show)
	}
}

func TestHomesPrune(t *testing.T) {
	h := newHomes()
	h.plan([]wsAt{{"1", "A", true}, {"7", "A", false}}, both("A"), nil, false, nil)
	h.plan([]wsAt{{"1", "A", true}}, both("A"), nil, false, nil)
	keys := []string{}
	for k := range h.Home {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if !reflect.DeepEqual(keys, []string{"1"}) {
		t.Fatalf("homes %v", keys)
	}
}
