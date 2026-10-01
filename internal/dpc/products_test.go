package dpc

import (
	"testing"
	"time"
)

func TestInfo(t *testing.T) {
	sri, ok := Lookup("sri")
	if !ok || !sri.IsNoData(-9999) || sri.IsNoData(0) || sri.Unit != "mm/h" {
		t.Errorf("SRI info %+v", sri)
	}
	temp, _ := Lookup("TEMP")
	if !temp.IsNoData(-99999) || !temp.IsNoData(0) || temp.IsNoData(-0.5) {
		t.Errorf("TEMP info %+v", temp)
	}
	if _, ok := Lookup("VMI"); ok {
		t.Error("VMI should be unknown")
	}
}

func TestStale(t *testing.T) {
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	sri, _ := Lookup("SRI")
	temp, _ := Lookup("TEMP")
	vmi, _ := Lookup("VMI") // unknown delay: two periods allowed
	for _, c := range []struct {
		info   Info
		period time.Duration
		age    time.Duration
		want   bool
	}{
		{sri, 5 * time.Minute, 15 * time.Minute, false},
		{sri, 5 * time.Minute, 21 * time.Minute, true},
		{temp, time.Hour, 100 * time.Minute, false},
		{temp, time.Hour, 151 * time.Minute, true},
		{vmi, 5 * time.Minute, 15 * time.Minute, false},
		{vmi, 5 * time.Minute, 16 * time.Minute, true},
	} {
		if got := c.info.Stale(base, c.period, base.Add(c.age)); got != c.want {
			t.Errorf("%s age %v: stale %v, want %v", c.info.Type, c.age, got, c.want)
		}
	}
}

func TestDisplay(t *testing.T) {
	poh, _ := Lookup("POH")
	sri, _ := Lookup("SRI")
	if got := poh.Display(0.5); got != 50 || poh.Unit != "%" {
		t.Errorf("POH 0.5 shown as %v %s, want 50 %%", got, poh.Unit)
	}
	if got := sri.Display(2.5); got != 2.5 {
		t.Errorf("SRI 2.5 shown as %v", got)
	}
}
