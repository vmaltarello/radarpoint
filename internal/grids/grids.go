// Package grids encodes the whole-grid values of every frame held by the
// service, observed and forecast, so map clients can draw them themselves.
//
// Each layer of each frame is one byte per pixel on the radar grid, rows
// from north to south, compressed with gzip: about 10–70 kB per frame on
// the 1200×1400 Radar-DPC grid. Byte 255 means no data; the other bytes
// decode with the table of the layer (see Values), so a client can upload a
// frame as an 8-bit texture and colour it with a 256-entry lookup.
package grids

import (
	"bytes"
	"compress/gzip"
	"math"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vmaltarello/radarpoint/internal/hailrisk"
	"github.com/vmaltarello/radarpoint/internal/irene"
	"github.com/vmaltarello/radarpoint/internal/nowcast"
)

// Layers.
const (
	Rain     = "rain"                   // rain rate, mm/h
	RainProb = "rain_probability"       // probability of rain, %; forecast only
	Hail     = "hail"                   // probability of hail (POH), %
	HailRisk = "hail_probability_30min" // probability of hail within 30 minutes, %; latest observation only
)

// NoData is the byte of pixels without data.
const NoData = 255

// Rain rates are stored on a logarithmic scale: 0 is under RainMin, 1…254
// go from RainMin to RainMax, about 3% apart. Larger rates are stored as 254.
const (
	RainMin = 0.1
	RainMax = 300.0
)

// Values returns the decoding table of a layer: the value of bytes 0…254.
func Values(layer string) []float64 {
	out := make([]float64, NoData)
	if layer != Rain {
		for b := range out {
			out[b] = float64(min(b, 100))
		}
		return out
	}
	for b := 1; b < NoData; b++ {
		out[b] = round3(RainMin * math.Pow(RainMax/RainMin, float64(b-1)/253))
	}
	return out
}

func round3(v float64) float64 {
	p := math.Pow(10, 3-math.Ceil(math.Log10(v)))
	return math.Round(v*p) / p
}

var rainLogRange = math.Log(RainMax / RainMin)

func rainByte(v float32) byte {
	switch {
	case v != v: // NaN
		return NoData
	case v < RainMin:
		return 0
	}
	b := 1 + math.Round(math.Log(float64(v)/RainMin)/rainLogRange*253)
	return byte(min(b, 254))
}

// percentByte stores a fraction 0–1 as a whole percentage.
func percentByte(v float32) byte {
	if v != v {
		return NoData
	}
	return byte(min(max(math.Round(float64(v)*100), 0), 100))
}

// Frame kinds.
const (
	Observed = "observed"
	Forecast = "forecast"
)

// Forecast methods of a Set.
const (
	Extrapolation = "extrapolation"
	IRENE         = "irene"
)

// Frame is one instant. Keys maps each layer available at that instant to
// the key of its data; a key never stands for two different contents, so
// clients may cache the data forever.
type Frame struct {
	ID          string // time as yyyymmddhhmm, UTC
	Time        time.Time
	Kind        string // Observed or Forecast
	LeadMinutes int    // minutes from the latest observation; ≤ 0 if observed
	Keys        map[string]string
}

// Set is every frame of one nowcast, from the oldest observation to the last
// forecast step, on the grid of the nowcast.
type Set struct {
	Nowcast *nowcast.Nowcast
	Method  string // Extrapolation or IRENE: source of the forecast rain
	Members int    // IRENE ensemble members, when Method is IRENE
	Frames  []Frame

	data map[string][]byte // layer + "/" + key → gzip data
}

// Data returns the gzip-compressed bytes of a layer, or nil.
func (s *Set) Data(layer, key string) []byte { return s.data[layer+"/"+key] }

// Inputs are what a Set is built from. Only Nowcast is required.
type Inputs struct {
	Nowcast *nowcast.Nowcast
	// Irene replaces the extrapolated rain and probability when it starts
	// from the same radar frame as Nowcast.
	Irene *irene.Forecast
	// Risk is used when it was computed for Nowcast.
	Risk *hailrisk.Risk
	// ObservedHail returns the probability of hail (0–1) observed at t, or
	// nil if unknown. It is asked only for observations not encoded yet.
	ObservedHail func(t time.Time) *nowcast.Field
}

// Builder encodes each new set of inputs once and keeps the result for
// concurrent readers. Observed frames never change, so they are encoded only
// the first time they are seen.
type Builder struct {
	mu       sync.Mutex // serialises Update
	last     Inputs
	observed map[string][]byte // encoded observed frames, by layer + "/" + key
	current  atomic.Pointer[Set]
}

// Current returns the latest set, or nil before the first one.
func (b *Builder) Current() *Set { return b.current.Load() }

