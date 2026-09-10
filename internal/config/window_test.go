package config

import (
	"testing"
	"time"
)

func mustWindow(t *testing.T, allow ...string) *Window {
	t.Helper()
	w := &Window{Allow: allow, Timezone: "UTC"}
	if err := w.Compile("UTC"); err != nil {
		t.Fatalf("compile: %v", err)
	}
	return w
}

func at(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse("2006-01-02 15:04", s)
	if err != nil {
		t.Fatal(err)
	}
	return ts.UTC()
}

func TestEmptyWindowIsAlwaysOpen(t *testing.T) {
	var w *Window
	if !w.OpenAt(time.Now()) {
		t.Fatal("a missing window must deploy immediately")
	}
	if !mustWindow(t).OpenAt(time.Now()) {
		t.Fatal("a window without rules must deploy immediately")
	}
}

func TestWeekdayRangeAndClock(t *testing.T) {
	w := mustWindow(t, "Mon-Fri 22:00-23:30")
	// 2026-09-09 is a Wednesday.
	cases := []struct {
		when string
		open bool
	}{
		{"2026-09-09 22:00", true},
		{"2026-09-09 23:29", true},
		{"2026-09-09 23:30", false},
		{"2026-09-09 21:59", false},
		{"2026-09-12 22:30", false}, // Saturday
	}
	for _, c := range cases {
		if got := w.OpenAt(at(t, c.when)); got != c.open {
			t.Errorf("%s: open=%v, want %v", c.when, got, c.open)
		}
	}
}

func TestWindowWrapsPastMidnight(t *testing.T) {
	w := mustWindow(t, "Mon-Fri 22:00-05:00")
	cases := []struct {
		when string
		open bool
	}{
		{"2026-09-09 23:00", true}, // Wednesday night
		{"2026-09-10 04:59", true}, // Thursday morning, still Wednesday's window
		{"2026-09-10 05:00", false},
		{"2026-09-12 03:00", true},  // Saturday morning belongs to Friday night
		{"2026-09-12 23:00", false}, // Saturday night is not allowed
		{"2026-09-13 03:00", false}, // Sunday morning would belong to Saturday
	}
	for _, c := range cases {
		if got := w.OpenAt(at(t, c.when)); got != c.open {
			t.Errorf("%s: open=%v, want %v", c.when, got, c.open)
		}
	}
}

func TestWholeDayAndLists(t *testing.T) {
	w := mustWindow(t, "Sat,Sun *")
	if !w.OpenAt(at(t, "2026-09-12 13:00")) {
		t.Error("Saturday should be open all day")
	}
	if w.OpenAt(at(t, "2026-09-09 13:00")) {
		t.Error("Wednesday should be closed")
	}
}

func TestBlackoutBeatsAllow(t *testing.T) {
	w := &Window{Allow: []string{"* *"}, Blackout: []string{"Fri 16:00-23:59"}, Timezone: "UTC"}
	if err := w.Compile("UTC"); err != nil {
		t.Fatal(err)
	}
	if w.OpenAt(at(t, "2026-09-11 17:00")) {
		t.Error("Friday evening must be blacked out")
	}
	if !w.OpenAt(at(t, "2026-09-11 09:00")) {
		t.Error("Friday morning must stay open")
	}
}

func TestNextOpen(t *testing.T) {
	w := mustWindow(t, "Mon-Fri 22:00-23:00")
	got := w.NextOpen(at(t, "2026-09-09 10:00"))
	want := at(t, "2026-09-09 22:00")
	if !got.Equal(want) {
		t.Errorf("NextOpen = %s, want %s", got, want)
	}
	// Already open: return the same instant.
	now := at(t, "2026-09-09 22:30")
	if !w.NextOpen(now).Equal(now) {
		t.Error("NextOpen inside the window must return now")
	}
}

func TestTimezoneIsRespected(t *testing.T) {
	w := &Window{Allow: []string{"Mon-Fri 09:00-10:00"}, Timezone: "Europe/Berlin"}
	if err := w.Compile(""); err != nil {
		t.Fatal(err)
	}
	// 07:30 UTC is 09:30 Berlin time in September (CEST).
	if !w.OpenAt(at(t, "2026-09-09 07:30")) {
		t.Error("window must be evaluated in its own timezone")
	}
}

func TestInvalidRules(t *testing.T) {
	for _, bad := range []string{"Funday 10:00-11:00", "Mon 25:00-26:00", "Mon 10:00", "Mon 10:00-10:00"} {
		w := &Window{Allow: []string{bad}}
		if err := w.Compile("UTC"); err == nil {
			t.Errorf("rule %q should have been rejected", bad)
		}
	}
}
