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

func TestCalendarBlackoutRange(t *testing.T) {
	w := &Window{Allow: []string{"* *"}, Blackout: []string{"2026-12-24..2026-12-27"}, Timezone: "UTC"}
	if err := w.Compile("UTC"); err != nil {
		t.Fatal(err)
	}
	closed := []string{"2026-12-24 00:00", "2026-12-25 13:00", "2026-12-27 23:30"}
	for _, when := range closed {
		if w.OpenAt(at(t, when)) {
			t.Errorf("%s must be blacked out", when)
		}
	}
	open := []string{"2026-12-23 23:59", "2026-12-28 00:00", "2027-12-25 12:00"}
	for _, when := range open {
		if !w.OpenAt(at(t, when)) {
			t.Errorf("%s must be open (the range names a year)", when)
		}
	}
}

func TestSingleCalendarDate(t *testing.T) {
	w := &Window{Allow: []string{"* *"}, Blackout: []string{"2026-12-31"}, Timezone: "UTC"}
	if err := w.Compile("UTC"); err != nil {
		t.Fatal(err)
	}
	if w.OpenAt(at(t, "2026-12-31 09:00")) {
		t.Error("the named date must be blacked out all day")
	}
	if !w.OpenAt(at(t, "2027-01-01 09:00")) {
		t.Error("the next day must be open again")
	}
}

func TestYearlyDateRepeats(t *testing.T) {
	w := &Window{Allow: []string{"* *"}, Blackout: []string{"12-24..12-26"}, Timezone: "UTC"}
	if err := w.Compile("UTC"); err != nil {
		t.Fatal(err)
	}
	for _, when := range []string{"2026-12-25 10:00", "2027-12-25 10:00", "2030-12-24 00:00"} {
		if w.OpenAt(at(t, when)) {
			t.Errorf("%s must be blacked out every year", when)
		}
	}
	if !w.OpenAt(at(t, "2026-12-27 10:00")) {
		t.Error("27 December must be open")
	}
}

func TestYearlyRangeWrapsNewYear(t *testing.T) {
	w := &Window{Allow: []string{"* *"}, Blackout: []string{"12-27..01-02"}, Timezone: "UTC"}
	if err := w.Compile("UTC"); err != nil {
		t.Fatal(err)
	}
	for _, when := range []string{"2026-12-28 10:00", "2027-01-01 10:00", "2027-01-02 23:00"} {
		if w.OpenAt(at(t, when)) {
			t.Errorf("%s must be blacked out", when)
		}
	}
	if !w.OpenAt(at(t, "2027-01-03 10:00")) {
		t.Error("3 January must be open")
	}
}

func TestCalendarDateWithTimeRange(t *testing.T) {
	w := &Window{Allow: []string{"* *"}, Blackout: []string{"2026-12-31 18:00-23:59"}, Timezone: "UTC"}
	if err := w.Compile("UTC"); err != nil {
		t.Fatal(err)
	}
	if !w.OpenAt(at(t, "2026-12-31 10:00")) {
		t.Error("the morning must stay open")
	}
	if w.OpenAt(at(t, "2026-12-31 19:00")) {
		t.Error("the evening must be blacked out")
	}
}

// A multi-day blackout must not make NextOpen give up and report a wrong time.
func TestNextOpenSeesPastALongBlackout(t *testing.T) {
	w := &Window{Allow: []string{"* *"}, Blackout: []string{"2026-12-20..2026-12-31"}, Timezone: "UTC"}
	if err := w.Compile("UTC"); err != nil {
		t.Fatal(err)
	}
	got := w.NextOpen(at(t, "2026-12-21 10:00"))
	want := at(t, "2027-01-01 00:00")
	if got.Before(want) || got.Sub(want) > 15*time.Minute {
		t.Errorf("NextOpen = %s, want about %s", got, want)
	}
}

func TestInvalidDateRules(t *testing.T) {
	for _, bad := range []string{
		"2026-13-01", "2026-12-32", "2026-12-27..2026-12-24", "2026-12-24..12-27", "20261224",
	} {
		w := &Window{Blackout: []string{bad}}
		if err := w.Compile("UTC"); err == nil {
			t.Errorf("rule %q should have been rejected", bad)
		}
	}
}

// Weekday rules must keep working now that dates share the syntax.
func TestWeekdayRulesStillParse(t *testing.T) {
	w := mustWindow(t, "Mon-Fri 22:00-05:00")
	if !w.OpenAt(at(t, "2026-09-09 23:00")) {
		t.Error("Mon-Fri must not be mistaken for a date")
	}
}
