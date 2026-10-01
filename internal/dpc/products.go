package dpc

import (
	"math"
	"slices"
	"strings"
)

// Info describes how to interpret the values of a product file. Units and
// nodata codes are not documented by the API: they come from inspecting real
// files (see README).
type Info struct {
	Type        string
	Description string
	Unit        string
	NoData      []float64 // sentinel values meaning "no data"
	NoDataText  string    // shown for nodata values
	ZeroText    string    // shown for a value of exactly 0, if meaningful
	// ZeroIsNoData marks products where exactly 0 is a mask (e.g. outside
	// Italy), not a measurement.
	ZeroIsNoData bool
}

const radarNoData = "no data (radar not available at this point)"

var catalog = map[string]Info{
	"SRI": {Type: "SRI", Description: "rain rate at ground level", Unit: "mm/h",
		NoData: []float64{-9999}, NoDataText: radarNoData, ZeroText: "no rain"},
	"POH": {Type: "POH", Description: "probability of hail",
		NoData: []float64{-9999}, NoDataText: radarNoData, ZeroText: "no hail"},
	"TEMP": {Type: "TEMP", Description: "air temperature, interpolated from ground stations", Unit: "°C",
		NoData: []float64{-99999}, ZeroIsNoData: true,
		NoDataText: "no data (point at sea or outside Italy)"},
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
