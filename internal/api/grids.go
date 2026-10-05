package api

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/vmaltarello/radarpoint/internal/dpc"
	"github.com/vmaltarello/radarpoint/internal/geo"
	"github.com/vmaltarello/radarpoint/internal/grids"
	"github.com/vmaltarello/radarpoint/internal/nowcast"
)

func (s *Server) registerGrids(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "grids",
		Method:      http.MethodGet,
		Path:        Prefix + "/grids",
		Summary:     "Whole-grid values of the observed and forecast frames",
		Description: "Lists the frames held in memory, from the oldest observation to the last forecast step, " +
			"with the URL of each layer's data, the grid georeferencing and how to decode the bytes. " +
			"It is meant for clients that draw their own maps.",
	}, s.grids)
	huma.Register(api, huma.Operation{
		OperationID: "grid-data",
		Method:      http.MethodGet,
		Path:        Prefix + "/grids/{layer}/{key}",
		Summary:     "Values of one layer of one frame",
		Description: "One byte per pixel, rows from north to south, gzip-compressed (Content-Encoding: gzip). " +
			"Byte 255 is no data; the others decode with the layer's values in /grids. " +
			"The content of a URL never changes; once the frame leaves the window it answers 404.",
	}, s.gridData)
}

// GridProjection describes the projection of the grid coordinates.
type GridProjection struct {
	Name          string  `json:"name" enum:"transverse_mercator,geographic"`
	Proj4         string  `json:"proj4" doc:"The projection as a PROJ string, e.g. for proj4js"`
	Lat0          float64 `json:"lat0,omitempty"`
	Lon0          float64 `json:"lon0,omitempty"`
	K0            float64 `json:"k0,omitempty"`
	FalseEasting  float64 `json:"false_easting"`
	FalseNorthing float64 `json:"false_northing"`
	SemiMajorAxis float64 `json:"semi_major_axis" doc:"Ellipsoid semi-major axis, metres"`
	InvFlattening float64 `json:"inverse_flattening"`
}

// Grid places the pixels: pixel (col, row) covers x from OriginX+col·PixelW
// to OriginX+(col+1)·PixelW and y from OriginY−row·PixelH down to
// OriginY−(row+1)·PixelH, in projection units.
type Grid struct {
	Width      int            `json:"width"`
	Height     int            `json:"height"`
	Projection GridProjection `json:"projection"`
	OriginX    float64        `json:"origin_x" doc:"x of the west edge of the first column"`
	OriginY    float64        `json:"origin_y" doc:"y of the north edge of the first row"`
	PixelW     float64        `json:"pixel_width"`
	PixelH     float64        `json:"pixel_height"`
	Bounds     Bounds         `json:"bounds" doc:"Smallest lat/lon box containing the grid"`
}

// Bounds is a box in degrees.
type Bounds struct {
	West  float64 `json:"west"`
	South float64 `json:"south"`
	East  float64 `json:"east"`
	North float64 `json:"north"`
}

// GridLayer describes one layer and how to decode its bytes.
type GridLayer struct {
	Description string    `json:"description"`
	Unit        string    `json:"unit"`
	Values      []float64 `json:"values" doc:"Value of bytes 0…254, in unit; 255 is no data"`
}

// GridFrame is one instant and the URL of each layer available then.
type GridFrame struct {
	Time        time.Time         `json:"time"`
	Kind        string            `json:"kind" enum:"observed,forecast"`
	LeadMinutes int               `json:"lead_minutes" doc:"Minutes from the latest observation; negative for older observations"`
	Layers      map[string]string `json:"layers" doc:"URL of the data of each layer, relative to the server"`
}

// GridsBody is the response of /grids.
type GridsBody struct {
	Status      string               `json:"status" enum:"ok,unavailable" doc:"unavailable until two radar frames have been downloaded"`
	BaseTime    *time.Time           `json:"base_time,omitempty" doc:"Time of the latest observation"`
	Stale       bool                 `json:"stale" doc:"The latest observation is older than it should be"`
	Method      string               `json:"method,omitempty" enum:"extrapolation,irene" doc:"Source of the forecast rain and rain probability; hail is always extrapolated"`
	Members     int                  `json:"members,omitempty" doc:"IRENE ensemble members, when method is irene"`
	Grid        *Grid                `json:"grid,omitempty"`
	Layers      map[string]GridLayer `json:"layers"`
	Frames      []GridFrame          `json:"frames"`
	Attribution string               `json:"attribution"`
}

type gridsOutput struct {
	CacheControl string `header:"Cache-Control"`
	Body         GridsBody
}

