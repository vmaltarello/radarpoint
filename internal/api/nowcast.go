package api

import (
	"context"
	"errors"
	"math"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/vmaltarello/radarpoint/internal/dpc"
	"github.com/vmaltarello/radarpoint/internal/irene"
	"github.com/vmaltarello/radarpoint/internal/nowcast"
	"github.com/vmaltarello/radarpoint/internal/raster"
)

const nowcastNote = "Extrapolation of the observed motion of the rain (Lagrangian persistence): " +
	"rain is moved but never created, grown or dissipated, so new storms are not anticipated " +
	"and reliability decreases with lead time."

func (s *Server) registerNowcast(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "nowcast",
		Method:      http.MethodGet,
		Path:        Prefix + "/nowcast",
		Summary:     "Rain and hail forecast for the next hour at a point",
		Description: "Lead 0 is the latest observation, followed by one value per 5-minute step up to 60 minutes. " +
			"When POH is followed, the probability of hail is moved with the rain as well. " + nowcastNote,
	}, s.nowcast)
}

// Motion describes how the rain moves at the point.
type Motion struct {
	SpeedKmh  float64 `json:"speed_kmh"`
	TowardDeg float64 `json:"toward_deg" doc:"Direction the rain moves towards, degrees clockwise from north"`
}

// NowcastStep is the forecast at one lead time.
type NowcastStep struct {
	Time            time.Time `json:"time"`
	LeadMinutes     int       `json:"lead_minutes"`
	Status          string    `json:"status" enum:"ok,nodata" doc:"nodata: the rain would come from outside radar coverage"`
	Value           *float64  `json:"value" doc:"Rain rate; null unless status is ok"`
	HailPercent     *float64  `json:"hail_percent" doc:"Probability of hail in %; 0 means under 30%. Null when unknown: status not ok, or POH not followed or not yet matched to this forecast"`
	RainProbability *float64  `json:"rain_probability" doc:"Probability of rain (≥ 0.2 mm/h) in %, over a neighbourhood that grows with lead time; null where too little of it has radar data"`
}

// NowcastBody is the response of /nowcast.
type NowcastBody struct {
	Lat         float64       `json:"lat"`
	Lon         float64       `json:"lon"`
	Product     string        `json:"product" example:"SRI"`
	Unit        string        `json:"unit" example:"mm/h"`
	Status      string        `json:"status" enum:"ok,outside,unavailable" doc:"unavailable until two radar frames have been downloaded"`
	BaseTime    *time.Time    `json:"base_time,omitempty" doc:"Time of the latest observation the forecast starts from"`
	Stale       bool          `json:"stale" doc:"The forecast starts from data older than it should be"`
	Motion      *Motion       `json:"motion,omitempty" doc:"Absent when no rain could be tracked anywhere"`
	Steps       []NowcastStep `json:"steps"`
	Method      string        `json:"method" enum:"lagrangian-persistence,irene" doc:"Method that produced the forecast steps"`
	Members     int           `json:"members,omitempty" doc:"IRENE ensemble members, when method is irene"`
	Note        string        `json:"note"`
	Attribution string        `json:"attribution"`
}

// nowcastInput adds the choice of method to the point parameters.
type nowcastInput struct {
	PointParams
	Method string `query:"method" enum:"auto,irene,extrapolation" default:"auto" doc:"auto uses IRENE when its forecast for the latest radar frame is ready, the extrapolation otherwise"`
}

type nowcastOutput struct {
	Body NowcastBody
}

