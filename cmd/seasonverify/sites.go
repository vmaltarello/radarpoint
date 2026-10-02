package main

// Hail warnings as a service would send them: points every 10 km over
// Italy, one warning at most per hour each, every 5 minutes of a season.

import (
	"fmt"
	"image/png"
	"math"
	"os"
	"slices"
	"sync/atomic"
	"time"

	"github.com/vmaltarello/radarpoint/internal/hailrisk"
)

const (
	cooldownSteps = 12 // one warning per hour at most, and episodes an hour apart
	warnSteps     = 6  // a warning is right if hail follows within 30 minutes
)

// Bits of siteStep.rules.
const (
	ruleNow   = 1 << iota // POH ≥ 50% within 5 km now
	rulePoint             // moved POH ≥ 50% at the point
	ruleNear              // moved POH ≥ 50% within 5 km
)

type siteStep struct {
	rules uint8
	p     float32 // hailrisk probability
}

// observed is the hail seen at every point at one time, and whether a
// warning was possible then (POH > 0 or VIL ≥ 10 kg/m² somewhere).
type observed struct {
	hail   []bool
	active bool
}

// variant is one way of deciding a warning.
type variant struct {
	name string
	on   func(siteStep) bool
}

// landMask reads the TEMP mask of internal/dpc (white: sea or abroad) and
// reports whether lat/lon is Italian land.
func landMask(path string) (func(lat, lon float64) bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		return nil, err
	}
	const lon0, lat0, pixel = 6, 47.50026321411133, 0.019983009747110096
	b := img.Bounds()
	return func(lat, lon float64) bool {
		x, y := int(math.Floor((lon-lon0)/pixel)), int(math.Floor((lat0-lat)/pixel))
		if x < 0 || y < 0 || x >= b.Dx() || y >= b.Dy() {
			return false
		}
		g, _, _, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
		return g <= 0x8000
	}, nil
}

func simulateSites(a archive, from, to time.Time, maskPath string, fitCases []time.Time, workers int) error {
	land, err := landMask(maskPath)
	if err != nil {
		return err
	}
	var times []time.Time
	for t := from; t.Before(to); t = t.Add(step) {
		times = append(times, t)
	}
	first := a.field("POH", times[0])
	if first == nil {
		return fmt.Errorf("no POH at %s", times[0].Format(time.RFC3339))
	}
	var site []int
	for r := 5; r < first.H; r += 10 {
		for c := 5; c < first.W; c += 10 {
			lat, lon := dpcProj.Inverse(dpcTransform.Center(c, r))
			if v := first.V[r*first.W+c]; land(lat, lon) && v == v {
				site = append(site, r*first.W+c)
			}
		}
	}
	fmt.Printf("%d points, %d times\n", len(site), len(times))

	// Pass 1: hail observed at every point, and the active times.
	obs := parallel(times, 8, func(t time.Time) observed {
		o := observed{hail: make([]bool, len(site))}
		f := a.field("POH", t)
		if f == nil {
			return o
		}
		for i, px := range site {
			o.hail[i] = f.V[px] >= hailrisk.Hail
		}
		o.active = slices.ContainsFunc(f.V, func(v float32) bool { return v > 0 })
		if !o.active {
			if vil := a.field("VIL", t); vil != nil {
				o.active = slices.ContainsFunc(vil.V, func(v float32) bool { return v >= 10 })
			}
		}
		return o
	})
	var active []int
	for k, o := range obs {
		if o.active {
			active = append(active, k)
		}
	}
	fmt.Printf("%d active times\n", len(active))

	// Pass 2: what each rule says at every point and active time. Active
	// times are split into contiguous chunks, each with its own engine so
	// motion pairs are reused, and only the trajectories near the points
	// are followed.
	warn := make([][]siteStep, len(times))
	var done atomic.Int64
	var chunks [][]int
	for w := range workers {
		chunks = append(chunks, active[w*len(active)/workers:(w+1)*len(active)/workers])
	}
	parallel(chunks, workers, func(chunk []int) bool {
		e := newEngine(hailrisk.Steps)
		for _, k := range chunk {
			t := times[k]
			frames := a.sriFrames(t)
			if frames == nil {
				continue
			}
			n, err := e.Update(frames)
			if err != nil {
				continue
			}
			in, ok := a.hailInputs(t)
			if !ok {
				continue
			}
			out := make([]siteStep, len(site))
			w := in.POH.W
			for i, px := range site {
				c, r := px%w, px/w
				s := hailrisk.SignalsAt(n, in, c, r)
				ss := siteStep{p: float32(s.Prob())}
				nowNear := float32(math.Inf(-1))
				for qr := max(r-hailrisk.Radius, 0); qr <= min(r+hailrisk.Radius, in.POH.H-1); qr++ {
					for qc := max(c-hailrisk.Radius, 0); qc <= min(c+hailrisk.Radius, w-1); qc++ {
						if v := in.POH.V[qr*w+qc]; v > nowNear {
							nowNear = v
						}
					}
				}
				if nowNear >= hailrisk.Hail {
					ss.rules |= ruleNow
				}
				if s.At[hailrisk.SigPOH] >= hailrisk.Hail {
					ss.rules |= rulePoint
				}
				if s.Near[hailrisk.SigPOH] >= hailrisk.Hail {
					ss.rules |= ruleNear
				}
				out[i] = ss
			}
			warn[k] = out
			if d := done.Add(1); d%500 == 0 {
				fmt.Fprintf(os.Stderr, "%d/%d active times\n", d, len(active))
			}
		}
		return true
	})

	// The model was fitted on the even days of its cases (see hailfeat).
	fitDays := map[string]bool{}
	var seen []string
	for _, t := range fitCases {
		if d := t.Format("2006-01-02"); !slices.Contains(seen, d) {
			if len(seen)%2 == 0 {
				fitDays[d] = true
			}
			seen = append(seen, d)
		}
	}

	variants := []variant{
		{"POH within 5 km now", func(s siteStep) bool { return s.rules&ruleNow != 0 }},
		{"moved POH at the point", func(s siteStep) bool { return s.rules&rulePoint != 0 }},
		{"moved POH within 5 km", func(s siteStep) bool { return s.rules&ruleNear != 0 }},
	}
	for _, th := range []float32{0.1, 0.2, 0.3, 0.4, 0.5} {
		variants = append(variants, variant{fmt.Sprintf("hail probability ≥ %.0f%%", th*100), func(s siteStep) bool { return s.p >= th }})
	}
	for _, subset := range []string{"all days", "days not used to fit the model"} {
		use := func(k int) bool { return subset == "all days" || !fitDays[times[k].Format("2006-01-02")] }
		reportSites(subset, times, site, obs, warn, use, variants)
	}
	return nil
}

