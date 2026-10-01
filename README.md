# radarpoint

A small Go command-line tool that reads the latest weather radar products
published by the Italian Civil Protection Department
([Radar-DPC](https://radar.protezionecivile.it)) and prints the value at a
given latitude/longitude: rain rate, probability of hail, air temperature and
more.

```
$ radarpoint --product SRI --lat 45.5966 --lon 8.9150
Prodotto:      SRI (intensità di pioggia al suolo)
Orario dato:   2026-10-01 12:40 UTC (14:40 ora italiana), periodo PT5M
Punto:         45.5966, 8.9150
Pixel:         x=320, y=244
Valore:        0 mm/h (nessuna pioggia)
File:          459 kB, 1200x1400 px, 1 banda float32, LZW, nodata=-9999 (dedotto, non dichiarato nel file)
Proiezione:    Transverse Mercator WGS84 (lat0=42, lon0=12.5, k0=1, FE=0, FN=0), pixel 1000 x 1000
Tempi:         API 259 ms, download 167 ms, lettura 0 ms
Fonte:         Radar-DPC – Dipartimento della Protezione Civile (CC-BY-SA 4.0)
```

The command output is in Italian, since the data covers Italy only.

The project is at an early stage: today it performs a single reading and
exits. The Go packages are meant to become the base of a small service that
downloads each product once and answers point queries (see
[Roadmap](#roadmap)).

- Pure Go, a single static binary, no GDAL or other C libraries.
- No API key or registration needed: Radar-DPC data is open.
- Documented file formats, projections and quirks (see
  [Data format](#data-format)), verified against real files.

## Data source and license

All data comes from **Radar-DPC – Dipartimento della Protezione Civile**,
released under [CC-BY-SA 4.0](https://creativecommons.org/licenses/by-sa/4.0/).
If you publish or redistribute values obtained with this tool you must credit
the source and share under the same license.

The data is real time and **not validated**. Radar coverage can be partial
because of faults or maintenance. Do not use it for safety-critical decisions.

The source code of this project is released under the [MIT license](LICENSE).

## Installation

Requires Go 1.27 or later.

```
go install github.com/vmaltarello/radarpoint/cmd/radarpoint@latest
```

Or from a clone:

```
git clone https://github.com/vmaltarello/radarpoint
cd radarpoint
go build -o radarpoint ./cmd/radarpoint
```

## Usage

```
radarpoint --product SRI  --lat 45.5966 --lon 8.9150          # rain rate, mm/h
radarpoint --product POH  --lat 45.5966 --lon 8.9150          # probability of hail
radarpoint --product TEMP --lat 45.5966 --lon 8.9150          # air temperature, °C
radarpoint --product SRI  --lat 45.5966 --lon 8.9150 --keep   # also save the GeoTIFF
```

| Flag | Meaning |
|---|---|
| `--product` | Radar-DPC product type, default `SRI`. Any raster type accepted by the API works; units and nodata values are known for `SRI`, `POH` and `TEMP`. |
| `--lat`, `--lon` | WGS84 coordinates in decimal degrees (required). |
| `--keep` | Save the downloaded file in the current directory as `<PRODUCT>_<name>.tif`. |

Exit codes: `0` success (including "no data at this point"), `1` error or point
outside the product area, `2` invalid arguments.

Special values are reported explicitly:

- radar not available at the point → `nessun dato (radar non disponibile in questa zona)`
- point outside the product grid → error with the approximate covered area
- zero rain → `0 mm/h (nessuna pioggia)`

### Helper tools

- `go run ./cmd/tiffdump file.tif` prints TIFF tags, GeoTIFF keys and value
  statistics. It is a small replacement for `gdalinfo`.
- `go run ./cmd/tiffcrop in.tif out.tif fromRow toRow` cuts a band of rows from
  a file without re-encoding it. It is used to build the files in `testdata/`.

## How it works

1. `GET /findLastProductByType?type=SRI` returns the time of the latest
   available product.
2. `POST /downloadProduct` returns a pre-signed S3 URL for that file.
3. The GeoTIFF is downloaded and parsed in memory.
4. The point is projected into the file's coordinate system, converted to a
   pixel, and only the strip containing that pixel is decoded.

Every HTTP call has a timeout, and a failed call is retried at most once
(only for network errors, HTTP 429 and 5xx).

API documentation:
[dpc-radar.readthedocs.io](https://dpc-radar.readthedocs.io/it/latest/api.html).
The API changed on 12 January 2026; scripts written before that date no longer
work.

## Project layout

```
cmd/radarpoint/     the command-line tool
cmd/tiffdump/       inspect a GeoTIFF
cmd/tiffcrop/       cut a band of rows out of a GeoTIFF
internal/dpc/       Radar-DPC API client and product catalogue (units, nodata)
internal/raster/    minimal TIFF/GeoTIFF reader
internal/geo/       Transverse Mercator projection and pixel transform
testdata/           small excerpts of real Radar-DPC files
```

### Why not GDAL?

The products inspected so far only need: classic TIFF with strips, LZW or
Deflate compression without predictor, one float32 band, and two coordinate
reference systems (plain EPSG:4326 and a parameter-defined Transverse
Mercator). A focused reader and an ellipsoidal Transverse Mercator
implementation (Krüger series) cover all of it, and keep the build free of cgo
and system libraries.

If Radar-DPC starts publishing files with other projections or encodings, the
reader fails with an explicit "not supported" error. GDAL through
[godal](https://github.com/airbusgeo/godal) remains the fallback.

## Data format

Findings from real files downloaded on 1 October 2026. The official
documentation does not describe units, nodata values or projections, so
everything below comes from inspecting the files and may change.

| | SRI | POH | TEMP |
|---|---|---|---|
| Content | rain rate at ground | probability of hail | air temperature, interpolated from ground stations |
| Update period | 5 min | 5 min | 1 hour |
| File size | ~450 kB | ~320 kB | ~306 kB |
| Raster size | 1200 × 1400 | 1200 × 1400 | 631 × 576 |
| Bands / type | 1 × float32 | 1 × float32 | 1 × float32 |
| Compression | LZW, no predictor | LZW, no predictor | Deflate, no predictor |
| Layout | 1-row strips | 1-row strips | 3-row strips |
| Coordinate system | Transverse Mercator | Transverse Mercator | EPSG:4326 |
| Pixel size | 1000 m | 1000 m | 0.019983° (~2 km) |
| Top-left corner | x −600000, y 650000 m | same | lon 6.0, lat 47.5003 |
| Nodata | −9999 | −9999 | −99999, and exactly 0 |
| Unit | mm/h, stored directly | not documented | °C, stored directly |

SRI and POH share the same grid. TEMP uses a different one.

### SRI/POH coordinate system

The GeoTIFF keys declare a projected model (`GTModelType=1`) with a Transverse
Mercator transformation (`ProjCoordTrans=1`), natural origin at 42°N 12.5°E,
metres, WGS84 datum. The scale factor and false easting/northing are missing,
so the GeoTIFF defaults apply (k0 = 1, no offsets). There is no EPSG code. The
equivalent PROJ string is:

```
+proj=tmerc +lat_0=42 +lon_0=12.5 +k=1 +x_0=0 +y_0=0 +datum=WGS84 +units=m
```

Pixel to model coordinates: `x = -600000 + col × 1000`,
`y = 650000 − row × 1000` (pixel-is-area, top-left corner). The grid covers
roughly latitude 35.1–47.6 and longitude 4.5–20.5.

How this was verified: the coverage mask (−9999 pixels) of a downloaded file
was reprojected with this code and compared with the official WMS map tiles
in EPSG:4326 for the same area. Where both refer to the same moment, 99.8% of
the samples match and the best alignment is with no shift.

### Values and nodata

- **SRI**: rain rate in mm/h, no scale or offset, resolution 0.01. `−9999`
  means outside radar coverage or radar unavailable (about half of the grid,
  including open sea and neighbouring countries).
- **POH**: probability of hail. Only `0` and `−9999` have been observed so
  far, so whether the scale is 0–1 or 0–100 is **still unknown**. The tool
  prints the raw value. Contributions from a file recorded during a hailstorm
  are welcome.
- **TEMP**: °C, no scale or offset. `−99999` marks a few missing pixels, and
  **exactly 0 marks sea and areas outside Italy** (about three quarters of the
  grid). The tool treats an exact 0 as no data for this product. A real
  temperature of exactly 0.000 °C stored as float32 is extremely unlikely, but
  the format is ambiguous.
- No file carries the `GDAL_NODATA` tag. Nodata values are defined per
  product in [`internal/dpc/products.go`](internal/dpc/products.go).

### API behaviour

- Requests need an `Origin` header (`https://radar.protezionecivile.it`). The
  client also sends `Referer` and a `User-Agent` identifying the project.
- The pre-signed download URL expires after **300 seconds** (the
  documentation says 900). Download it right away.
- Object keys have no common format, e.g. `SRI/01-10-2026-12-35.tif` and
  `rete_terra/temp/202610011200_1h_Temp.tif`. The client does not parse them.
- TEMP is published with a delay: at 12:35 UTC the latest TEMP file was the
  12:00 UTC one.
- Timings measured from a home connection: 100–200 ms per API call (two per
  reading), 130–650 ms to download a file, under 1 ms to read the point.

## Development

```
go test ./...
go vet ./...
```

- `internal/dpc` tests use `httptest` servers that return the example
  responses from the official documentation, including 404 and 400 errors,
  retry and timeout behaviour.
- `internal/raster` tests read the excerpts in `testdata/` (LZW with
  Transverse Mercator, Deflate with EPSG:4326) and check values against the
  original files.
- `internal/geo` tests check the projection against the worked example in
  Snyder, *Map Projections – A Working Manual* (USGS Professional Paper 1395,
  p. 269), plus round trips across the whole grid.

### Test data

The files in `testdata/` are bands of rows cut from real Radar-DPC files with
`cmd/tiffcrop`, which copies the compressed strips unchanged:

- `sri_crop.tif`: SRI, 2026-10-01 12:45 UTC, rows 170–249
- `temp_crop.tif`: TEMP, 2026-10-01 12:00 UTC, rows 90–101

Source: Radar-DPC – Dipartimento della Protezione Civile, CC-BY-SA 4.0.

## Contributing

Issues and pull requests are welcome. Especially useful:

- the scale of POH values, from a file recorded during hail;
- format notes for other products (VMI, SRT1, CUM*, CAPPI*, …);
- reports when Radar-DPC changes its API or file format.

Please keep the code dependency-free where reasonable, run `gofmt` and
`go test ./...`, and make sure the Radar-DPC attribution stays visible in any
output that shows data.

## Roadmap

1. A long-running service that downloads each product once every 5 minutes
   and keeps the last 30 minutes in memory.
2. An HTTP API: `/now?lat=&lon=` for current rain, hail and temperature, and
   `/nowcast?lat=&lon=` for the next hour.
3. Nowcasting up to 60 minutes: first a simple motion extrapolation in Go,
   then the [IRENE](https://huggingface.co/it4lia/irene) model (BSD 2-Clause).
4. Integrations: Home Assistant, Telegram bot, webhooks.

Lightning data is not available from the Radar-DPC API and is out of scope.
