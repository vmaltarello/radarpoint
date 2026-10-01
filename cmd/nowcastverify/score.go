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
