package main

// Scores of the hail forecast: POH moved four ways, against the POH
// observed later, on the moments with the most hail of a season.

import (
	"cmp"
	"fmt"
	"math"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/vmaltarello/radarpoint/internal/hailrisk"
	"github.com/vmaltarello/radarpoint/internal/nowcast"
)

const (
	hailLeads = 12
	nHailMeth = 4
)

var (
	hailMethods = [nHailMeth]string{"smoothed like rain", "moved, sharp", "moved, max nearby", "persistence"}
	hailThr     = [2]float32{0.5, 0.8}
	hailScales  = []int{1, 5, 11, 21, 41}
	hailFSSLead = []int{2, 5, 11} // +15, +30, +60 minutes
	warnRadii   = []int{0, 5, 10} // km around the point for a warning
)

// hailCases picks the moments with the largest area of POH ≥ 50% (the
// weaker of two consecutive frames, against single bad frames), at most 3
// a day and 2 hours apart, with every frame needed.
func hailCases(a archive, from, to time.Time, n int) []time.Time {
	var ts []time.Time
	for t := from; t.Before(to); t = t.Add(15 * time.Minute) {
		if a.exists("POH", t) && a.exists("POH", t.Add(-step)) {
			ts = append(ts, t)
		}
	}
	fmt.Printf("%d candidate moments\n", len(ts))
	type cand struct {
		t    time.Time
		area float64
	}
	scored := parallel(ts, 8, func(t time.Time) cand {
		now, before := a.field("POH", t), a.field("POH", t.Add(-step))
		if now == nil || before == nil {
			return cand{t, 0}
		}
		fn, ok1 := fractions(now, hailrisk.Hail)
		fb, ok2 := fractions(before, hailrisk.Hail)
		if !ok1 || !ok2 {
			return cand{t, 0}
		}
		return cand{t, min(fn[0], fb[0])}
	})
	slices.SortStableFunc(scored, func(a, b cand) int { return cmp.Compare(b.area, a.area) })
	var picked []time.Time
	perDay := map[string][]time.Time{}
	for _, c := range scored {
		if len(picked) == n || c.area == 0 {
			break
		}
		d := c.t.Format("2006-01-02")
		near := slices.ContainsFunc(perDay[d], func(o time.Time) bool { return c.t.Sub(o).Abs() < 2*time.Hour })
		if near || len(perDay[d]) == 3 || !a.complete("SRI", c.t, -5, 0) || !a.complete("POH", c.t, 0, hailLeads) ||
			!a.complete("VIL", c.t, -hailrisk.GrowthSteps, 0) || !a.complete("ETM", c.t, -hailrisk.GrowthSteps, 0) {
			continue
		}
		perDay[d] = append(perDay[d], c.t)
		picked = append(picked, c.t)
	}
	slices.SortFunc(picked, func(a, b time.Time) int { return a.Compare(b) })
	fmt.Printf("%d cases on %d days\n", len(picked), len(perDay))
	return picked
}

func readCases(path string) ([]time.Time, error) {
	lines, err := readLines(path)
	if err != nil {
		return nil, err
	}
	var ts []time.Time
	for _, l := range lines {
		t, err := time.Parse("2006-01-02 15:04", strings.TrimSpace(l))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		ts = append(ts, t)
	}
	return ts, nil
}

