// Command radarpoint reads the latest Radar-DPC product value at a point.
//
//	radarpoint --product SRI --lat 45.5966 --lon 8.9150 [--keep]
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // Europe/Rome without relying on the system zoneinfo

	"github.com/vmaltarello/radarpoint/internal/dpc"
	"github.com/vmaltarello/radarpoint/internal/raster"
)

const userAgent = "radarpoint-prototype/0.1 (+https://github.com/vmaltarello/radarpoint)"

func main() {
	product := flag.String("product", "SRI", "Radar-DPC product type (SRI, POH, TEMP, …)")
	lat := flag.Float64("lat", 0, "WGS84 latitude in decimal degrees")
	lon := flag.Float64("lon", 0, "WGS84 longitude in decimal degrees")
	keep := flag.Bool("keep", false, "keep the downloaded .tif in the current directory")
	flag.Parse()

	latSet, lonSet := false, false
	flag.Visit(func(f *flag.Flag) {
		latSet = latSet || f.Name == "lat"
		lonSet = lonSet || f.Name == "lon"
	})
	if !latSet || !lonSet {
		fail(2, "--lat and --lon are required")
	}
	if *lat < -90 || *lat > 90 || *lon < -180 || *lon > 180 {
		fail(2, "invalid coordinates: %g, %g", *lat, *lon)
	}
	pt := strings.ToUpper(*product)
	if !slices.Contains(dpc.ValidTypes, pt) {
		fail(2, "invalid product %q; valid: %s", *product, strings.Join(dpc.ValidTypes, ", "))
	}
	if pt == "SITES" {
		fail(2, "SITES is a GeoJSON of radar sites, not a raster")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := run(ctx, pt, *lat, *lon, *keep); err != nil {
		fail(1, "%v", err)
	}
}

func run(ctx context.Context, productType string, lat, lon float64, keep bool) error {
	c := dpc.New(userAgent)

	t0 := time.Now()
	p, err := c.FindLast(ctx, productType)
	if err != nil {
		return err
	}
	dl, err := c.DownloadURL(ctx, productType, p.Time)
	if err != nil {
		return err
	}
	tAPI := time.Since(t0)

	t0 = time.Now()
	f, err := os.CreateTemp("", "radarpoint-*.tif")
	if err != nil {
		return err
	}
	defer func() {
		if !keep {
			os.Remove(f.Name())
		}
	}()
	size, err := c.Fetch(ctx, dl.URL, f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	tDown := time.Since(t0)

	t0 = time.Now()
	g, err := raster.OpenGeoTIFF(f.Name())
	if err != nil {
		return fmt.Errorf("reading GeoTIFF: %w", err)
	}
	v, col, row, valErr := g.ValueAt(lat, lon)
	tRead := time.Since(t0)

	keptAs := ""
	if keep {
		keptAs = productType + "_" + path.Base(dl.Key) // e.g. SRI_01-10-2026-12-35.tif
		if err := moveFile(f.Name(), keptAs); err != nil {
			return fmt.Errorf("saving %s: %w", keptAs, err)
		}
	}

	info, _ := dpc.Lookup(productType)
	rome, _ := time.LoadLocation("Europe/Rome")
	w := os.Stdout
	fmt.Fprintf(w, "Product:      %s", p.Type)
	if info.Description != "" {
		fmt.Fprintf(w, " (%s)", info.Description)
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "Data time:    %s UTC (%s Italian time), period %s\n",
		p.Time.Format("2006-01-02 15:04"), p.Time.In(rome).Format("15:04"), p.Period)
	fmt.Fprintf(w, "Point:        %.4f, %.4f\n", lat, lon)
	switch {
	case errors.Is(valErr, raster.ErrOutside):
		b := g.Bounds()
		fmt.Fprintf(w, "Pixel:        x=%d, y=%d (outside the %dx%d raster)\n", col, row, g.Width, g.Height)
		fmt.Fprintf(w, "Value:        ERROR: %v (area roughly lat %.2f…%.2f, lon %.2f…%.2f)\n", valErr,
			min(b[2][0], b[3][0]), max(b[0][0], b[1][0]), min(b[0][1], b[3][1]), max(b[1][1], b[2][1]))
	case valErr != nil:
		return fmt.Errorf("reading value: %w", valErr)
	default:
		fmt.Fprintf(w, "Pixel:        x=%d, y=%d\n", col, row)
		fmt.Fprintf(w, "Value:        %s\n", formatValue(info, v))
	}
	nd := "none in the file"
	if len(info.NoData) > 0 {
		nd = fmt.Sprintf("%g (inferred, not declared in the file)", info.NoData[0])
	}
	if v, ok := g.NoData(); ok {
		nd = strconv.FormatFloat(v, 'g', -1, 64)
	}
	fmt.Fprintf(w, "File:         %s, %dx%d px, 1 band %s, %s, nodata=%s\n",
		humanSize(size), g.Width, g.Height, g.DataType(), g.CompressionName(), nd)
	fmt.Fprintf(w, "Projection:   %s, pixel %.6g x %.6g\n", g.Projection, g.Transform.PixelW, g.Transform.PixelH)
	fmt.Fprintf(w, "Timings:      API %s, download %s, read %s\n", ms(tAPI), ms(tDown), ms(tRead))
	if keep {
		fmt.Fprintf(w, "Saved:        %s\n", keptAs)
	}
	fmt.Fprintf(w, "Source:       %s\n", dpc.Attribution)
	if errors.Is(valErr, raster.ErrOutside) {
		return valErr
	}
	return nil
}

func formatValue(info dpc.Info, v float64) string {
	if info.IsNoData(v) {
		return info.NoDataText
	}
	v = info.Display(v)
	s := strconv.FormatFloat(v, 'f', 2, 64)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	if s == "-0" {
		s = "0"
	}
	if info.Unit != "" {
		s += " " + info.Unit
	}
	if v == 0 && info.ZeroText != "" {
		s += " (" + info.ZeroText + ")"
	}
	return s
}

func humanSize(n int64) string {
	if n < 1<<20 {
		return fmt.Sprintf("%.0f kB", float64(n)/1e3)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/1e6)
}

func ms(d time.Duration) string { return fmt.Sprintf("%d ms", d.Milliseconds()) }

// moveFile renames src to dst, copying when they are on different filesystems.
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Remove(src)
}

func fail(code int, format string, args ...any) {
	fmt.Fprintf(os.Stderr, "radarpoint: "+format+"\n", args...)
	os.Exit(code)
}