func (s *Server) grids(_ context.Context, _ *struct{}) (*gridsOutput, error) {
	sri, _ := dpc.Lookup("SRI")
	out := &gridsOutput{CacheControl: "no-cache", Body: GridsBody{
		Status: StatusUnavailable, Frames: []GridFrame{}, Attribution: dpc.Attribution,
		Layers: map[string]GridLayer{
			grids.Rain:     {Description: "rain rate at ground level", Unit: "mm/h"},
			grids.RainProb: {Description: "probability of rain (≥ 0.2 mm/h) nearby, forecast steps only", Unit: "%"},
			grids.Hail:     {Description: "probability of hail; 0 means under 30%", Unit: "%"},
			grids.HailRisk: {Description: "probability that hail (POH ≥ 50%) arrives within 30 minutes, latest observation only", Unit: "%"},
		},
	}}
	for name, l := range out.Body.Layers {
		l.Values = grids.Values(name)
		out.Body.Layers[name] = l
	}
	set := s.gridSet()
	if set == nil {
		return out, nil
	}
	n := set.Nowcast
	base := n.Base
	out.Body.Status, out.Body.BaseTime, out.Body.Method, out.Body.Members = StatusOK, &base, set.Method, set.Members
	out.Body.Stale = sri.Stale(n.Base, n.Step, s.clock())
	out.Body.Grid = gridOfNowcast(n)
	for _, f := range set.Frames {
		gf := GridFrame{Time: f.Time, Kind: f.Kind, LeadMinutes: f.LeadMinutes, Layers: map[string]string{}}
		for layer, key := range f.Keys {
			gf.Layers[layer] = Prefix + "/grids/" + layer + "/" + key
		}
		out.Body.Frames = append(out.Body.Frames, gf)
	}
	return out, nil
}

type gridDataInput struct {
	Layer          string `path:"layer" enum:"rain,rain_probability,hail,hail_probability_30min"`
	Key            string `path:"key" doc:"Data key, as listed by /grids"`
	AcceptEncoding string `header:"Accept-Encoding"`
}

type gridDataOutput struct {
	ContentType     string `header:"Content-Type"`
	ContentEncoding string `header:"Content-Encoding"`
	CacheControl    string `header:"Cache-Control"`
	Vary            string `header:"Vary"`
	Body            []byte
}

func (s *Server) gridData(_ context.Context, in *gridDataInput) (*gridDataOutput, error) {
	set := s.gridSet()
	var data []byte
	if set != nil {
		data = set.Data(in.Layer, in.Key)
	}
	if data == nil {
		return nil, huma.Error404NotFound("no such grid: it may have left the window, ask /grids again")
	}
	out := &gridDataOutput{ContentType: "application/octet-stream", Vary: "Accept-Encoding",
		CacheControl: "public, max-age=86400, immutable", Body: data, ContentEncoding: "gzip"}
	if !acceptsGzip(in.AcceptEncoding) {
		zr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		raw, err := io.ReadAll(zr)
		if err != nil {
			return nil, err
		}
		out.Body, out.ContentEncoding = raw, ""
	}
	return out, nil
}

// acceptsGzip reports whether an Accept-Encoding header allows gzip.
func acceptsGzip(h string) bool {
	for part := range strings.SplitSeq(h, ",") {
		coding, params, _ := strings.Cut(part, ";")
		coding = strings.TrimSpace(coding)
		if !strings.EqualFold(coding, "gzip") && coding != "*" {
			continue
		}
		q, ok := strings.CutPrefix(strings.ReplaceAll(params, " ", ""), "q=")
		if !ok {
			return true
		}
		v, err := strconv.ParseFloat(q, 64)
		return err == nil && v > 0
	}
	return false
}

func (s *Server) gridSet() *grids.Set {
	if s.Grids == nil {
		return nil
	}
	return s.Grids()
}

// num formats a number for a PROJ string, without exponent and without the
// noise of a computed inverse flattening (298.25722356299997).
func num(v float64) string { return strconv.FormatFloat(math.Round(v*1e9)/1e9, 'f', -1, 64) }

// gridOfNowcast describes the grid the nowcast is computed on.
func gridOfNowcast(n *nowcast.Nowcast) *Grid {
	gt := n.Transform()
	g := &Grid{Width: n.Latest.W, Height: n.Latest.H, OriginX: gt.OriginX, OriginY: gt.OriginY, PixelW: gt.PixelW, PixelH: gt.PixelH}
	switch p := n.Projection().(type) {
	case *geo.TransverseMercator:
		e := p.Ellipsoid
		g.Projection = GridProjection{Name: "transverse_mercator", Lat0: p.Lat0, Lon0: p.Lon0, K0: p.K0,
			FalseEasting: p.FE, FalseNorthing: p.FN, SemiMajorAxis: e.A, InvFlattening: 1 / e.F,
			Proj4: fmt.Sprintf("+proj=tmerc +lat_0=%s +lon_0=%s +k=%s +x_0=%s +y_0=%s +a=%s +rf=%s +units=m +no_defs",
				num(p.Lat0), num(p.Lon0), num(p.K0), num(p.FE), num(p.FN), num(e.A), num(1/e.F))}
	case geo.Geographic:
		e := p.Ellipsoid
		g.Projection = GridProjection{Name: "geographic", SemiMajorAxis: e.A, InvFlattening: 1 / e.F,
			Proj4: fmt.Sprintf("+proj=longlat +a=%s +rf=%s +no_defs", num(e.A), num(1/e.F))}
	}
	// The edges of a projected grid are curved in lat/lon: sample them.
	b := Bounds{West: math.Inf(1), South: math.Inf(1), East: math.Inf(-1), North: math.Inf(-1)}
	const samples = 64
	w, h := float64(g.Width)*gt.PixelW, float64(g.Height)*gt.PixelH
	for i := range samples + 1 {
		f := float64(i) / samples
		for _, p := range [][2]float64{{f * w, 0}, {f * w, h}, {0, f * h}, {w, f * h}} {
			lat, lon := n.Projection().Inverse(gt.OriginX+p[0], gt.OriginY-p[1])
			b.West, b.East = min(b.West, lon), max(b.East, lon)
			b.South, b.North = min(b.South, lat), max(b.North, lat)
		}
	}
	g.Bounds = b
	return g
}
