package main

// Scores of the rain forecasts on exported cases: the extrapolation of
// internal/nowcast, IRENE (outputs of verify/run_irene.py) and persistence.

import (
	"encoding/csv"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/vmaltarello/radarpoint/internal/nowcast"
)

const (
	leads   = 12
	extrap  = 0
	irene   = 1
	persist = 2
	nMeth   = 3
)

var (
	rainMethods  = [nMeth]string{"extrapolation", "IRENE", "persistence"}
	rainThr      = [2]float32{0.5, 5}
	rainFSSLeads = []int{2, 5, 11}             // +15, +30, +60
	rainScales   = []int{1, 5, 11, 21, 41, 81} // window side, km
	rainRegions  = []string{"Alps (≥45.7°N)", "North (44–45.7°N)", "Centre (41.8–44°N)", "South and Sicily", "Sardinia and west sea"}
)

// rainScores accumulates every measure for a set of cases.
type rainScores struct {
	n      int
	ct     [nMeth][leads][2][3]int64 // hits, misses, false alarms
	brier  [2][leads]float64         // extrapolation, IRENE
	nb     [leads]float64
	relCnt [2][leads][10]int64
	relWet [2][leads][10]int64
	fssNum [2][3][2][6]float64 // method, lead, threshold, scale
	fssDen [2][3][2][6]float64
	// Onset at points dry now (< 0.5 mm/h): rain within 30 and 60 minutes.
	onset [2][2][4]int64 // method, window, hits/misses/false alarms/correct negatives
	// Timing of the first wet step, for 60-minute onset hits: minutes.
	tAbs, tSigned [2]float64
	tN            [2]int64
}

func (s *rainScores) add(o *rainScores) {
	s.n += o.n
	for m := range nMeth {
		for k := range leads {
			for j := range 2 {
				for c := range 3 {
					s.ct[m][k][j][c] += o.ct[m][k][j][c]
				}
			}
		}
	}
	for m := range 2 {
		for k := range leads {
			s.brier[m][k] += o.brier[m][k]
			for b := range 10 {
				s.relCnt[m][k][b] += o.relCnt[m][k][b]
				s.relWet[m][k][b] += o.relWet[m][k][b]
			}
		}
		for l := range 3 {
			for j := range 2 {
				for c := range rainScales {
					s.fssNum[m][l][j][c] += o.fssNum[m][l][j][c]
					s.fssDen[m][l][j][c] += o.fssDen[m][l][j][c]
				}
			}
		}
		for w := range 2 {
			for c := range 4 {
				s.onset[m][w][c] += o.onset[m][w][c]
			}
		}
		s.tAbs[m] += o.tAbs[m]
		s.tSigned[m] += o.tSigned[m]
		s.tN[m] += o.tN[m]
	}
	for k := range leads {
		s.nb[k] += o.nb[k]
	}
}

// regionCT holds the contingency tables per region.
type regionCT [5][nMeth][leads][2][3]int64

func (r *regionCT) add(o *regionCT) {
	for g := range r {
		for m := range nMeth {
			for k := range leads {
				for j := range 2 {
					for c := range 3 {
						r[g][m][k][j][c] += o[g][m][k][j][c]
					}
				}
			}
		}
	}
}

func isNaN(v float32) bool { return v != v }

func count(t *[3]int64, f, o bool) {
	switch {
	case f && o:
		t[0]++
	case o:
		t[1]++
	case f:
		t[2]++
	}
}

