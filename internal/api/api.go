// Package api serves point queries over the frames held in a store.
//
//	GET /now?lat=45.5966&lon=8.915          latest value of every product
//	GET /history?lat=…&lon=…&product=SRI    all stored frames of one product
//	GET /healthz                            frames held per product
//	GET /docs, /openapi.json                interactive docs and OpenAPI schema
//
// It is built with Huma, which validates the parameters and generates the
// OpenAPI description. Responses always carry the Radar-DPC attribution.
package api

import (
	"cmp"
	"context"
	"errors"
	"math"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/vmaltarello/radarpoint/internal/dpc"
	"github.com/vmaltarello/radarpoint/internal/nowcast"
	"github.com/vmaltarello/radarpoint/internal/raster"
	"github.com/vmaltarello/radarpoint/internal/store"
)

// Value statuses.
const (
	StatusOK          = "ok"          // value is a measurement
	StatusNoData      = "nodata"      // the product has no data at this point
	StatusOutside     = "outside"     // the point is outside the product grid
	StatusUnavailable = "unavailable" // no frame downloaded yet
)

// Server answers HTTP queries. Products lists the product types to report.
type Server struct {
	Store    *store.Store
	Products []string
	// Nowcast returns the latest rain forecast, or nil; if Nowcast itself is
	// nil the /nowcast endpoint is not served.
	Nowcast func() *nowcast.Nowcast
	Version string           // shown in the OpenAPI description
	Now     func() time.Time // for tests; defaults to time.Now
}

// Handler returns the HTTP handler with all routes and the API docs.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	cfg := huma.DefaultConfig("radarpoint", cmp.Or(s.Version, "dev"))
	cfg.Info.Description = "Point values of Radar-DPC weather radar products. " +
		"Data: " + dpc.Attribution + "."
	api := humago.New(mux, cfg)

	huma.Register(api, huma.Operation{
		OperationID: "now",
		Method:      http.MethodGet,
		Path:        "/now",
		Summary:     "Latest value of every product at a point",
	}, s.now)
	huma.Register(api, huma.Operation{
		OperationID: "history",
		Method:      http.MethodGet,
		Path:        "/history",
		Summary:     "Values at a point for all frames held in memory",
		Description: "Frames are ordered from oldest to newest and cover the configured window (30 minutes by default).",
	}, s.history)
	productEnum(api, "/history", s.Products)
	if s.Nowcast != nil {
		s.registerNowcast(api)
	}
	huma.Register(api, huma.Operation{
		OperationID: "health",
		Method:      http.MethodGet,
		Path:        "/healthz",
		Summary:     "Frames held per product",
		Description: "Answers 503 until every product has at least one frame, and whenever a product is stale.",
	}, s.health)
	return cors(mux)
}

// PointParams are the query parameters shared by point queries.
type PointParams struct {
	Lat float64 `query:"lat" required:"true" minimum:"-90" maximum:"90" doc:"Latitude, WGS84 decimal degrees" example:"45.5966"`
	Lon float64 `query:"lon" required:"true" minimum:"-180" maximum:"180" doc:"Longitude, WGS84 decimal degrees" example:"8.915"`
}

func (p PointParams) check() error {
	if math.IsNaN(p.Lat) || math.IsNaN(p.Lon) {
		return huma.Error422UnprocessableEntity("lat and lon must be numbers")
	}
	return nil
}

// Reading is the value of one product frame at a point.
type Reading struct {
	Product     string     `json:"product" example:"SRI"`
	Description string     `json:"description,omitempty"`
	Time        *time.Time `json:"time,omitempty" doc:"Nominal time of the data, UTC"`
	AgeSeconds  *int64     `json:"age_seconds,omitempty" doc:"Seconds elapsed since time"`
	Stale       bool       `json:"stale" doc:"The data is older than it should be: Radar-DPC may have stopped publishing"`
	Status      string     `json:"status" enum:"ok,nodata,outside,unavailable"`
	Value       *float64   `json:"value" doc:"Measured value; null unless status is ok"`
	Unit        string     `json:"unit,omitempty" example:"mm/h"`
}

// PointBody is the response of point queries.
type PointBody struct {
	Lat         float64   `json:"lat"`
	Lon         float64   `json:"lon"`
	Readings    []Reading `json:"readings"`
	Attribution string    `json:"attribution"`
}

type pointOutput struct {
	Body PointBody
}

