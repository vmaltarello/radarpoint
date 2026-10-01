package raster

import (
	"errors"
	"fmt"
	"strings"
)

// GeoTIFF key IDs (GeoTIFF 1.1, OGC 19-008r4).
const (
	KeyGTModelType          = 1024
	KeyGTRasterType         = 1025
	KeyGTCitation           = 1026
	KeyGeographicType       = 2048
	KeyGeogCitation         = 2049
	KeyGeogGeodeticDatum    = 2050
	KeyGeogAngularUnits     = 2054
	KeyGeogEllipsoid        = 2056
	KeyGeogSemiMajorAxis    = 2057
	KeyGeogSemiMinorAxis    = 2058
	KeyGeogInvFlattening    = 2059
	KeyProjectedCSType      = 3072
	KeyPCSCitation          = 3073
	KeyProjection           = 3074
	KeyProjCoordTrans       = 3075
	KeyProjLinearUnits      = 3076
	KeyProjStdParallel1     = 3078
	KeyProjStdParallel2     = 3079
	KeyProjNatOriginLong    = 3080
	KeyProjNatOriginLat     = 3081
	KeyProjFalseEasting     = 3082
	KeyProjFalseNorthing    = 3083
	KeyProjFalseOriginLong  = 3084
	KeyProjFalseOriginLat   = 3085
	KeyProjCenterLong       = 3088
	KeyProjCenterLat        = 3089
	KeyProjScaleAtNatOrigin = 3092
	KeyProjScaleAtCenter    = 3093
	KeyVerticalCSType       = 4096
)

var geoKeyNames = map[uint16]string{
	1024: "GTModelType", 1025: "GTRasterType", 1026: "GTCitation",
	2048: "GeographicType", 2049: "GeogCitation", 2050: "GeogGeodeticDatum",
	2051: "GeogPrimeMeridian", 2052: "GeogLinearUnits", 2054: "GeogAngularUnits",
	2056: "GeogEllipsoid", 2057: "GeogSemiMajorAxis", 2058: "GeogSemiMinorAxis",
	2059: "GeogInvFlattening", 2061: "GeogPrimeMeridianLong",
	3072: "ProjectedCSType", 3073: "PCSCitation", 3074: "Projection",
	3075: "ProjCoordTrans", 3076: "ProjLinearUnits", 3078: "ProjStdParallel1",
	3079: "ProjStdParallel2", 3080: "ProjNatOriginLong", 3081: "ProjNatOriginLat",
	3082: "ProjFalseEasting", 3083: "ProjFalseNorthing", 3084: "ProjFalseOriginLong",
	3085: "ProjFalseOriginLat", 3086: "ProjFalseOriginEasting", 3087: "ProjFalseOriginNorthing",
	3088: "ProjCenterLong", 3089: "ProjCenterLat", 3092: "ProjScaleAtNatOrigin",
	3093: "ProjScaleAtCenter", 3094: "ProjAzimuthAngle", 3095: "ProjStraightVertPoleLong",
	4096: "VerticalCSType", 4099: "VerticalUnits",
}

// GeoKeyName returns the symbolic name of a GeoTIFF key.
func GeoKeyName(id uint16) string {
	if n, ok := geoKeyNames[id]; ok {
		return n
	}
	return fmt.Sprintf("Key%d", id)
}

// GeoKeys maps a key ID to its value: int64, []float64 or string.
type GeoKeys map[uint16]any

// Int returns an integer key, or def.
func (k GeoKeys) Int(id uint16, def int) int {
	if v, ok := k[id].(int64); ok {
		return int(v)
	}
	return def
}

// Float returns the first double of a key, or def.
func (k GeoKeys) Float(id uint16, def float64) float64 {
	if v, ok := k[id].([]float64); ok && len(v) > 0 {
		return v[0]
	}
	return def
}

// ParseGeoKeys decodes the GeoKeyDirectory of a TIFF.
func ParseGeoKeys(im *Image) (GeoKeys, error) {
	dir := im.ints(TagGeoKeyDirectory)
	if len(dir) < 4 {
		return nil, errors.New("no GeoKeyDirectory tag")
	}
	var doubles []float64
	if t, ok := im.Tags[TagGeoDoubleParams]; ok {
		doubles = t.Floats
	}
	var ascii string
	if t, ok := im.Tags[TagGeoAsciiParams]; ok {
		ascii = t.Str
	}
	n := int(dir[3])
	if len(dir) < 4+4*n {
		return nil, errors.New("GeoKeyDirectory truncated")
	}
	keys := GeoKeys{}
	for i := 0; i < n; i++ {
		e := dir[4+4*i : 8+4*i]
		id, loc, count, off := uint16(e[0]), e[1], int(e[2]), int(e[3])
		switch loc {
		case 0:
			keys[id] = e[3]
		case TagGeoDoubleParams:
			if off+count <= len(doubles) {
				keys[id] = doubles[off : off+count]
			}
		case TagGeoAsciiParams:
			if off+count <= len(ascii) {
				keys[id] = strings.TrimRight(ascii[off:off+count], "|\x00")
			}
		case TagGeoKeyDirectory:
			if off+count <= len(dir) {
				keys[id] = dir[off]
			}
		}
	}
	return keys, nil
}
