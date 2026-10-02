// Package irene talks to the IRENE nowcasting service (see irene/ in the
// repository) and keeps its latest forecast.
//
// IRENE is a neural network by Fondazione Bruno Kessler trained on the
// Radar-DPC composite: from the last 6 frames it produces an ensemble of
// forecasts for the next hour. The service returns, per 5-minute step, the
// ensemble mean rain rate and the share of members with rain, which the
// client turns into a calibrated probability.
package irene

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/vmaltarello/radarpoint/internal/nowcast"
)

// PastFrames is the number of frames IRENE takes as input.
const PastFrames = 6

// Client calls an IRENE service.
type Client struct {
	URL       string // base URL, e.g. http://irene:8000
	Members   int    // ensemble members; more is better and slower
	Threshold float32
	HTTP      *http.Client
}

// NewClient returns a client with 4 members, a 0.2 mm/h rain threshold for
// the probability and a timeout fit for a CPU forecast of the whole grid.
func NewClient(url string) *Client {
	return &Client{URL: url, Members: 4, Threshold: 0.2, HTTP: &http.Client{Timeout: 10 * time.Minute}}
}

// Forecast is IRENE's forecast from the frames ending at Base.
type Forecast struct {
	Base    time.Time
	Step    time.Duration
	Members int
	Mean    []*nowcast.Field // ensemble mean rain rate per step, mm/h
	Prob    []*nowcast.Field // calibrated probability of rain per step, 0–1
	Took    time.Duration    // time spent by the service
}

// Forecast sends the past frames (oldest first, all on the same grid) and
// returns the forecast for the given number of steps.
func (c *Client) Forecast(ctx context.Context, past []*nowcast.Field, base time.Time, step time.Duration, steps int) (*Forecast, error) {
	if len(past) != PastFrames {
		return nil, fmt.Errorf("irene: %d past frames, need %d", len(past), PastFrames)
	}
	w, h := past[0].W, past[0].H
	var body bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&body, gzip.BestSpeed)
	buf := make([]byte, 4*w*h)
	for _, f := range past {
		if f.W != w || f.H != h {
			return nil, errors.New("irene: past frames on different grids")
		}
		for i, v := range f.V {
			binary.LittleEndian.PutUint32(buf[4*i:], math.Float32bits(v))
		}
		zw.Write(buf)
	}
	zw.Close()

	url := fmt.Sprintf("%s/forecast?height=%d&width=%d&members=%d&steps=%d&threshold=%g",
		c.URL, h, w, c.Members, steps, c.Threshold)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Encoding", "gzip")
	// Accept-Encoding is left to the transport, which then decompresses.
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("irene: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("irene: HTTP %d: %s", resp.StatusCode, bytes.TrimSpace(msg))
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("irene: %w", err)
	}
	n := w * h
	if len(data) != steps*5*n {
		return nil, fmt.Errorf("irene: got %d bytes, want %d", len(data), steps*5*n)
	}

	fc := &Forecast{Base: base, Step: step, Members: c.Members}
	if s, err := strconv.ParseFloat(resp.Header.Get("X-Seconds"), 64); err == nil {
		fc.Took = time.Duration(s * float64(time.Second))
	}
	for k := range steps {
		block := data[k*5*n : (k+1)*5*n]
		mean := &nowcast.Field{W: w, H: h, V: make([]float32, n)}
		prob := &nowcast.Field{W: w, H: h, V: make([]float32, n)}
		for i := range n {
			mean.V[i] = math.Float32frombits(binary.LittleEndian.Uint32(block[4*i:]))
			prob.V[i] = calibrate(float32(block[4*n+i])/100, c.Members, c.Threshold, k)
		}
		fc.Mean, fc.Prob = append(fc.Mean, mean), append(fc.Prob, prob)
	}
	return fc, nil
}