func (s *Server) nowcast(_ context.Context, in *nowcastInput) (*nowcastOutput, error) {
	if err := in.check(); err != nil {
		return nil, err
	}
	info, _ := dpc.Lookup("SRI")
	poh, _ := dpc.Lookup("POH")
	out := &nowcastOutput{Body: NowcastBody{
		Lat: in.Lat, Lon: in.Lon, Product: "SRI", Unit: info.Unit,
		Steps: []NowcastStep{}, Method: "lagrangian-persistence", Note: nowcastNote,
		Attribution: dpc.Attribution, Status: StatusUnavailable,
	}}
	n := s.currentNowcast()
	if n == nil {
		return out, nil
	}
	base := n.Base
	out.Body.BaseTime = &base
	out.Body.Stale = info.Stale(n.Base, n.Step, s.clock())
	pts, err := n.Forecast(in.Lat, in.Lon)
	if errors.Is(err, raster.ErrOutside) {
		out.Body.Status = StatusOutside
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	out.Body.Status = StatusOK
	fc, err := s.ireneFor(n, in.Method)
	if err != nil {
		return nil, err
	}
	if fc != nil {
		out.Body.Method, out.Body.Members, out.Body.Note = "irene", fc.Members, ireneNote
		applyIRENE(pts, fc, n, in.Lat, in.Lon)
	}
	if n.Motion.Measured > 0 {
		if speed, toward, ok := n.Velocity(in.Lat, in.Lon); ok {
			out.Body.Motion = &Motion{SpeedKmh: math.Round(speed), TowardDeg: math.Round(toward)}
		}
	}
	for _, p := range pts {
		st := NowcastStep{Time: p.Time, LeadMinutes: int(p.Lead.Minutes()), Status: StatusNoData}
		if !math.IsNaN(p.Probability) {
			pr := math.Round(p.Probability * 100)
			st.RainProbability = &pr
		}
		if !math.IsNaN(p.Value) {
			v := math.Round(p.Value*100) / 100
			st.Status, st.Value = StatusOK, &v
			if !math.IsNaN(p.Hail) {
				h := math.Round(poh.Display(p.Hail))
				st.HailPercent = &h
			}
		}
		out.Body.Steps = append(out.Body.Steps, st)
	}
	return out, nil
}

func (s *Server) currentNowcast() *nowcast.Nowcast {
	if s.Nowcast == nil {
		return nil
	}
	return s.Nowcast()
}

const ireneNote = "IRENE ensemble forecast (Fondazione Bruno Kessler): a neural network trained on " +
	"the Radar-DPC composite that also learns how rain grows and decays; value is the ensemble mean, " +
	"rain_probability the share of members with rain, calibrated on past cases. Motion and hail come from the extrapolation."

// ireneFor returns the IRENE forecast to use for n, or nil for the
// extrapolation. IRENE is used only if its forecast starts from the same
// radar frame as n; asking for it explicitly when it is not ready is an
// error.
func (s *Server) ireneFor(n *nowcast.Nowcast, method string) (*irene.Forecast, error) {
	if method == "extrapolation" {
		return nil, nil
	}
	var fc *irene.Forecast
	if s.Irene != nil {
		fc = s.Irene()
	}
	if fc != nil && (!fc.Base.Equal(n.Base) || len(fc.Mean) < n.Steps) {
		fc = nil
	}
	if fc == nil && method == "irene" {
		return nil, huma.Error503ServiceUnavailable("the IRENE forecast for the latest radar frame is not available")
	}
	return fc, nil
}

// applyIRENE replaces the forecast steps (not the observation at lead 0)
// with IRENE's mean and probability at the point. Where the radar sees
// nothing now, the steps stay without data, as with the extrapolation.
func applyIRENE(pts []nowcast.Point, fc *irene.Forecast, n *nowcast.Nowcast, lat, lon float64) {
	col, row, ok := n.PixelOf(lat, lon)
	if !ok {
		return
	}
	covered := n.Latest.At(col, row) == n.Latest.At(col, row) // not NaN
	for k := 1; k < len(pts) && k <= len(fc.Mean); k++ {
		if !covered {
			pts[k].Value, pts[k].Probability = math.NaN(), math.NaN()
			continue
		}
		pts[k].Value = float64(fc.Mean[k-1].At(col, row))
		pts[k].Probability = float64(fc.Prob[k-1].At(col, row))
	}
}