func writeCases(path string, ts []time.Time) error {
	var b strings.Builder
	for _, t := range ts {
		b.WriteString(t.Format("2006-01-02 15:04") + "\n")
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

type hailScores struct {
	n      int
	ct     [nHailMeth][hailLeads][2][3]int64 // method, lead, threshold: hits, misses, false alarms
	fssNum [nHailMeth][3][2][5]float64       // method, lead, threshold, scale
	fssDen [nHailMeth][3][2][5]float64
	// Warnings at points without hail now: hits, misses, false alarms per
	// method, threshold and radius, and minutes to the first hail for hits.
	warn    [nHailMeth][2][3][3]int64
	leadSum [nHailMeth][2][3]float64
}

func (s *hailScores) add(o *hailScores) {
	s.n += o.n
	for m := range nHailMeth {
		for k := range hailLeads {
			for j := range 2 {
				for c := range 3 {
					s.ct[m][k][j][c] += o.ct[m][k][j][c]
				}
			}
		}
		for l := range 3 {
			for j := range 2 {
				for c := range hailScales {
					s.fssNum[m][l][j][c] += o.fssNum[m][l][j][c]
					s.fssDen[m][l][j][c] += o.fssDen[m][l][j][c]
				}
			}
		}
		for j := range 2 {
			for r := range warnRadii {
				for c := range 3 {
					s.warn[m][j][r][c] += o.warn[m][j][r][c]
				}
				s.leadSum[m][j][r] += o.leadSum[m][j][r]
			}
		}
	}
}

// integral returns the summed-area table of v ≥ th (NaN counts as 0).
func integral(v []float32, w, h int, th float32) []float64 {
	s := make([]float64, (w+1)*(h+1))
	for r := range h {
		row := 0.0
		for c := range w {
			if v[r*w+c] >= th {
				row++
			}
			s[(r+1)*(w+1)+c+1] = s[r*(w+1)+c+1] + row
		}
	}
	return s
}

// box sums the summed-area table s over the square of half side half
// around (r, c), clipped to the grid.
func box(s []float64, w, h, r, c, half int) float64 {
	r0, r1 := max(r-half, 0), min(r+half+1, h)
	c0, c1 := max(c-half, 0), min(c+half+1, w)
	return s[r1*(w+1)+c1] - s[r0*(w+1)+c1] - s[r1*(w+1)+c0] + s[r0*(w+1)+c0]
}

// maxFilter returns the maximum of f over a square of side 2r+1 around
// each pixel, ignoring NaN.
func maxFilter(f *nowcast.Field, r int) *nowcast.Field {
	if r == 0 {
		return f
	}
	w, h := f.W, f.H
	pass := func(src []float32, horizontal bool) []float32 {
		out := make([]float32, len(src))
		for row := range h {
			for col := range w {
				m := float32(math.NaN())
				for d := -r; d <= r; d++ {
					c, rr := col, row
					if horizontal {
						c += d
					} else {
						rr += d
					}
					if c < 0 || c >= w || rr < 0 || rr >= h {
						continue
					}
					if v := src[rr*w+c]; v == v && (m != m || v > m) {
						m = v
					}
				}
				out[row*w+col] = m
			}
		}
		return out
	}
	return &nowcast.Field{W: w, H: h, V: pass(pass(f.V, true), false)}
}

func scoreHail(a archive, t time.Time) (*hailScores, error) {
	frames := a.sriFrames(t)
	if frames == nil {
		return nil, fmt.Errorf("missing SRI")
	}
	n, err := newEngine(hailLeads).Update(frames)
	if err != nil {
		return nil, err
	}
	poh0 := a.field("POH", t)
	if poh0 == nil {
		return nil, fmt.Errorf("missing POH")
	}
	var obs []*nowcast.Field
	for k := 1; k <= hailLeads; k++ {
		f := a.field("POH", t.Add(time.Duration(k)*step))
		if f == nil {
			return nil, fmt.Errorf("missing POH +%d", k)
		}
		obs = append(obs, f)
	}
	var fc [nHailMeth][]*nowcast.Field
	fc[0] = n.ForecastFields(poh0, hailLeads)
	fc[1] = n.WithHail(poh0).HailFields(hailLeads)
	for k := range hailLeads {
		fc[2] = append(fc[2], maxFilter(fc[1][k], (k+1)/2)) // the radius grows 1 km every 10 minutes
		fc[3] = append(fc[3], poh0)
	}
	w, h := poh0.W, poh0.H
	s := &hailScores{n: 1}
	val := func(f *nowcast.Field, i int) float32 {
		if v := f.V[i]; v == v {
			return v
		}
		return 0
	}
	for k := range hailLeads {
		for i, o := range obs[k].V {
			if o != o || poh0.V[i] != poh0.V[i] {
				continue
			}
			for m := range nHailMeth {
				f := val(fc[m][k], i)
				for j, th := range hailThr {
					count(&s.ct[m][k][j], f >= th, o >= th)
				}
			}
		}
	}
	for l, k := range hailFSSLead {
		for m := range nHailMeth {
			for j, th := range hailThr {
				sf, so := integral(fc[m][k].V, w, h, th), integral(obs[k].V, w, h, th)
				for si, sc := range hailScales {
					half, area := sc/2, float64(sc*sc)
					var num, den float64
					for r := range h {
						for c := range w {
							if o := obs[k].V[r*w+c]; o != o {
								continue
							}
							pf, po := box(sf, w, h, r, c, half)/area, box(so, w, h, r, c, half)/area
							num += (pf - po) * (pf - po)
							den += pf*pf + po*po
						}
					}
					s.fssNum[m][l][j][si] += num
					s.fssDen[m][l][j][si] += den
				}
			}
		}
	}
	// Warnings: the forecast reaches the threshold within the radius of
	// the point at some step of the next 30 minutes.
	for j, th := range hailThr {
		var fInt [nHailMeth][][]float64
		for m := range nHailMeth {
			for k := range hailrisk.Steps {
				fInt[m] = append(fInt[m], integral(fc[m][k].V, w, h, th))
			}
		}
		for r := range h {
			for c := range w {
				i := r*w + c
				if poh0.V[i] != poh0.V[i] || poh0.V[i] >= th {
					continue // no data, or hail already here
				}
				first, ok := -1, true
				for k := range hailrisk.Steps {
					o := obs[k].V[i]
					if o != o {
						ok = false
						break
					}
					if first < 0 && o >= th {
						first = k
					}
				}
				if !ok {
					continue
				}
				for m := range nHailMeth {
					for ri, rad := range warnRadii {
						warned := false
						for k := range hailrisk.Steps {
							if box(fInt[m][k], w, h, r, c, rad) > 0 {
								warned = true
								break
							}
						}
						cnt := &s.warn[m][j][ri]
						switch {
						case warned && first >= 0:
							cnt[0]++
							s.leadSum[m][j][ri] += float64(5 * (first + 1))
						case first >= 0:
							cnt[1]++
						case warned:
							cnt[2]++
						}
					}
				}
			}
		}
	}
	return s, nil
}

// verifyHail scores the cases, about 1 GB of memory per worker.
func verifyHail(a archive, cases []time.Time, workers int) {
	var total hailScores
	var mu sync.Mutex
	parallel(cases, workers, func(t time.Time) bool {
		s, err := scoreHail(a, t)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", t.Format(time.RFC3339), err)
			return false
		}
		total.add(s)
		fmt.Fprintf(os.Stderr, "%s scored\n", t.Format("2006-01-02 15:04"))
		return true
	})
	reportHail(&total)
}

func reportHail(s *hailScores) {
	fmt.Printf("\n== Hail (POH), %d cases\n", s.n)
	for i, name := range hailMethods {
		fmt.Printf("method %d: %s\n", i+1, name)
	}
	for j, th := range hailThr {
		fmt.Printf("\nCSI POH≥%.0f%%     1       2       3       4\n", th*100)
		for k := range hailLeads {
			fmt.Printf("+%2dm      ", (k+1)*5)
			for m := range nHailMeth {
				fmt.Printf("  %.3f", csi(s.ct[m][k][j]))
			}
			fmt.Println()
		}
	}
	fmt.Println("\nFractions skill score by window side")
	for j, th := range hailThr {
		for l, k := range hailFSSLead {
			fmt.Printf("POH≥%.0f%% +%2dm  ", th*100, (k+1)*5)
			for _, sc := range hailScales {
				fmt.Printf("  %5d km", sc)
			}
			fmt.Println()
			for m := range nHailMeth {
				fmt.Printf("  method %d      ", m+1)
				for c := range hailScales {
					fmt.Printf("     %.3f", 1-s.fssNum[m][l][j][c]/s.fssDen[m][l][j][c])
				}
				fmt.Println()
			}
		}
	}
	fmt.Println("\nWarnings at points without hail now: hail (POH ≥ threshold) at the point within 30 min?")
	fmt.Println("A warning is issued if the forecast reaches the threshold within the radius.")
	fmt.Println("threshold radius  method                 POD    FAR    CSI   warning time  (hits, misses, false alarms)")
	for j, th := range hailThr {
		for ri, rad := range warnRadii {
			for m := range nHailMeth {
				c := s.warn[m][j][ri]
				h, mi, f := float64(c[0]), float64(c[1]), float64(c[2])
				fmt.Printf("POH≥%.0f%%  %2d km  %-20s  %.3f  %.3f  %.3f   %5.1f min    (%d, %d, %d)\n", th*100, rad, hailMethods[m],
					h/(h+mi), f/(h+f), h/(h+mi+f), s.leadSum[m][j][ri]/h, c[0], c[1], c[2])
			}
		}
	}
}