// fss adds the fractions skill score terms of one forecast at every scale.
func rainFSS(fc, obs []float32, valid []bool, w, h int, th float32, num, den *[6]float64) {
	integral := func(v []float32) []float64 {
		s := make([]float64, (w+1)*(h+1))
		for r := range h {
			row := 0.0
			for c := range w {
				i := r*w + c
				if valid[i] && v[i] >= th {
					row++
				}
				s[(r+1)*(w+1)+c+1] = s[r*(w+1)+c+1] + row
			}
		}
		return s
	}
	sf, so := integral(fc), integral(obs)
	box := func(s []float64, r0, c0, r1, c1 int) float64 {
		return s[r1*(w+1)+c1] - s[r0*(w+1)+c1] - s[r1*(w+1)+c0] + s[r0*(w+1)+c0]
	}
	for si, n := range rainScales {
		half, area := n/2, float64(n*n)
		var a, b float64
		for r := range h {
			r0, r1 := max(r-half, 0), min(r+half+1, h)
			for c := range w {
				if !valid[r*w+c] {
					continue
				}
				c0, c1 := max(c-half, 0), min(c+half+1, w)
				pf, po := box(sf, r0, c0, r1, c1)/area, box(so, r0, c0, r1, c1)/area
				a += (pf - po) * (pf - po)
				b += pf*pf + po*po
			}
		}
		num[si] += a
		den[si] += b
	}
}

type rainResult struct {
	tag, category string
	s             rainScores
	reg           regionCT
}

func scoreRainCase(casesDir, ireneDir, tag string, region []int8) (*rainResult, error) {
	past, err := readNpy(filepath.Join(casesDir, tag+"_past.npy"))
	if err != nil {
		return nil, err
	}
	obs, err := readNpy(filepath.Join(casesDir, tag+"_obs.npy"))
	if err != nil {
		return nil, err
	}
	imean, err := readNpy(filepath.Join(ireneDir, tag+"_mean.npy"))
	if err != nil {
		return nil, err
	}
	iprob, err := readNpy(filepath.Join(ireneDir, tag+"_prob.npy"))
	if err != nil {
		return nil, err
	}
	base := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC) // any time: only the order matters
	var frames []nowcast.Frame
	for i, f := range past {
		frames = append(frames, nowcast.Frame{Time: base.Add(time.Duration(i) * step), Field: f})
	}
	n, err := nowcast.Compute(frames, dpcProj, dpcTransform, step, leads, nowcast.DefaultOptions)
	if err != nil {
		return nil, err
	}
	latest := past[len(past)-1]
	fc := [nMeth][]*nowcast.Field{n.ForecastFields(n.Latest, leads), imean, nil}
	prob := [2][]*nowcast.Field{n.ProbabilityFields(leads), iprob}
	w, h := latest.W, latest.H

	res := &rainResult{tag: tag}
	s := &res.s
	s.n = 1
	value := func(m, k, i int) float32 {
		if m == persist {
			return latest.V[i]
		}
		v := fc[m][k].V[i]
		if isNaN(v) {
			return 0
		}
		return v
	}
	valid := make([][]bool, leads)
	for k := range leads {
		valid[k] = make([]bool, w*h)
		for i, o := range obs[k].V {
			if isNaN(o) || isNaN(latest.V[i]) {
				continue
			}
			valid[k][i] = true
			for m := range nMeth {
				f := value(m, k, i)
				for j, t := range rainThr {
					count(&s.ct[m][k][j], f >= t, o >= t)
					count(&res.reg[region[i]][m][k][j], f >= t, o >= t)
				}
			}
			ow := 0.0
			if o >= 0.2 {
				ow = 1
			}
			for m := range 2 {
				p := float64(prob[m][k].V[i])
				if p != p {
					p = 0
				}
				b := min(int(p*10), 9)
				s.relCnt[m][k][b]++
				if ow == 1 {
					s.relWet[m][k][b]++
				}
				s.brier[m][k] += (p - ow) * (p - ow)
			}
			s.nb[k]++
		}
	}
	for l, k := range rainFSSLeads {
		for m := range 2 {
			for j, t := range rainThr {
				rainFSS(fc[m][k].V, obs[k].V, valid[k], w, h, t, &s.fssNum[m][l][j], &s.fssDen[m][l][j])
			}
		}
	}
	// Onset at points dry now and with data at every step.
	for i := range w * h {
		if isNaN(latest.V[i]) || latest.V[i] >= 0.5 {
			continue
		}
		ok := true
		for k := range leads {
			ok = ok && valid[k][i]
		}
		if !ok {
			continue
		}
		first := func(v func(k int) float32) int {
			for k := range leads {
				if v(k) >= 0.5 {
					return k
				}
			}
			return -1
		}
		ko := first(func(k int) float32 { return obs[k].V[i] })
		for m := range 2 {
			kf := first(func(k int) float32 { return value(m, k, i) })
			for wi, lim := range []int{6, 12} {
				count3 := &s.onset[m][wi]
				fw, ow := kf >= 0 && kf < lim, ko >= 0 && ko < lim
				switch {
				case fw && ow:
					count3[0]++
				case ow:
					count3[1]++
				case fw:
					count3[2]++
				default:
					count3[3]++
				}
			}
			if kf >= 0 && ko >= 0 {
				d := float64(5 * (kf - ko))
				s.tAbs[m] += math.Abs(d)
				s.tSigned[m] += d
				s.tN[m]++
			}
		}
	}
	return res, nil
}