// Update builds a new set from in, unless nothing changed since the last
// call. It reports whether a new set was stored.
func (b *Builder) Update(in Inputs) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := in.Nowcast
	if n == nil {
		return false
	}
	if !in.Irene.Matches(n) {
		in.Irene = nil
	}
	if in.Risk != nil && !in.Risk.Base.Equal(n.Base) {
		in.Risk = nil
	}
	if n == b.last.Nowcast && in.Irene == b.last.Irene && in.Risk == b.last.Risk {
		return false
	}
	set := &Set{Nowcast: n, Method: Extrapolation, data: map[string][]byte{}}
	if in.Irene != nil {
		set.Method, set.Members = IRENE, in.Irene.Members
	}
	baseID := frameID(n.Base)
	observed := map[string][]byte{}
	// Each layer is encoded as soon as its fields exist, so the forecast
	// fields of only one layer are held at a time.
	var jobs []job
	add := func(f *Frame, layer, key string, encode func() []byte) {
		f.Keys[layer] = key
		jobs = append(jobs, job{layer + "/" + key, f.Kind == Observed, encode})
	}
	flush := func() {
		b.run(jobs, set.data, observed)
		jobs = nil
	}

	for _, o := range n.Observed {
		f := Frame{ID: frameID(o.Time), Time: o.Time, Kind: Observed,
			LeadMinutes: int(o.Time.Sub(n.Base).Minutes()), Keys: map[string]string{}}
		field := o.Field
		add(&f, Rain, f.ID, func() []byte { return encode(field, rainByte) })
		if b.observed[Hail+"/"+f.ID] != nil {
			add(&f, Hail, f.ID, nil) // already encoded
		} else if hail := observedHail(n, o.Time, in.ObservedHail); hail != nil {
			add(&f, Hail, f.ID, func() []byte { return encode(hail, percentByte) })
		}
		if o.Time.Equal(n.Base) && in.Risk != nil {
			add(&f, HailRisk, f.ID, func() []byte { return encode(in.Risk.Prob, percentByte) })
		}
		set.Frames = append(set.Frames, f)
	}
	flush()

	forecast := make([]Frame, n.Steps)
	for k := range forecast {
		t := n.Base.Add(time.Duration(k+1) * n.Step)
		forecast[k] = Frame{ID: frameID(t), Time: t, Kind: Forecast, LeadMinutes: int(t.Sub(n.Base).Minutes()), Keys: map[string]string{}}
	}
	// Forecast data depends on the observation it starts from and on the
	// method, so both are part of the key. Hail is always extrapolated: its
	// key does not need the method.
	layer := func(name string, fields func() []*nowcast.Field, quantize func(float32) byte, method bool) {
		fs := fields()
		if fs == nil {
			return
		}
		for k := range forecast {
			key := forecast[k].ID + "-" + baseID
			if method {
				key += "-" + set.Method
			}
			add(&forecast[k], name, key, func() []byte { return encode(fs[k], quantize) })
		}
		flush()
	}
	layer(Rain, func() []*nowcast.Field {
		if in.Irene != nil {
			return coveredAll(in.Irene.Mean[:n.Steps], n.Latest)
		}
		return n.ForecastFields(n.Latest, n.Steps)
	}, rainByte, true)
	layer(RainProb, func() []*nowcast.Field {
		if in.Irene != nil {
			return coveredAll(in.Irene.Prob[:n.Steps], n.Latest)
		}
		return n.ProbabilityFields(n.Steps)
	}, percentByte, true)
	layer(Hail, func() []*nowcast.Field { return n.HailFields(n.Steps) }, percentByte, false)
	set.Frames = append(set.Frames, forecast...)

	b.observed, b.last = observed, in
	b.current.Store(set)
	return true
}

// run encodes jobs on all CPUs, reusing observed frames already encoded,
// and stores the results in data; observed ones also go to observed.
func (b *Builder) run(jobs []job, data, observed map[string][]byte) {
	out := make([][]byte, len(jobs))
	next := make(chan int, len(jobs))
	for i, j := range jobs {
		if d := b.observed[j.path]; d != nil {
			out[i] = d
		} else {
			next <- i
		}
	}
	close(next)
	var wg sync.WaitGroup
	for range runtime.GOMAXPROCS(0) {
		wg.Go(func() {
			for i := range next {
				out[i] = jobs[i].encode()
			}
		})
	}
	wg.Wait()
	for i, j := range jobs {
		data[j.path] = out[i]
		if j.observed {
			observed[j.path] = out[i]
		}
	}
}

type job struct {
	path     string
	observed bool
	encode   func() []byte
}

// observedHail returns the hail observed at t: the nowcast's own at its base
// time, from the callback otherwise.
func observedHail(n *nowcast.Nowcast, t time.Time, fn func(time.Time) *nowcast.Field) *nowcast.Field {
	if t.Equal(n.Base) && n.Hail != nil {
		return n.Hail
	}
	if fn == nil {
		return nil
	}
	return fn(t)
}

func frameID(t time.Time) string { return t.UTC().Format("200601021504") }

// encode quantizes f byte by byte and compresses the result.
func encode(f *nowcast.Field, quantize func(float32) byte) []byte {
	raw := make([]byte, len(f.V))
	for i, v := range f.V {
		raw[i] = quantize(v)
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(raw) // writes to a bytes.Buffer do not fail
	zw.Close()
	return buf.Bytes()
}

func coveredAll(fs []*nowcast.Field, latest *nowcast.Field) []*nowcast.Field {
	out := make([]*nowcast.Field, len(fs))
	for k, f := range fs {
		out[k] = covered(f, latest)
	}
	return out
}

// covered returns f with no data where the radar sees nothing now, as the
// extrapolation and the point forecast do.
func covered(f, latest *nowcast.Field) *nowcast.Field {
	out := &nowcast.Field{W: f.W, H: f.H, V: make([]float32, len(f.V))}
	for i, v := range f.V {
		if l := latest.V[i]; l != l { // NaN
			v = float32(math.NaN())
		}
		out.V[i] = v
	}
	return out
}
