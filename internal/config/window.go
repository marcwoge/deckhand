package config

import (
	"fmt"
	"strings"
	"time"
)

// Window describes when a deployment may run. An empty Window (no allow rules)
// is always open, which is the default: deploy immediately.
type Window struct {
	Timezone string   `yaml:"timezone"`
	Allow    []string `yaml:"allow"`
	Blackout []string `yaml:"blackout"`

	loc      *time.Location
	allow    []rule
	blackout []rule
}

// rule is one parsed "Mon-Fri 22:00-05:00" style entry.
type rule struct {
	days     [7]bool // index 0 = Sunday, matching time.Weekday
	startMin int     // minutes since midnight
	endMin   int     // minutes since midnight; may be <= startMin for wrap-around
	wholeDay bool
	source   string
}

var weekdayNames = map[string]time.Weekday{
	"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday,
	"wed": time.Wednesday, "thu": time.Thursday, "fri": time.Friday,
	"sat": time.Saturday,
}

// Compile parses the rules and resolves the timezone. defaultTZ is used when
// the window does not carry one of its own.
func (w *Window) Compile(defaultTZ string) error {
	if w == nil {
		return nil
	}
	tz := w.Timezone
	if tz == "" {
		tz = defaultTZ
	}
	if tz == "" || strings.EqualFold(tz, "local") {
		w.loc = time.Local
	} else {
		loc, err := time.LoadLocation(tz)
		if err != nil {
			return fmt.Errorf("unknown timezone %q: %w", tz, err)
		}
		w.loc = loc
	}
	w.allow = w.allow[:0]
	for _, s := range w.Allow {
		r, err := parseRule(s)
		if err != nil {
			return fmt.Errorf("allow: %w", err)
		}
		w.allow = append(w.allow, r)
	}
	w.blackout = w.blackout[:0]
	for _, s := range w.Blackout {
		r, err := parseRule(s)
		if err != nil {
			return fmt.Errorf("blackout: %w", err)
		}
		w.blackout = append(w.blackout, r)
	}
	return nil
}

// parseRule understands:
//
//	"Mon-Fri 22:00-05:00"   weekday range plus time range (may wrap midnight)
//	"Sat,Sun 00:00-06:00"   weekday list
//	"* 02:00-04:00"         every day
//	"Sun *"                 whole day
//	"22:00-05:00"           every day, time range only
func parseRule(s string) (rule, error) {
	r := rule{source: s}
	fields := strings.Fields(strings.TrimSpace(s))
	if len(fields) == 0 {
		return r, fmt.Errorf("empty rule")
	}
	var daySpec, timeSpec string
	switch len(fields) {
	case 1:
		if strings.Contains(fields[0], ":") {
			daySpec, timeSpec = "*", fields[0]
		} else {
			daySpec, timeSpec = fields[0], "*"
		}
	case 2:
		daySpec, timeSpec = fields[0], fields[1]
	default:
		return r, fmt.Errorf("rule %q: expected \"<days> <times>\"", s)
	}

	if daySpec == "*" {
		for i := range r.days {
			r.days[i] = true
		}
	} else {
		for _, part := range strings.Split(daySpec, ",") {
			part = strings.TrimSpace(part)
			if from, to, ok := strings.Cut(part, "-"); ok {
				a, err := parseWeekday(from)
				if err != nil {
					return r, err
				}
				b, err := parseWeekday(to)
				if err != nil {
					return r, err
				}
				for d := a; ; d = (d + 1) % 7 {
					r.days[d] = true
					if d == b {
						break
					}
				}
				continue
			}
			d, err := parseWeekday(part)
			if err != nil {
				return r, err
			}
			r.days[d] = true
		}
	}

	if timeSpec == "*" {
		r.wholeDay = true
		return r, nil
	}
	from, to, ok := strings.Cut(timeSpec, "-")
	if !ok {
		return r, fmt.Errorf("rule %q: time range must look like \"22:00-05:00\"", s)
	}
	var err error
	if r.startMin, err = parseClock(from); err != nil {
		return r, fmt.Errorf("rule %q: %w", s, err)
	}
	if r.endMin, err = parseClock(to); err != nil {
		return r, fmt.Errorf("rule %q: %w", s, err)
	}
	if r.startMin == r.endMin {
		return r, fmt.Errorf("rule %q: start and end are identical; use \"*\" for a whole day", s)
	}
	return r, nil
}

