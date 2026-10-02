package irene

import "math"

// calibration4 is the observed frequency of rain (≥ 0.2 mm/h) when 0, 1, 2,
// 3 or all 4 members have rain, per 5-minute step. Measured on 120 cases of
// July–December 2020 from the IT-DPC-SRI archive, before IRENE's training
// period; the two halves of the cases agree within one percentage point.
// The raw share of members is overconfident: with 2 of 4 members it rains
// 43–49% of the time, with 3 of 4 62–68%.
var calibration4 = [12][5]float32{
	{0.005, 0.290, 0.485, 0.684, 0.969},
	{0.006, 0.272, 0.466, 0.664, 0.961},
	{0.008, 0.271, 0.467, 0.658, 0.953},
	{0.008, 0.262, 0.456, 0.649, 0.942},
	{0.010, 0.259, 0.453, 0.646, 0.931},
	{0.011, 0.260, 0.453, 0.652, 0.929},
	{0.012, 0.255, 0.439, 0.640, 0.915},
	{0.012, 0.255, 0.436, 0.636, 0.912},
	{0.014, 0.256, 0.434, 0.632, 0.906},
	{0.014, 0.252, 0.425, 0.624, 0.896},
	{0.015, 0.252, 0.424, 0.622, 0.893},
	{0.016, 0.257, 0.427, 0.623, 0.890},
}

// calibrate turns the share of members with rain at step k (0-based) into
// the observed frequency of rain. Only 4 members at 0.2 mm/h are calibrated;
// anything else is returned as is.
func calibrate(share float32, members int, threshold float32, k int) float32 {
	if members != 4 || threshold != 0.2 || k >= len(calibration4) {
		return share
	}
	level := int(math.Round(float64(share) * 4))
	return calibration4[k][min(max(level, 0), 4)]
}