func csi(c [3]int64) float64 {
	n := c[0] + c[1] + c[2]
	if n == 0 {
		return math.NaN()
	}
	return float64(c[0]) / float64(n)
}

func reportRain(title string, s *rainScores) {
	fmt.Printf("\n== %s: %d cases\n", title, s.n)
	fmt.Println("CSI   ≥0.5 mm/h: extrap  IRENE  persist.   ≥5 mm/h: extrap  IRENE  persist.")
	for k := range leads {
		fmt.Printf("+%2dm             %.3f  %.3f  %.3f              %.3f  %.3f  %.3f\n", (k+1)*5,
			csi(s.ct[0][k][0]), csi(s.ct[1][k][0]), csi(s.ct[2][k][0]),
			csi(s.ct[0][k][1]), csi(s.ct[1][k][1]), csi(s.ct[2][k][1]))
	}
	fmt.Printf("Brier (rain ≥0.2 mm/h) +15/+30/+60: extrap %.4f %.4f %.4f   IRENE %.4f %.4f %.4f\n",
		s.brier[0][2]/s.nb[2], s.brier[0][5]/s.nb[5], s.brier[0][11]/s.nb[11],
		s.brier[1][2]/s.nb[2], s.brier[1][5]/s.nb[5], s.brier[1][11]/s.nb[11])
}

func reportRainDetail(s *rainScores) {
	fmt.Println("\nProbability of rain (≥0.2 mm/h): observed frequency [share of pixels], +30 min")
	fmt.Println("forecast    extrapolation       IRENE")
	for b := range 10 {
		fmt.Printf("%3d–%3d%%", b*10, b*10+10)
		for m := range 2 {
			c := s.relCnt[m][5][b]
			if c == 0 {
				fmt.Printf("        –          ")
				continue
			}
			fmt.Printf("     %4.0f%% [%4.1f%%]", 100*float64(s.relWet[m][5][b])/float64(c), 100*float64(c)/s.nb[5])
		}
		fmt.Println()
	}
	fmt.Println("\nFractions skill score (1 = perfect) by window side")
	for j, t := range rainThr {
		fmt.Printf("≥%g mm/h   ", t)
		for _, n := range rainScales {
			fmt.Printf("  %3d km      ", n)
		}
		fmt.Println()
		for l, k := range rainFSSLeads {
			fmt.Printf("+%2dm      ", (k+1)*5)
			for c := range rainScales {
				e := 1 - s.fssNum[0][l][j][c]/s.fssDen[0][l][j][c]
				i := 1 - s.fssNum[1][l][j][c]/s.fssDen[1][l][j][c]
				fmt.Printf("  %.3f/%.3f", e, i)
			}
			fmt.Println("   (extrap/IRENE)")
		}
	}
	fmt.Println("\nOnset at points dry now: will it rain (≥0.5 mm/h) within …")
	fmt.Println("window   method          POD    FAR    CSI   (hits, misses, false alarms)")
	for wi, lab := range []string{"30 min", "60 min"} {
		for m := range 2 {
			c := s.onset[m][wi]
			h, mi, f := float64(c[0]), float64(c[1]), float64(c[2])
			fmt.Printf("%-8s %-14s  %.3f  %.3f  %.3f  (%d, %d, %d)\n", lab, rainMethods[m],
				h/(h+mi), f/(h+f), h/(h+mi+f), c[0], c[1], c[2])
		}
	}
	for m := range 2 {
		fmt.Printf("Onset time error, %s: mean |error| %.1f min, bias %+.1f min (positive = forecast late), %d points\n",
			rainMethods[m], s.tAbs[m]/float64(s.tN[m]), s.tSigned[m]/float64(s.tN[m]), s.tN[m])
	}
}

