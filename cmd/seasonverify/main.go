// Command seasonverify scores the rain and hail forecasts on whole seasons
// of Radar-DPC files downloaded with dpcarchive. See docs/verification.md
// for the method and the results.
//
//	seasonverify cases  --from … --to … --out DIR [--storms 30 --random 10]
//	    pick rain cases and export them as .npy (6 past, 12 future frames)
//	seasonverify score  --cases DIR --irene DIR
//	    score the extrapolation, IRENE and persistence on exported cases
//	seasonverify hail   --from … --to … [--n 150 --cases-out FILE]
//	    pick the moments with the most hail and score the hail forecast
//	seasonverify hailfeat --cases FILE --out FILE
//	    write the training rows of the hail model
//	seasonverify sites  --from … --to … --cases FILE
//	    simulate hail warnings at points every 10 km over a season
//
// Every subcommand reads the archive from --dir (default
// ~/.cache/radarpoint). Times are RFC 3339.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func usage() {
	fmt.Fprintln(os.Stderr, `usage: seasonverify cases|score|hail|hailfeat|sites [flags]
run "seasonverify <subcommand> -h" for the flags of each`)
	os.Exit(2)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	home, _ := os.UserHomeDir()
	fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	dir := fs.String("dir", filepath.Join(home, ".cache", "radarpoint"), "archive directory written by dpcarchive")
	from := fs.String("from", "", "first instant, RFC 3339")
	to := fs.String("to", "", "end of the period, RFC 3339, excluded")
	workers := fs.Int("workers", 0, "parallel workers (default depends on the subcommand)")
	parse := func() archive {
		fs.Parse(os.Args[2:])
		return archive{dir: *dir}
	}
	period := func() (time.Time, time.Time) {
		f, err1 := time.Parse(time.RFC3339, *from)
		t, err2 := time.Parse(time.RFC3339, *to)
		if err1 != nil || err2 != nil {
			fmt.Fprintln(os.Stderr, "seasonverify: --from and --to are required, in RFC 3339")
			os.Exit(2)
		}
		return f.UTC(), t.UTC()
	}
	nWorkers := func(def int) int {
		if *workers > 0 {
			return *workers
		}
		return def
	}

	var err error
	switch os.Args[1] {
	case "cases":
		out := fs.String("out", "", "output directory (required)")
		storms := fs.Int("storms", 30, "storm cases: largest area of rain ≥ 10 mm/h")
		random := fs.Int("random", 10, "random moments with rain")
		seed := fs.Int64("seed", 2026, "seed for the random moments")
		a := parse()
		f, t := period()
		if *out == "" {
			usage()
		}
		err = rainCases(a, *out, f, t, *storms, *random, *seed)
	case "score":
		cases := fs.String("cases", "", "directory with cases.csv and the .npy cases (required)")
		irene := fs.String("irene", "", "directory with IRENE's _mean.npy and _prob.npy (required)")
		parse()
		if *cases == "" || *irene == "" {
			usage()
		}
		err = scoreRain(*cases, *irene, nWorkers(6))
	case "hail":
		n := fs.Int("n", 150, "number of cases")
		casesOut := fs.String("cases-out", "", "also write the case times to this file, for hailfeat and sites")
		a := parse()
		f, t := period()
		cases := hailCases(a, f, t, *n)
		if *casesOut != "" {
			if err = writeCases(*casesOut, cases); err != nil {
				break
			}
		}
		verifyHail(a, cases, nWorkers(3)) // about 1 GB per worker
	case "hailfeat":
		casesFile := fs.String("cases", "", "case times written by hail --cases-out (required)")
		out := fs.String("out", "", "output file of float32 rows (required)")
		a := parse()
		if *casesFile == "" || *out == "" {
			usage()
		}
		var cases []time.Time
		if cases, err = readCases(*casesFile); err == nil {
			err = hailFeatures(a, cases, *out, nWorkers(3))
		}
	case "sites":
		casesFile := fs.String("cases", "", "case times the hail model was fitted on (required, to report the other days)")
		mask := fs.String("mask", "internal/dpc/tempmask.png", "land mask")
		a := parse()
		f, t := period()
		if *casesFile == "" {
			usage()
		}
		var cases []time.Time
		if cases, err = readCases(*casesFile); err == nil {
			err = simulateSites(a, f, t, *mask, cases, nWorkers(8))
		}
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "seasonverify:", err)
		os.Exit(1)
	}
}
