// Command tiffdump prints the TIFF tags, GeoTIFF keys and value statistics of
// a single-band GeoTIFF. It is a small stand-in for gdalinfo.
package main

import (
	"fmt"
	"math"
	"os"
	"sort"

	"github.com/vmaltarello/radarpoint/internal/raster"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: tiffdump file.tif...")
		os.Exit(2)
	}
	for _, p := range os.Args[1:] {
		if err := dump(p); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", p, err)
			os.Exit(1)
		}
	}
}

func dump(path string) error {
	im, err := raster.Open(path)
	if err != nil {
		return err
	}
	fi, _ := os.Stat(path)
	fmt.Printf("== %s (%d bytes)\n", path, fi.Size())
	fmt.Println("-- tags")
	for _, id := range im.SortedTagIDs() {
		t := im.Tags[id]
		fmt.Printf("%6d type=%-2d count=%-7d ", id, t.Type, t.Count)
		switch {
		case t.Str != "":
			s := t.Str
			if len(s) > 400 {
				s = s[:400] + "..."
			}
			fmt.Printf("%q\n", s)
		case len(t.Floats) > 0:
			fmt.Println(trunc(t.Floats))
		default:
			fmt.Println(trunc(t.Ints))
		}
	}
	bw, bh := im.BlockSize()
	fmt.Printf("-- layout: %dx%d, %d bit, sampleformat=%d, compression=%d, predictor=%d, tiled=%v, block=%dx%d\n",
		im.Width, im.Height, im.BitsPerSample, im.SampleFormat, im.Compression, im.Predictor, im.Tiled(), bw, bh)

	if keys, err := raster.ParseGeoKeys(im); err == nil {
		fmt.Println("-- geokeys")
		ids := make([]int, 0, len(keys))
		for k := range keys {
			ids = append(ids, int(k))
		}
		sort.Ints(ids)
		for _, k := range ids {
			fmt.Printf("%6d %-28s %v\n", k, raster.GeoKeyName(uint16(k)), keys[uint16(k)])
		}
	} else {
		fmt.Println("-- geokeys:", err)
	}

	vals, err := im.ReadAll()
	if err != nil {
		return err
	}
	nd, hasND := im.NoData()
	min, max, sum := math.Inf(1), math.Inf(-1), 0.0
	var n, nNaN, nND, nZero, nNeg int
	hist := map[float64]int{}
	for _, v := range vals {
		switch {
		case math.IsNaN(v):
			nNaN++
			continue
		case hasND && v == nd:
			nND++
			continue
		}
		if v == 0 {
			nZero++
		}
		if v < 0 {
			nNeg++
		}
		n++
		sum += v
		min, max = math.Min(min, v), math.Max(max, v)
		if len(hist) < 50 {
			hist[v]++
		}
	}
	fmt.Printf("-- values: total=%d valid=%d nan=%d nodata(%v,%v)=%d zero=%d negative=%d\n",
		len(vals), n, nNaN, hasND, nd, nND, nZero, nNeg)
	if n > 0 {
		fmt.Printf("   min=%g max=%g mean=%g\n", min, max, sum/float64(n))
	}
	// most frequent low-value "special" codes, useful to spot hidden nodata
	type kv struct {
		v float64
		c int
	}
	counts := map[float64]int{}
	for _, v := range vals {
		if !math.IsNaN(v) {
			counts[v]++
		}
	}
	var top []kv
	for v, c := range counts {
		top = append(top, kv{v, c})
	}
	sort.Slice(top, func(i, j int) bool { return top[i].c > top[j].c })
	if len(top) > 8 {
		top = top[:8]
	}
	fmt.Print("   most frequent:")
	for _, t := range top {
		fmt.Printf(" %g×%d", t.v, t.c)
	}
	fmt.Println()
	return nil
}

func trunc[T any](s []T) string {
	if len(s) > 12 {
		return fmt.Sprint(s[:12]) + fmt.Sprintf(" ...(%d)", len(s))
	}
	return fmt.Sprint(s)
}