// reportSites simulates the warnings of each variant and matches them with
// the hail episodes: hail at a point after at least an hour without.
func reportSites(subset string, times []time.Time, site []int, obs []observed, warn [][]siteStep, use func(int) bool, variants []variant) {
	type episode struct{ site, k int }
	var eps []episode
	for i := range site {
		last := -1000
		for k := range times {
			if !use(k) || !obs[k].hail[i] {
				continue
			}
			if k-last > cooldownSteps {
				eps = append(eps, episode{i, k})
			}
			last = k
		}
	}
	days := map[string]bool{}
	for k := range times {
		if use(k) {
			days[times[k].Format("2006-01-02")] = true
		}
	}
	fmt.Printf("\n== %s: %d days, %d points, %d hail episodes (%.2f per point)\n", subset, len(days), len(site), len(eps), float64(len(eps))/float64(len(site)))
	fmt.Println("rule                          warnings/point  right  wrong%  episodes: warned        late  missed  median lead")
	for _, v := range variants {
		var warnings, right int
		sent := make([][]int, len(site))
		for i := range site {
			last := -1000
			for k := range times {
				// No warning while hail is already falling at the point.
				if !use(k) || warn[k] == nil || !v.on(warn[k][i]) || obs[k].hail[i] || k-last < cooldownSteps {
					continue
				}
				last = k
				warnings++
				sent[i] = append(sent[i], k)
				for j := 1; j <= warnSteps && k+j < len(times); j++ {
					if obs[k+j].hail[i] {
						right++
						break
					}
				}
			}
		}
		var late, missed int
		var leads []int
		for _, e := range eps {
			lead := -1
			isLate := false
			for _, k := range sent[e.site] {
				if k < e.k && e.k-k <= warnSteps {
					lead = max(lead, e.k-k)
				}
				if k >= e.k && k-e.k <= warnSteps {
					isLate = true
				}
			}
			switch {
			case lead > 0:
				leads = append(leads, 5*lead)
			case isLate:
				late++
			default:
				missed++
			}
		}
		slices.Sort(leads)
		median := 0
		if len(leads) > 0 {
			median = leads[len(leads)/2]
		}
		fmt.Printf("%-30s %8.1f  %6d  %5.1f%%   %6d (%4.1f%%)  %5d  %6d     %3d min\n", v.name,
			float64(warnings)/float64(len(site)), right, 100*float64(warnings-right)/float64(max(warnings, 1)),
			len(leads), 100*float64(len(leads))/float64(max(len(eps), 1)), late, missed, median)
	}
}
