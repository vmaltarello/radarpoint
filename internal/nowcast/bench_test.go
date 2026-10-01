package nowcast

import (
	"testing"

	"github.com/vmaltarello/radarpoint/internal/geo"
)

// gtDPC is the georeference of the Radar-DPC SRI grid.
var gtDPC = geo.GeoTransform{OriginX: -600000, OriginY: 650000, PixelW: 1000, PixelH: 1000}

// BenchmarkForecast measures one point query with the default smoothing and
// probability on a grid the size of Radar-DPC SRI.
func BenchmarkForecast(b *testing.B) {
	const w, h = 1200, 1400
	fr := frames(randomBlobs(200, w, h), w, h, 2, 3, 1)
	n, err := Compute(fr, tm, gtDPC, step, 12, DefaultOptions)
	if err != nil {
		b.Fatal(err)
	}
	lat, lon := tm.Inverse(n.transform.Center(600, 700))
	b.ResetTimer()
	for b.Loop() {
		n.Forecast(lat, lon)
	}
}
