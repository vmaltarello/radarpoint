// Command tempmask builds the mask of the TEMP grid where Radar-DPC writes
// exactly 0 because there is no data (sea, outside Italy), as opposed to a
// real temperature of 0 °C.
//
// It downloads TEMP files spread over the last days and marks the pixels
// that are exactly 0 in all of them: a land pixel is never exactly
// 0.000 °C for days on end, day and night. The result is a 1-bit PNG,
// embedded in the binary by internal/dpc.
//
//	go run ./cmd/tempmask --days 13 --every 3h --out internal/dpc/tempmask.png
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"time"

	"github.com/vmaltarello/radarpoint/internal/dpc"
	"github.com/vmaltarello/radarpoint/internal/raster"
)

const userAgent = "radarpoint-tempmask/0.1 (+https://github.com/vmaltarello/radarpoint)"

func main() {
	days := flag.Int("days", 13, "how many days back to sample")
	every := flag.Duration("every", 3*time.Hour, "spacing of the sampled files")
	out := flag.String("out", "internal/dpc/tempmask.png", "output PNG")
	flag.Parse()
	if err := run(*days, *every, *out); err != nil {
		fmt.Fprintln(os.Stderr, "tempmask:", err)
		os.Exit(1)
	}
}

func run(days int, every time.Duration, out string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	c := dpc.New(userAgent)
	last, err := c.FindLast(ctx, "TEMP")
	if err != nil {
		return err
	}

	var zero []bool // still exactly 0 in every file read
	var ref *raster.GeoTIFF
	files := 0
	for t := last.Time; t.After(last.Time.Add(-time.Duration(days) * 24 * time.Hour)); t = t.Add(-every) {
		g, err := download(ctx, c, t)
		var apiErr *dpc.APIError
		if errors.Is(err, dpc.ErrNotFound) || (errors.As(err, &apiErr) && apiErr.Status >= 500) {
			continue
		}
		if err != nil {
			return err
		}
		vals, err := g.ReadAll()
		if err != nil {
			return err
		}
		if ref == nil {
			ref = g
			zero = make([]bool, len(vals))
			for i := range zero {
				zero[i] = true
			}
		} else if g.Width != ref.Width || g.Height != ref.Height || g.Transform != ref.Transform {
			return fmt.Errorf("%s: the TEMP grid changed", t.Format(time.RFC3339))
		}
		for i, v := range vals {
			if v != 0 && v != -99999 {
				zero[i] = false
			}
		}
		files++
		fmt.Fprintf(os.Stderr, "\r%d files", files)
	}
	fmt.Fprintln(os.Stderr)
	if files < 24 {
		return fmt.Errorf("only %d files: not enough to tell sea from 0 °C", files)
	}

	img := image.NewPaletted(image.Rect(0, 0, ref.Width, ref.Height), color.Palette{color.Gray{0}, color.Gray{255}})
	outside := 0
	for i, z := range zero {
		if z {
			img.Pix[i] = 1 // white: no data
			outside++
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return err
	}
	if err := os.WriteFile(out, buf.Bytes(), 0o644); err != nil {
		return err
	}
	t := ref.Transform
	fmt.Printf("%s: %dx%d, %d files, %.1f%% no data; grid origin %v, %v, pixel %v × %v\n",
		out, ref.Width, ref.Height, files, 100*float64(outside)/float64(len(zero)), t.OriginX, t.OriginY, t.PixelW, t.PixelH)
	return nil
}

func download(ctx context.Context, c *dpc.Client, t time.Time) (*raster.GeoTIFF, error) {
	dl, err := c.DownloadURL(ctx, "TEMP", t)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if _, err := c.Fetch(ctx, dl.URL, &buf); err != nil {
		return nil, err
	}
	im, err := raster.Parse(buf.Bytes())
	if err != nil {
		return nil, err
	}
	return raster.NewGeoTIFF(im)
}
