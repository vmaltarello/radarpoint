package main

import (
	"cmp"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/vmaltarello/radarpoint/internal/nowcast"
)

// Rain cases: 6 past and 12 future frames of SRI, exported as .npy for
// IRENE (verify/run_irene.py) and for the score subcommand.

type rainCandidate struct {
	t     time.Time
	score []float64 // shares of pixels at ≥ 0.5, ≥ 5 and ≥ 10 mm/h
}

// rainCases picks, at most one per day, the storms (largest area of rain
// ≥ 10 mm/h) and random moments with at least 1% of the area wet, and
// exports them to out with cases.csv.
func rainCases(a archive, out string, from, to time.Time, storms, random int, seed int64) error {
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	var ts []time.Time
	for t := from; t.Before(to); t = t.Add(30 * time.Minute) {
		if a.complete("SRI", t, -6, 12) {
			ts = append(ts, t)
		}
	}
	fmt.Printf("%d candidate moments\n", len(ts))
	scored := parallel(ts, 8, func(t time.Time) *rainCandidate {
		now, before := a.field("SRI", t), a.field("SRI", t.Add(-step))
		if now == nil || before == nil {
			return nil
		}
		fn, ok1 := fractions(now, 0.5, 5, 10)
		fb, ok2 := fractions(before, 0.5, 5, 10)
		if !ok1 || !ok2 {
			return nil
		}
		// The weaker of two consecutive frames, so a single bad frame
		// cannot make a moment look stormy.
		for i := range fn {
			fn[i] = min(fn[i], fb[i])
		}
		return &rainCandidate{t, fn}
	})
	scored = slices.DeleteFunc(scored, func(c *rainCandidate) bool { return c == nil })

	type pick struct {
		c   *rainCandidate
		cat string
	}
	var picked []pick
	days := map[string]bool{}
	take := func(pool []*rainCandidate, cat string, n int) {
		got := 0
		for _, c := range pool {
			if got == n {
				return
			}
			if d := c.t.Format("2006-01-02"); !days[d] && c.score[0] >= 0.01 {
				days[d] = true
				picked = append(picked, pick{c, cat})
				got++
			}
		}
	}
	// A few spare cases, in case some have a corrupted frame.
	byHeavy := slices.Clone(scored)
	slices.SortFunc(byHeavy, func(a, b *rainCandidate) int { return cmp.Compare(b.score[2], a.score[2]) })
	take(byHeavy, "storm", storms+3)
	pool := slices.DeleteFunc(slices.Clone(scored), func(c *rainCandidate) bool { return c.score[0] < 0.01 })
	rand.New(rand.NewSource(seed)).Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
	take(pool, "random", random+3)

	csv, err := os.Create(filepath.Join(out, "cases.csv"))
	if err != nil {
		return err
	}
	defer csv.Close()
	fmt.Fprintln(csv, "tag,category,wet05,wet5,wet10")
	want := map[string]int{"storm": storms, "random": random}
	have := map[string]int{}
	for _, p := range picked {
		if have[p.cat] == want[p.cat] {
			continue
		}
		var past, obs []*nowcast.Field
		for k := -5; k <= 12; k++ {
			f := a.field("SRI", p.c.t.Add(time.Duration(k)*step))
			if f == nil {
				break
			}
			if k <= 0 {
				past = append(past, f)
			} else {
				obs = append(obs, f)
			}
		}
		if len(past) != 6 || len(obs) != 12 || !clean(append(slices.Clone(past), obs...)) {
			fmt.Printf("%s %s: skipped, a frame is missing or corrupted\n", p.c.t.Format(time.RFC3339), p.cat)
			continue
		}
		tag := p.c.t.UTC().Format("200601021504")
		if err := writeNpy(filepath.Join(out, tag+"_past.npy"), past); err != nil {
			return err
		}
		if err := writeNpy(filepath.Join(out, tag+"_obs.npy"), obs); err != nil {
			return err
		}
		have[p.cat]++
		fmt.Fprintf(csv, "%s,%s,%.4f,%.4f,%.4f\n", tag, p.cat, p.c.score[0], p.c.score[1], p.c.score[2])
		fmt.Printf("%s %-6s heavy %.2f%%, wet %.1f%%\n", p.c.t.Format("2006-01-02 15:04"), p.cat, 100*p.c.score[2], 100*p.c.score[0])
	}
	fmt.Println("cases per category:", have)
	return nil
}

// clean reports that no frame has far more heavy rain (≥ 10 mm/h) than the
// window as a whole: the archive has a few corrupted frames with heavy
// rain almost everywhere.
func clean(frames []*nowcast.Field) bool {
	var counts []float64
	for _, f := range frames {
		var c float64
		for _, v := range f.V {
			if v >= 10 {
				c++
			}
		}
		counts = append(counts, c)
	}
	s := slices.Clone(counts)
	slices.Sort(s)
	return slices.Max(counts) <= 3*max(s[len(s)/2], 100)
}
