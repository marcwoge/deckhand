package config

import (
	"fmt"
	"time"
)

// Duration is a time.Duration that unmarshals from a YAML string such as
// "60s", "10m" or "2h30m". Plain integers are interpreted as seconds.
type Duration time.Duration

func (d Duration) String() string { return time.Duration(d).String() }

// D returns the value as a time.Duration, falling back to def when unset.
func (d Duration) D(def time.Duration) time.Duration {
	if d == 0 {
		return def
	}
	return time.Duration(d)
}

func (d *Duration) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var s string
	if err := unmarshal(&s); err == nil {
		parsed, err := time.ParseDuration(s)
		if err != nil {
			return fmt.Errorf("invalid duration %q: %w", s, err)
		}
		*d = Duration(parsed)
		return nil
	}
	var secs int
	if err := unmarshal(&secs); err != nil {
		return fmt.Errorf("duration must be a string like \"60s\" or a number of seconds")
	}
	*d = Duration(time.Duration(secs) * time.Second)
	return nil
}

func (d Duration) MarshalYAML() (interface{}, error) { return d.String(), nil }