func (s *Server) now(_ context.Context, in *PointParams) (*pointOutput, error) {
	if err := in.check(); err != nil {
		return nil, err
	}
	out := &pointOutput{Body: PointBody{Lat: in.Lat, Lon: in.Lon, Attribution: dpc.Attribution, Readings: []Reading{}}}
	for _, p := range s.Products {
		out.Body.Readings = append(out.Body.Readings, s.read(p, s.Store.Latest(p), in.Lat, in.Lon))
	}
	return out, nil
}

type historyInput struct {
	PointParams
	Product string `query:"product" required:"true" doc:"Product type, one of those followed by the service" example:"SRI"`
}

func (s *Server) history(_ context.Context, in *historyInput) (*pointOutput, error) {
	if err := in.check(); err != nil {
		return nil, err
	}
	// The value was already checked against the enum set by productEnum.
	product := in.Product
	out := &pointOutput{Body: PointBody{Lat: in.Lat, Lon: in.Lon, Attribution: dpc.Attribution, Readings: []Reading{}}}
	for _, f := range s.Store.Frames(product) {
		out.Body.Readings = append(out.Body.Readings, s.read(product, f, in.Lat, in.Lon))
	}
	return out, nil
}

// productEnum restricts the "product" query parameter of the GET operation at
// path to the products actually followed, which are only known at run time.
// Huma shares the parameter schema between the OpenAPI document and request
// validation, so this updates both the docs and the accepted values.
func productEnum(api huma.API, path string, products []string) {
	op := api.OpenAPI().Paths[path].Get
	for _, p := range op.Parameters {
		if p.Name == "product" && p.In == "query" {
			p.Schema.Enum = nil
			for _, v := range products {
				p.Schema.Enum = append(p.Schema.Enum, v)
			}
			p.Schema.PrecomputeMessages()
		}
	}
}

// ProductHealth describes the frames held for one product.
type ProductHealth struct {
	Frames     int        `json:"frames"`
	Latest     *time.Time `json:"latest,omitempty"`
	AgeSeconds *int64     `json:"age_seconds,omitempty"`
	Stale      bool       `json:"stale"`
}

type healthOutput struct {
	Status int
	Body   struct {
		OK       bool                     `json:"ok" doc:"Every product has at least one frame, and none is stale"`
		Products map[string]ProductHealth `json:"products"`
	}
}

func (s *Server) health(_ context.Context, _ *struct{}) (*healthOutput, error) {
	out := &healthOutput{Status: http.StatusOK}
	out.Body.OK = true
	out.Body.Products = map[string]ProductHealth{}
	for _, p := range s.Products {
		frames := s.Store.Frames(p)
		h := ProductHealth{Frames: len(frames)}
		if len(frames) > 0 {
			f := frames[len(frames)-1]
			t := f.Time
			h.Latest, h.AgeSeconds, h.Stale = &t, s.age(t), s.stale(f)
		}
		if len(frames) == 0 || h.Stale {
			out.Body.OK = false
			out.Status = http.StatusServiceUnavailable
		}
		out.Body.Products[p] = h
	}
	return out, nil
}

func (s *Server) read(product string, f *store.Frame, lat, lon float64) Reading {
	info, _ := dpc.Lookup(product)
	rd := Reading{Product: product, Description: info.Description, Unit: info.Unit}
	if f == nil {
		rd.Status = StatusUnavailable
		return rd
	}
	t := f.Time
	rd.Time, rd.AgeSeconds, rd.Stale = &t, s.age(t), s.stale(f)
	v, _, _, err := f.Grid.ValueAt(lat, lon)
	switch {
	case errors.Is(err, raster.ErrOutside):
		rd.Status = StatusOutside
	case err != nil:
		// The frame decoded fine when it was downloaded, so a read error
		// here is a bug rather than a property of the point.
		rd.Status = StatusNoData
	case info.IsNoData(v):
		rd.Status = StatusNoData
	default:
		v = math.Round(v*100) / 100 // stored as float32: 2.0199999 → 2.02
		rd.Status, rd.Value = StatusOK, &v
	}
	return rd
}

func (s *Server) clock() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Server) age(t time.Time) *int64 {
	a := int64(s.clock().Sub(t).Seconds())
	return &a
}

// stale reports whether f is older than its product normally gets.
func (s *Server) stale(f *store.Frame) bool {
	info, _ := dpc.Lookup(f.Product)
	return info.Stale(f.Time, f.Period, s.clock())
}

// cors lets browser pages on other origins call the API, e.g. a map viewer.
func cors(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		h.ServeHTTP(w, r)
	})
}
