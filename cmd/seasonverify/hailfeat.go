package main

// Training rows for the hail model (verify/hail_model.py): the signals of
// internal/hailrisk at pixels near storms without hail, and whether hail
// then reached them within 30 minutes.

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"math/rand"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/vmaltarello/radarpoint/internal/hailrisk"
	"github.com/vmaltarello/radarpoint/internal/nowcast"
)

// featNames are the float32 columns of each row: for each signal, the
// maximum along the pixel's trajectory and within 5 km; then VIL and POH
// now at the pixel; the label (hail within 30 minutes), the minutes to the
// first hail (-1 if none), the day index and the row weight.
var featNames = []string{
	"poh_max", "poh_max5", "vil_max", "vil_max5", "dvil_max", "dvil_max5",
	"etm_max", "etm_max5", "detm_max", "detm_max5", "vil_now", "poh_now",
	"label", "first_min", "day", "weight",
}

// hailRows writes the rows of one case: pixels with no hail now, data at
// every step and a storm within 25 km (VIL ≥ 2 kg/m² or POH > 0).
// Negatives are kept 1 in 4, with weight 4.
func hailRows(a archive, t time.Time, day int, w io.Writer) (int, error) {
	frames := a.sriFrames(t)
	if frames == nil {
		return 0, fmt.Errorf("missing SRI")
	}
	n, err := newEngine(hailrisk.Steps).Update(frames)
	if err != nil {
		return 0, err
	}
	in, ok := a.hailInputs(t)
	if !ok {
		return 0, fmt.Errorf("missing POH, VIL or ETM")
	}
	var obs []*nowcast.Field
	for k := 1; k <= hailrisk.Steps; k++ {
		f := a.field("POH", t.Add(time.Duration(k)*step))
		if f == nil {
			return 0, fmt.Errorf("missing POH +%d", k)
		}
		obs = append(obs, f)
	}
	sig := hailrisk.NewSignalField(n, in)
	wd, ht := in.POH.W, in.POH.H
	storm := make([]float32, wd*ht)
	for i := range storm {
		if in.VIL.V[i] >= 2 || in.POH.V[i] > 0 {
			storm[i] = 1
		}
	}
	near := integral(storm, wd, ht, 1)
	rng := rand.New(rand.NewSource(t.Unix()))
	row := make([]float32, len(featNames))
	buf := make([]byte, 4*len(featNames))
	rows := 0
	for r := range ht {
		for c := range wd {
			i := r*wd + c
			if p := in.POH.V[i]; p != p || p >= hailrisk.Hail || box(near, wd, ht, r, c, 25) == 0 {
				continue
			}
			first, ok := -1, true
			for k := range obs {
				v := obs[k].V[i]
				if v != v {
					ok = false
					break
				}
				if first < 0 && v >= hailrisk.Hail {
					first = k
				}
			}
			if !ok {
				continue
			}
			weight := float32(1)
			if first < 0 {
				if rng.Intn(4) != 0 {
					continue
				}
				weight = 4
			}
			s := sig.Pixel(i)
			for j := range hailrisk.NumSignals {
				row[2*j], row[2*j+1] = s.At[j], s.Near[j]
			}
			row[10], row[11] = s.VILNow, s.POHNow
			row[12], row[13] = 0, -1
			if first >= 0 {
				row[12], row[13] = 1, float32(5*(first+1))
			}
			row[14], row[15] = float32(day), weight
			for j, v := range row {
				binary.LittleEndian.PutUint32(buf[4*j:], math.Float32bits(v))
			}
			w.Write(buf)
			rows++
		}
	}
	return rows, nil
}

// hailFeatures writes the rows of every case to out. The day index counts
// the distinct days in order: the model is fitted on even ones.
func hailFeatures(a archive, cases []time.Time, out string, workers int) error {
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer f.Close()
	bw := bufio.NewWriter(f)
	days := map[string]int{}
	for _, t := range cases {
		if _, ok := days[t.Format("2006-01-02")]; !ok {
			days[t.Format("2006-01-02")] = len(days)
		}
	}
	var mu sync.Mutex
	total := 0
	parallel(cases, workers, func(t time.Time) bool {
		var buf bytes.Buffer
		rows, err := hailRows(a, t, days[t.Format("2006-01-02")], &buf)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", t.Format(time.RFC3339), err)
			return false
		}
		bw.Write(buf.Bytes())
		total += rows
		fmt.Fprintf(os.Stderr, "%s: %d rows\n", t.Format("2006-01-02 15:04"), rows)
		return true
	})
	if err := bw.Flush(); err != nil {
		return err
	}
	fmt.Printf("%d rows of %d float32: %s\n", total, len(featNames), strings.Join(featNames, ","))
	return nil
}
