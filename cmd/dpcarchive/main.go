// Command dpcarchive downloads every frame of some Radar-DPC products over
// a period, to verify forecasts on past seasons.
//
// Radar-DPC keeps about four and a half months of files; older ones are
// refused. Frames are saved as <dir>/<product>/<PRODUCT>_yyyymmddhhmm.tif,
// the layout of the nowcastverify cache, oldest first since those disappear
// first. Files already there are skipped, so an interrupted run resumes.
//
//	go run ./cmd/dpcarchive --from 2026-05-12T00:00:00Z --products POH,SRI,VIL,ETM
//
// A summer day of SRI, POH, VIL and ETM is about 1,150 files and 0.45 GB.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vmaltarello/radarpoint/internal/dpc"
)

const userAgent = "radarpoint-dpcarchive/0.1 (+https://github.com/vmaltarello/radarpoint)"

func main() {
	home, _ := os.UserHomeDir()
	from := flag.String("from", "", "first instant, RFC 3339 (required)")
	to := flag.String("to", "", "end of the period, RFC 3339, excluded (default: now)")
	products := flag.String("products", "SRI,POH,VIL,ETM", "comma-separated product types")
	dir := flag.String("dir", filepath.Join(home, ".cache", "radarpoint"), "archive directory")
	workers := flag.Int("workers", 6, "parallel downloads")
	step := flag.Duration("step", 5*time.Minute, "time between frames")
	flag.Parse()

	start, err := time.Parse(time.RFC3339, *from)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dpcarchive: --from:", err)
		os.Exit(2)
	}
	end := time.Now().UTC().Truncate(*step)
	if *to != "" {
		if end, err = time.Parse(time.RFC3339, *to); err != nil {
			fmt.Fprintln(os.Stderr, "dpcarchive: --to:", err)
			os.Exit(2)
		}
	}
	var types []string
	for p := range strings.SplitSeq(*products, ",") {
		p = strings.ToUpper(strings.TrimSpace(p))
		if !slices.Contains(dpc.ValidTypes, p) || p == "SITES" {
			fmt.Fprintln(os.Stderr, "dpcarchive: invalid product", p)
			os.Exit(2)
		}
		types = append(types, p)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	a := &archiver{client: dpc.New(userAgent), dir: *dir}
	a.run(ctx, start.UTC(), end.UTC(), *step, types, *workers)
}

type archiver struct {
	client *dpc.Client
	dir    string

	got, have, gone, failed, bytes atomic.Int64
}

type job struct {
	product string
	t       time.Time
	path    string
}

func (a *archiver) run(ctx context.Context, from, to time.Time, step time.Duration, types []string, workers int) {
	jobs := make(chan job)
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for j := range jobs {
				a.fetch(ctx, j)
			}
		})
	}
	began := time.Now()
	// Day by day, all products of a day before the next one.
	for day := from.Truncate(24 * time.Hour); day.Before(to) && ctx.Err() == nil; day = day.Add(24 * time.Hour) {
		for _, p := range types {
			sub := filepath.Join(a.dir, strings.ToLower(p))
			if err := os.MkdirAll(sub, 0o755); err != nil {
				fmt.Fprintln(os.Stderr, "dpcarchive:", err)
				os.Exit(1)
			}
			for t := day; t.Before(day.Add(24*time.Hour)) && t.Before(to); t = t.Add(step) {
				if t.Before(from) {
					continue
				}
				path := filepath.Join(sub, p+"_"+t.Format("200601021504")+".tif")
				if _, err := os.Stat(path); err == nil {
					a.have.Add(1)
					continue
				}
				select {
				case jobs <- job{p, t, path}:
				case <-ctx.Done():
				}
			}
		}
		a.report(day.Format("2006-01-02"), began)
	}
	close(jobs)
	wg.Wait()
	a.report("done", began)
}

func (a *archiver) fetch(ctx context.Context, j job) {
	d, err := a.client.DownloadURL(ctx, j.product, j.t)
	if err != nil {
		if expired(err) {
			a.gone.Add(1)
			return
		}
		a.failed.Add(1)
		fmt.Fprintf(os.Stderr, "%s %s: %v\n", j.product, j.t.Format(time.RFC3339), err)
		return
	}
	var buf bytes.Buffer
	if _, err := a.client.Fetch(ctx, d.URL, &buf); err != nil {
		a.failed.Add(1)
		fmt.Fprintf(os.Stderr, "%s %s: %v\n", j.product, j.t.Format(time.RFC3339), err)
		return
	}
	// Write under a temporary name, so an interrupted run leaves no partial
	// file that the next run would skip.
	tmp := j.path + ".part"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		a.failed.Add(1)
		fmt.Fprintln(os.Stderr, "dpcarchive:", err)
		return
	}
	if err := os.Rename(tmp, j.path); err != nil {
		a.failed.Add(1)
		fmt.Fprintln(os.Stderr, "dpcarchive:", err)
		return
	}
	a.got.Add(1)
	a.bytes.Add(int64(buf.Len()))
}

// expired reports whether the API refused a frame because it is no longer
// kept: it answers 500 with the storage's 403 or 404 in the message.
func expired(err error) bool {
	var apiErr *dpc.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	if apiErr.Status == http.StatusNotFound || apiErr.Status == http.StatusForbidden {
		return true
	}
	return apiErr.Status == http.StatusInternalServerError &&
		(strings.Contains(apiErr.Message, "403") || strings.Contains(apiErr.Message, "404"))
}

func (a *archiver) report(label string, began time.Time) {
	fmt.Printf("%s  downloaded %d (%.1f GB), already there %d, no longer kept %d, failed %d, %s\n",
		label, a.got.Load(), float64(a.bytes.Load())/1e9, a.have.Load(), a.gone.Load(), a.failed.Load(),
		time.Since(began).Round(time.Second))
}
