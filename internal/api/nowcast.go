package api

import (
	"context"
	"errors"
	"math"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/vmaltarello/radarpoint/internal/dpc"
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
		Path:        "/nowcast",
		Summary:     "Rain forecast for the next hour at a point",
		Description: "Lead 0 is the latest observation, followed by one value per 5-minute step up to 60 minutes. " + nowcastNote,
	}, s.nowcast)
}

// Motion describes how the rain moves at the point.
type Motion struct {
	SpeedKmh  float64 `json:"speed_kmh"`
	TowardDeg float64 `json:"toward_deg" doc:"Direction the rain moves towards, degrees clockwise from north"`
}

// NowcastStep is the forecast at one lead time.
type NowcastStep struct {
	Time        time.Time `json:"time"`
	LeadMinutes int       `json:"lead_minutes"`
	Status      string    `json:"status" enum:"ok,nodata" doc:"nodata: the rain would come from outside radar coverage"`
	Value       *float64  `json:"value" doc:"Rain rate; null unless status is ok"`
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
	Method      string        `json:"method"`
	Note        string        `json:"note"`
	Attribution string        `json:"attribution"`
}

type nowcastOutput struct {
	Body NowcastBody
}

func (s *Server) nowcast(_ context.Context, in *PointParams) (*nowcastOutput, error) {
	if err := in.check(); err != nil {
		return nil, err
	}
	info, _ := dpc.Lookup("SRI")
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
	if n.Motion.Measured > 0 {
		if speed, toward, ok := n.Velocity(in.Lat, in.Lon); ok {
			out.Body.Motion = &Motion{SpeedKmh: math.Round(speed), TowardDeg: math.Round(toward)}
		}
	}
	for _, p := range pts {
		st := NowcastStep{Time: p.Time, LeadMinutes: int(p.Lead.Minutes()), Status: StatusNoData}
		if !math.IsNaN(p.Value) {
			v := math.Round(p.Value*100) / 100
			st.Status, st.Value = StatusOK, &v
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
