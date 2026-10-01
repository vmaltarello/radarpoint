package main

import "math"

// table counts hits, misses and false alarms of a yes/no forecast of rain
// above a threshold, over pixels where forecast and observation have data.
type table struct {
	hits, misses, falseAlarms int
}

func (t *table) add(fc, obs []float32, threshold float32) {
	for i, o := range obs {
		f := fc[i]
		if o != o || f != f { // NaN: outside coverage
			continue
		}
		switch fr, or := f >= threshold, o >= threshold; {
		case fr && or:
			t.hits++
		case or:
			t.misses++
		case fr:
			t.falseAlarms++
		}
	}
}

// csi is the critical success index, hits / (hits + misses + false alarms):
// 1 is a perfect forecast, 0 no skill. NaN when no rain was forecast or seen.
func (t table) csi() float64 {
	n := t.hits + t.misses + t.falseAlarms
	if n == 0 {
		return math.NaN()
	}
	return float64(t.hits) / float64(n)
}

// rainShare is the percentage of covered pixels with rain ≥ threshold.
func rainShare(v []float32, threshold float32) float64 {
	var rain, valid int
	for _, x := range v {
		if x == x {
			valid++
			if x >= threshold {
				rain++
			}
		}
	}
	return 100 * float64(rain) / float64(max(valid, 1))
}

// reliability compares a probability forecast with what happened: for each
// tenth of probability, how often it then rained. A reliable forecast says
// 70% where it rains 7 times out of 10. It also adds up the Brier score of
// the probability and of the yes/no forecast, for comparison (lower is
// better).
type reliability struct {
	count, wet [10]int64
	brier      float64 // sum of (p - o)²
	brierYesNo float64 // the same for the yes/no forecast
	n          int64
}

func (r *reliability) add(prob, fc, obs []float32, threshold float32) {
	for i, o := range obs {
		p, f := prob[i], fc[i]
		if o != o || p != p || f != f {
			continue
		}
		var ov, fv float64
		if o >= threshold {
			ov = 1
		}
		if f >= threshold {
			fv = 1
		}
		b := min(int(p*10), 9)
		r.count[b]++
		if ov == 1 {
			r.wet[b]++
		}
		r.brier += (float64(p) - ov) * (float64(p) - ov)
		r.brierYesNo += (fv - ov) * (fv - ov)
		r.n++
	}
}

// observed is the observed frequency of rain in bin b, NaN if empty.
func (r *reliability) observed(b int) float64 {
	if r.count[b] == 0 {
		return math.NaN()
	}
	return float64(r.wet[b]) / float64(r.count[b])
}