func parseWeekday(s string) (time.Weekday, error) {
	key := strings.ToLower(strings.TrimSpace(s))
	if len(key) > 3 {
		key = key[:3]
	}
	d, ok := weekdayNames[key]
	if !ok {
		return 0, fmt.Errorf("unknown weekday %q", s)
	}
	return d, nil
}

func parseClock(s string) (int, error) {
	s = strings.TrimSpace(s)
	hh, mm, ok := strings.Cut(s, ":")
	if !ok {
		return 0, fmt.Errorf("invalid time %q, expected HH:MM", s)
	}
	var h, m int
	if _, err := fmt.Sscanf(hh, "%d", &h); err != nil {
		return 0, fmt.Errorf("invalid time %q", s)
	}
	if _, err := fmt.Sscanf(mm, "%d", &m); err != nil {
		return 0, fmt.Errorf("invalid time %q", s)
	}
	if h == 24 && m == 0 {
		return 24 * 60, nil
	}
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, fmt.Errorf("invalid time %q", s)
	}
	return h*60 + m, nil
}

func (r rule) matches(t time.Time) bool {
	minutes := t.Hour()*60 + t.Minute()
	if r.wholeDay {
		return r.days[int(t.Weekday())]
	}
	if r.endMin > r.startMin {
		return r.days[int(t.Weekday())] && minutes >= r.startMin && minutes < r.endMin
	}
	// Wraps past midnight: the window belongs to the day it starts on.
	if r.days[int(t.Weekday())] && minutes >= r.startMin {
		return true
	}
	prev := (int(t.Weekday()) + 6) % 7
	return r.days[prev] && minutes < r.endMin
}

// OpenAt reports whether a deployment may start at t.
func (w *Window) OpenAt(t time.Time) bool {
	if w == nil || len(w.allow) == 0 && len(w.blackout) == 0 {
		return true
	}
	local := t.In(w.location())
	for _, r := range w.blackout {
		if r.matches(local) {
			return false
		}
	}
	if len(w.allow) == 0 {
		return true
	}
	for _, r := range w.allow {
		if r.matches(local) {
			return true
		}
	}
	return false
}

// NextOpen returns the next instant at or after t at which the window is open.
// It scans minute by minute for eight days; if the window never opens within
// that period it returns t plus eight days so callers keep re-evaluating
// instead of blocking forever.
func (w *Window) NextOpen(t time.Time) time.Time {
	if w.OpenAt(t) {
		return t
	}
	cur := t.Truncate(time.Minute)
	for i := 0; i < 8*24*60; i++ {
		cur = cur.Add(time.Minute)
		if w.OpenAt(cur) {
			return cur
		}
	}
	return t.Add(8 * 24 * time.Hour)
}

func (w *Window) location() *time.Location {
	if w == nil || w.loc == nil {
		return time.Local
	}
	return w.loc
}

// Describe renders the window for status output.
func (w *Window) Describe() string {
	if w == nil || len(w.Allow) == 0 && len(w.Blackout) == 0 {
		return "always"
	}
	parts := []string{}
	if len(w.Allow) > 0 {
		parts = append(parts, strings.Join(w.Allow, ", "))
	}
	if len(w.Blackout) > 0 {
		parts = append(parts, "except "+strings.Join(w.Blackout, ", "))
	}
	return strings.Join(parts, " ") + " (" + w.location().String() + ")"
}

// nowUTC exists so tests can talk about "now" without importing time.
func nowUTC() time.Time { return time.Now().UTC() }