// scoreRain scores the extrapolation, IRENE and persistence on the cases of
// casesDir (cases.csv and .npy files) with IRENE's outputs in ireneDir.
func scoreRain(casesDir, ireneDir string, workers int) error {
	f, err := os.Open(filepath.Join(casesDir, "cases.csv"))
	if err != nil {
		return err
	}
	rows, err := csv.NewReader(f).ReadAll()
	f.Close()
	if err != nil {
		return err
	}
	category := map[string]string{}
	var tags []string
	for _, r := range rows[1:] {
		category[r[0]] = r[1]
		tags = append(tags, r[0])
	}
	sort.Strings(tags)

	const w, h = 1200, 1400
	region := make([]int8, w*h)
	for r := range h {
		for c := range w {
			x, y := dpcTransform.Center(c, r)
			lat, lon := dpcProj.Inverse(x, y)
			var g int8
			switch {
			case lat >= 45.7:
				g = 0
			case lat >= 44:
				g = 1
			case lat >= 41.8:
				g = 2
			case lon < 11.5:
				g = 4
			default:
				g = 3
			}
			region[r*w+c] = g
		}
	}

	var (
		mu    sync.Mutex
		total rainScores
		byCat = map[string]*rainScores{}
		reg   regionCT
		wg    sync.WaitGroup
		jobs  = make(chan string)
		done  int
	)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for tag := range jobs {
				res, err := scoreRainCase(casesDir, ireneDir, tag, region)
				mu.Lock()
				done++
				if err != nil {
					fmt.Fprintf(os.Stderr, "%s: %v\n", tag, err)
					mu.Unlock()
					continue
				}
				total.add(&res.s)
				c := category[tag]
				if byCat[c] == nil {
					byCat[c] = &rainScores{}
				}
				byCat[c].add(&res.s)
				reg.add(&res.reg)
				fmt.Fprintf(os.Stderr, "%d/%d %s\n", done, len(tags), tag)
				mu.Unlock()
			}
		}()
	}
	for _, t := range tags {
		jobs <- t
	}
	close(jobs)
	wg.Wait()

	reportRain("All cases", &total)
	reportRainDetail(&total)
	for _, c := range []string{"storm", "autumn", "winter", "random"} {
		if s := byCat[c]; s != nil {
			reportRain("Category "+c, s)
		}
	}
	fmt.Println("\n== By region (all cases), CSI extrap/IRENE")
	fmt.Println("region                    ≥0.5 +30m      ≥0.5 +60m      ≥5 +30m        ≥5 +60m")
	for g, name := range rainRegions {
		fmt.Printf("%-24s", name)
		for _, jk := range [][2]int{{0, 5}, {0, 11}, {1, 5}, {1, 11}} {
			j, k := jk[0], jk[1]
			fmt.Printf("  %.3f/%.3f", csi(reg[g][0][k][j]), csi(reg[g][1][k][j]))
		}
		fmt.Println()
	}
	return nil
}
