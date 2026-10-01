package dpc

import (
	"fmt"
	"regexp"
	"strconv"
	"time"
)

var periodRE = regexp.MustCompile(`^PT(?:(\d+)H)?(?:(\d+)M)?(?:(\d+)S)?$`)

// ParsePeriod converts the ISO 8601 time-only durations used by the API,
// such as "PT5M" or "PT1H", to a time.Duration.
func ParsePeriod(s string) (time.Duration, error) {
	m := periodRE.FindStringSubmatch(s)
	if m == nil || s == "PT" {
		return 0, fmt.Errorf("unrecognised period %q", s)
	}
	var d time.Duration
	for i, unit := range []time.Duration{time.Hour, time.Minute, time.Second} {
		if m[i+1] != "" {
			n, _ := strconv.Atoi(m[i+1])
			d += time.Duration(n) * unit
		}
	}
	if d <= 0 {
		return 0, fmt.Errorf("zero period %q", s)
	}
	return d, nil
}

// PeriodDuration returns the product period as a time.Duration.
func (p Product) PeriodDuration() (time.Duration, error) { return ParsePeriod(p.Period) }
