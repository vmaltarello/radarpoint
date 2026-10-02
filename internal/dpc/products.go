package dpc

import (
	"math"
	"slices"
	"strings"
	"time"
)

// Info describes how to interpret the values of a product file. Units and
// nodata codes are not documented by the API: they come from inspecting real
// files (see README).
type Info struct {
	Type        string
	Description string
	Unit        string
	// Scale converts stored values to the unit shown, e.g. 100 for a 0–1
	// probability shown in %. Zero means 1.
	Scale      float64
	NoData     []float64 // sentinel values meaning "no data"
	NoDataText string    // shown for nodata values
	ZeroText   string    // shown for a value of exactly 0, if meaningful
	// ZeroValues are stored codes that mean 0, e.g. ETM's -9998: radar
	// coverage but no echo.
	ZeroValues []float64
	// ZeroIsNoData marks products where exactly 0 is a mask (e.g. outside
	// Italy), not a measurement.
	ZeroIsNoData bool
	// ZeroMask, when it matches the grid, refines ZeroIsNoData: an exact 0
	// is no data only on masked pixels, and a real measurement elsewhere.
	ZeroMask *Mask
	// MaxDelay is the longest normal wait between the nominal time of an
	// instant and its publication, as observed on the API. Zero means
	// unknown; see Stale.
	MaxDelay time.Duration
}

const radarNoData = "no data (radar not available at this point)"

var catalog = map[string]Info{
	"SRI": {Type: "SRI", Description: "rain rate at ground level", Unit: "mm/h",
		NoData: []float64{-9999}, NoDataText: radarNoData, ZeroText: "no rain",
		MaxDelay: 15 * time.Minute},
	// POH is stored as 0–1 in steps of 1/254; values under 0.30 are set to 0.
	"POH": {Type: "POH", Description: "probability of hail", Unit: "%", Scale: 100,
		NoData: []float64{-9999}, NoDataText: radarNoData, ZeroText: "hail unlikely, under 30%",
		MaxDelay: 15 * time.Minute},
	// VIL and ETM are stored directly; ETM marks coverage without echo with
	// -9998 (see README).
	"VIL": {Type: "VIL", Description: "vertically integrated liquid water", Unit: "kg/m²",
		NoData: []float64{-9999}, NoDataText: radarNoData, ZeroText: "no echo",
		MaxDelay: 15 * time.Minute},
	"ETM": {Type: "ETM", Description: "maximum echo top height", Unit: "m",
		NoData: []float64{-9999}, NoDataText: radarNoData, ZeroText: "no echo", ZeroValues: []float64{-9998},
		MaxDelay: 15 * time.Minute},
	"TEMP": {Type: "TEMP", Description: "air temperature, interpolated from ground stations", Unit: "°C",
		NoData: []float64{-99999}, ZeroIsNoData: true, ZeroMask: tempMask,
		NoDataText: "no data (point at sea or outside Italy)", MaxDelay: 90 * time.Minute},
}

// Lookup returns the known interpretation of a product type. Unknown types
// get a generic entry treating -9999 and -99999 as nodata.
func Lookup(productType string) (Info, bool) {
	t := strings.ToUpper(productType)
	if i, ok := catalog[t]; ok {
		return i, true
	}
	return Info{Type: t, NoData: []float64{-9999, -99999}, NoDataText: "no data"}, false
}

// IsNoData reports whether v is a nodata sentinel (or NaN) for this product.
func (i Info) IsNoData(v float64) bool {
	return math.IsNaN(v) || slices.Contains(i.NoData, v) || (i.ZeroIsNoData && v == 0)
}

// ValidTypes lists the product types accepted by the API.
var ValidTypes = []string{"VMI", "SRI", "SRT1", "IR_108", "TEMP", "CUM3", "CUM6", "CUM12", "CUM24",
	"CAPPI_1", "CAPPI_2", "CAPPI_3", "CAPPI_4", "CAPPI_5", "CAPPI_6", "CAPPI_7", "CAPPI_8", "CAPPI_9", "CAPPI_10",
	"VIL", "ETM", "POH", "SITES"}

// Stale reports whether data with nominal time t is older than it should be
// at now, given the product period: the next instant should have been
// published by t + period + MaxDelay. Without a known MaxDelay, two periods
// are allowed.
func (i Info) Stale(t time.Time, period time.Duration, now time.Time) bool {
	delay := i.MaxDelay
	if delay == 0 {
		delay = 2 * period
	}
	return now.Sub(t) > period+delay
}

// Display converts a stored value to the unit shown to users.
func (i Info) Display(v float64) float64 {
	if slices.Contains(i.ZeroValues, v) {
		return 0
	}
	if i.Scale == 0 {
		return v
	}
	return v * i.Scale
}

// NoDataAt is IsNoData for pixel (col, row) of grid g. With a zero mask
// built for g, an exact 0 is no data only where the mask says so: a real
// 0 °C on land is kept.
func (i Info) NoDataAt(v float64, col, row int, g Grid) bool {
	if v == 0 && i.ZeroIsNoData && i.ZeroMask.Matches(g) {
		return i.ZeroMask.Outside(col, row)
	}
	return i.IsNoData(v)
}
