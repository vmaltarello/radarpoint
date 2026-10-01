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

func TestTempZeroMask(t *testing.T) {
	temp, _ := Lookup("TEMP")
	grid := Grid{W: 631, H: 576, OriginX: 6, OriginY: 47.50026321411133, PixelW: 0.019983009747110096}
	if !temp.ZeroMask.Matches(grid) {
		t.Fatal("embedded mask does not match the TEMP grid")
	}
	// Legnano (col 145, row 95) is on land: 0 °C there is a measurement.
	if temp.NoDataAt(0, 145, 95, grid) {
		t.Error("0 °C on land treated as no data")
	}
	// The Adriatic at 45.6N 13.0E (col 350, row 95) is masked.
	if !temp.NoDataAt(0, 350, 95, grid) {
		t.Error("0 at sea not treated as no data")
	}
	// Other values are never masked, and -99999 is always no data.
	if temp.NoDataAt(-3.5, 350, 95, grid) || !temp.NoDataAt(-99999, 145, 95, grid) {
		t.Error("mask applied to values other than 0")
	}
	// On another grid the mask does not apply: back to "0 is no data".
	other := grid
	other.W = 600
	if !temp.NoDataAt(0, 145, 95, other) {
		t.Error("mask applied to a different grid")
	}
	// About three quarters of the grid is sea or abroad.
	n := 0
	for _, o := range temp.ZeroMask.outside {
		if o {
			n++
		}
	}
	if share := float64(n) / float64(len(temp.ZeroMask.outside)); share < 0.7 || share > 0.85 {
		t.Errorf("%.0f%% of the grid masked, want about 77%%", share*100)
	}
}
