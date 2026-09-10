package config

import (
	"fmt"
	"strconv"
	"strings"
)

// Size is a byte count that unmarshals from "10MB", "512KB" or a plain number
// of bytes.
type Size int64

func (s Size) String() string {
	switch {
	case s >= 1<<30:
		return fmt.Sprintf("%.1fGB", float64(s)/(1<<30))
	case s >= 1<<20:
		return fmt.Sprintf("%.0fMB", float64(s)/(1<<20))
	case s >= 1<<10:
		return fmt.Sprintf("%.0fKB", float64(s)/(1<<10))
	default:
		return fmt.Sprintf("%dB", int64(s))
	}
}

// B returns the byte count, falling back to def when unset.
func (s Size) B(def int64) int64 {
	if s == 0 {
		return def
	}
	return int64(s)
}

func (s *Size) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var n int64
	if err := unmarshal(&n); err == nil {
		*s = Size(n)
		return nil
	}
	var raw string
	if err := unmarshal(&raw); err != nil {
		return fmt.Errorf("size must be a number of bytes or a string like \"10MB\"")
	}
	text := strings.TrimSpace(strings.ToUpper(raw))
	multiplier := int64(1)
	for suffix, m := range map[string]int64{"KB": 1 << 10, "MB": 1 << 20, "GB": 1 << 30} {
		if strings.HasSuffix(text, suffix) {
			text, multiplier = strings.TrimSuffix(text, suffix), m
			break
		}
	}
	text = strings.TrimSuffix(text, "B")
	value, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
	if err != nil {
		return fmt.Errorf("invalid size %q", raw)
	}
	if value < 0 {
		return fmt.Errorf("size %q cannot be negative", raw)
	}
	*s = Size(int64(value) * multiplier)
	return nil
}

func (s Size) MarshalYAML() (interface{}, error) { return s.String(), nil }
