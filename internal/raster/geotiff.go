package raster

import (
	"errors"
	"fmt"

	"github.com/vmaltarello/radarpoint/internal/geo"
)

// ErrOutside is returned when a point falls outside the raster extent.
var ErrOutside = errors.New("punto fuori dall'area coperta dal prodotto")

// GeoTIFF is a single-band TIFF with its georeference decoded.
type GeoTIFF struct {
	*Image
	Keys       GeoKeys
	Projection geo.Projection
	Transform  geo.GeoTransform
}

// OpenGeoTIFF reads a file and decodes its georeference.
func OpenGeoTIFF(path string) (*GeoTIFF, error) {
	im, err := Open(path)
	if err != nil {
		return nil, err
	}
	return NewGeoTIFF(im)
}

// NewGeoTIFF decodes the georeference of a parsed TIFF.
func NewGeoTIFF(im *Image) (*GeoTIFF, error) {
	keys, err := ParseGeoKeys(im)
	if err != nil {
		return nil, err
	}
	g := &GeoTIFF{Image: im, Keys: keys}
	if g.Projection, err = projection(keys); err != nil {
		return nil, err
	}
	if g.Transform, err = transform(im, keys); err != nil {
		return nil, err
	}
	return g, nil
}

func ellipsoid(k GeoKeys) (geo.Ellipsoid, error) {
	a, inv := k.Float(KeyGeogSemiMajorAxis, 0), k.Float(KeyGeogInvFlattening, 0)
	if a > 0 && inv > 0 {
		return geo.Ellipsoid{A: a, F: 1 / inv}, nil
	}
	switch gt := k.Int(KeyGeographicType, 4326); gt {
	case 4326:
		return geo.WGS84, nil
	default:
		return geo.Ellipsoid{}, fmt.Errorf("datum geografico EPSG:%d non supportato", gt)
	}
}

func projection(k GeoKeys) (geo.Projection, error) {
	ell, err := ellipsoid(k)
	if err != nil {
		return nil, err
	}
	switch mt := k.Int(KeyGTModelType, 0); mt {
	case 2: // geographic
		if !ell.Near(geo.WGS84) {
			return nil, errors.New("sistema geografico non WGS84 non supportato")
		}
		return geo.Geographic{Ellipsoid: ell}, nil
	case 1: // projected
		if pcs := k.Int(KeyProjectedCSType, 32767); pcs != 32767 {
			return nil, fmt.Errorf("sistema proiettato EPSG:%d non supportato", pcs)
		}
		if u := k.Int(KeyProjLinearUnits, 9001); u != 9001 {
			return nil, fmt.Errorf("unità lineare EPSG:%d non supportata (solo metri)", u)
		}
		switch ct := k.Int(KeyProjCoordTrans, 0); ct {
		case 1:
			return &geo.TransverseMercator{
				Lat0:      k.Float(KeyProjNatOriginLat, 0),
				Lon0:      k.Float(KeyProjNatOriginLong, 0),
				K0:        k.Float(KeyProjScaleAtNatOrigin, 1),
				FE:        k.Float(KeyProjFalseEasting, 0),
				FN:        k.Float(KeyProjFalseNorthing, 0),
				Ellipsoid: ell,
			}, nil
		default:
			return nil, fmt.Errorf("trasformazione di coordinate GeoTIFF %d non supportata", ct)
		}
	default:
		return nil, fmt.Errorf("GTModelType %d non supportato", mt)
	}
}

func transform(im *Image, k GeoKeys) (geo.GeoTransform, error) {
	if _, ok := im.Tags[TagModelTransformation]; ok {
		return geo.GeoTransform{}, errors.New("ModelTransformationTag non supportato")
	}
	sc, tp := im.Tags[TagModelPixelScale], im.Tags[TagModelTiepoint]
	if sc == nil || tp == nil || len(sc.Floats) < 2 || len(tp.Floats) < 6 {
		return geo.GeoTransform{}, errors.New("georeferenziazione assente (ModelPixelScale/ModelTiepoint)")
	}
	gt := geo.GeoTransform{PixelW: sc.Floats[0], PixelH: sc.Floats[1]}
	if gt.PixelW <= 0 || gt.PixelH <= 0 {
		return geo.GeoTransform{}, errors.New("dimensione pixel non valida")
	}
	i, j, x, y := tp.Floats[0], tp.Floats[1], tp.Floats[3], tp.Floats[4]
	if k.Int(KeyGTRasterType, 1) == 2 { // PixelIsPoint: tiepoint is a pixel centre
		i, j = i+0.5, j+0.5
	}
	gt.OriginX = x - i*gt.PixelW
	gt.OriginY = y + j*gt.PixelH
	return gt, nil
}

// PixelAt returns the pixel containing lat/lon, or ErrOutside.
func (g *GeoTIFF) PixelAt(lat, lon float64) (col, row int, err error) {
	x, y := g.Projection.Forward(lat, lon)
	col, row = g.Transform.Pixel(x, y)
	if col < 0 || row < 0 || col >= g.Width || row >= g.Height {
		return col, row, ErrOutside
	}
	return col, row, nil
}

// ValueAt returns the raw value at lat/lon and its pixel.
func (g *GeoTIFF) ValueAt(lat, lon float64) (v float64, col, row int, err error) {
	col, row, err = g.PixelAt(lat, lon)
	if err != nil {
		return 0, col, row, err
	}
	v, err = g.At(col, row)
	return v, col, row, err
}

// Bounds returns the lat/lon of the four raster corners: NW, NE, SE, SW.
func (g *GeoTIFF) Bounds() [4][2]float64 {
	t := g.Transform
	w, h := float64(g.Width)*t.PixelW, float64(g.Height)*t.PixelH
	pts := [4][2]float64{{t.OriginX, t.OriginY}, {t.OriginX + w, t.OriginY}, {t.OriginX + w, t.OriginY - h}, {t.OriginX, t.OriginY - h}}
	var out [4][2]float64
	for i, p := range pts {
		out[i][0], out[i][1] = g.Projection.Inverse(p[0], p[1])
	}
	return out
}

// DataType describes the sample type, e.g. "float32".
func (im *Image) DataType() string {
	switch im.SampleFormat {
	case 3:
		return fmt.Sprintf("float%d", im.BitsPerSample)
	case 2:
		return fmt.Sprintf("int%d", im.BitsPerSample)
	default:
		return fmt.Sprintf("uint%d", im.BitsPerSample)
	}
}

// CompressionName describes the compression scheme.
func (im *Image) CompressionName() string {
	switch im.Compression {
	case CompressionNone:
		return "nessuna"
	case CompressionLZW:
		return "LZW"
	default:
		return "Deflate"
	}
}
