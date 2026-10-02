# radarpoint

A small Go command-line tool that reads the latest weather radar products
published by the Italian Civil Protection Department
([Radar-DPC](https://radar.protezionecivile.it)) and prints the value at a
given latitude/longitude: rain rate, probability of hail, air temperature and
more.

```
$ radarpoint --product SRI --lat 45.5966 --lon 8.9150
Product:      SRI (rain rate at ground level)
Data time:    2026-10-01 12:40 UTC (14:40 Italian time), period PT5M
Point:        45.5966, 8.9150
Pixel:        x=320, y=244
Value:        0 mm/h (no rain)
File:         459 kB, 1200x1400 px, 1 band float32, LZW, nodata=-9999 (inferred, not declared in the file)
Projection:   Transverse Mercator WGS84 (lat0=42, lon0=12.5, k0=1, FE=0, FN=0), pixel 1000 x 1000
Timings:      API 259 ms, download 167 ms, read 0 ms
Source:       Radar-DPC – Dipartimento della Protezione Civile (CC-BY-SA 4.0)
```

The project has two programs:

- `radarpoint`, a command-line tool that performs a single reading and exits;
- `radarpointd`, a small HTTP service that downloads each product once, as
  soon as it is published, keeps the last 30 minutes in memory and answers
  point queries for any number of clients, including a rain forecast for
  the next hour (see [HTTP service](#http-service) and [Nowcast](#nowcast)).

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
go build -o radarpointd ./cmd/radarpointd
```

### With Docker

```
docker compose up -d                 # radarpointd and the IRENE forecast service
docker compose up -d radarpointd     # radarpointd alone, extrapolation only
```

`radarpointd` is a 19 MB image. The optional IRENE service is a CPU-only
PyTorch image of about 1.6 GB; on first start it downloads the model
(770 MB) into a volume. See [IRENE](#irene).

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

- radar not available at the point → `no data (radar not available at this point)`
- point outside the product grid → error with the approximate covered area
- zero rain → `0 mm/h (no rain)`

### Helper tools

- `go run ./cmd/tiffdump file.tif` prints TIFF tags, GeoTIFF keys and value
  statistics. It is a small replacement for `gdalinfo`.
- `go run ./cmd/tiffcrop in.tif out.tif fromRow toRow` cuts a band of rows from
  a file without re-encoding it. It is used to build the files in `testdata/`.

## HTTP service

```
radarpointd --listen :8080 --products SRI,POH,TEMP --window 30m
```

| Endpoint | Returns |
|---|---|
| `GET /v1/now?lat=&lon=` | latest value of every product at the point |
| `GET /v1/history?lat=&lon=&product=SRI` | values of all frames held in memory, oldest first; `product` must be one of the products the service follows |
| `GET /v1/nowcast?lat=&lon=` | rain and hail forecast for the next hour, in 5-minute steps (see [Nowcast](#nowcast)) |
| `GET /healthz` | frames held per product; `503` until every product has data |
| `GET /docs` | interactive API documentation |
| `GET /openapi.json` | OpenAPI 3.1 description |

The data endpoints are versioned under `/v1`. Breaking changes will get a new
prefix (`/v2`), with the old one kept for a transition period; additions, such
as new fields in a response, can happen within a version. `/healthz`, `/docs`
and `/openapi.json` are for operators and are not versioned.

```
$ curl 'localhost:8080/v1/now?lat=45.5966&lon=8.915'
{
  "lat": 45.5966,
  "lon": 8.915,
  "readings": [
    {"product": "SRI", "description": "rain rate at ground level",
     "time": "2026-10-01T13:25:00Z", "age_seconds": 745, "stale": false,
     "status": "ok", "value": 0, "unit": "mm/h"},
    {"product": "POH", "description": "probability of hail",
     "time": "2026-10-01T13:30:00Z", "age_seconds": 445, "stale": false,
     "status": "ok", "value": 0, "unit": "%"},
    {"product": "TEMP", "description": "air temperature, interpolated from ground stations",
     "time": "2026-10-01T13:00:00Z", "age_seconds": 2245, "stale": false,
     "status": "ok", "value": 23.12, "unit": "°C"}
  ],
  "attribution": "Radar-DPC – Dipartimento della Protezione Civile (CC-BY-SA 4.0)"
}
```

`status` is one of `ok`, `nodata` (no radar or station data at the point),
`outside` (the point is outside the product grid) and `unavailable` (nothing
downloaded yet); `value` is `null` unless the status is `ok`. `stale` is `true`
when the data is older than it normally gets (more than 20 minutes for SRI and
POH, 2.5 hours for TEMP): Radar-DPC has probably stopped publishing, and the
value is kept but should not be presented as current. `/healthz` then answers
`503`. Each reading carries its own `time`: products are updated at different
rates and TEMP lags behind the radar products. Invalid parameters get a `422` answer in
[RFC 9457](https://www.rfc-editor.org/rfc/rfc9457) problem format. Responses
allow cross-origin requests, so a web page can call the API directly.

How the service talks to Radar-DPC:

- on start it downloads every instant within the window (6 frames of 5 minutes
  for 30 minutes; at least the latest frame for hourly products);
- it then waits until the next instant is due and checks once per interval
  (1 minute for 5-minute products, 5 minutes for hourly ones) until it is
  published, and fills any gap it finds;
- instants the API reports as missing are skipped and not asked again.

So the load on Radar-DPC does not depend on the number of clients: a few small
requests and one download per product every 5 minutes.

## Nowcast

Radar-DPC publishes observations only. `radarpointd` computes its own short-term
forecast from the SRI frames it holds, using **Lagrangian persistence**, the
standard baseline in radar nowcasting:

1. **Motion.** Consecutive frames are compared block by block (48 km blocks,
   on a 2 km grid). For each block, the shift that best overlays the older
   frame on the newer one is the displacement of the rain. Displacements from
   the last 5 frame pairs are averaged, blocks without rain take the median
   motion, and the field is smoothed. Each pair is measured once and cached,
   so a new frame costs one pair, not five.
2. **Extrapolation.** To forecast a point at +k steps, the motion is followed
   backwards from the point for k steps, and the latest observation is read
   where the rain comes from.
3. **Smoothing with lead time.** The value read there is a Gaussian average
   whose width grows with the lead time (0.75 km per 5-minute step, about
   9 km at one hour): small cells cannot be placed precisely far ahead, so
   the forecast blurs them instead of predicting a sharp cell in the wrong
   place. This alone improved the scores by 2–18% (see below).
4. **Probability of rain.** Each step also gives the share of pixels with
   rain (≥ 0.2 mm/h) in a square around the place the rain comes from, from
   5 km across now to about 53 km across at one hour, as the position error
   grows. The size was chosen so the probability is calibrated: where it
   says 70%, it rained about 7 times out of 10.

The motion is recomputed once for every new SRI frame (well under a second on
a desktop CPU); a `/v1/nowcast` query then takes about 0.07 ms
(`go test ./internal/nowcast -bench Forecast`).

**Hail.** When POH is followed too, the probability of hail observed at the
same time is moved along the same trajectories: hail falls from the same storm
cells as the heaviest rain. Each step then carries `hail_percent`.

```
$ curl 'localhost:8080/v1/nowcast?lat=45.5966&lon=8.915'
{
  "lat": 45.5966, "lon": 8.915, "product": "SRI", "unit": "mm/h",
  "status": "ok",
  "base_time": "2026-10-01T13:45:00Z",
  "stale": false,
  "motion": {"speed_kmh": 10, "toward_deg": 109},
  "steps": [
    {"time": "2026-10-01T13:45:00Z", "lead_minutes": 0,  "status": "ok", "value": 0, "rain_probability": 0, "hail_percent": 0},
    {"time": "2026-10-01T13:50:00Z", "lead_minutes": 5,  "status": "ok", "value": 0, "rain_probability": 0, "hail_percent": 0},
    …
    {"time": "2026-10-01T14:45:00Z", "lead_minutes": 60, "status": "ok", "value": 0, "rain_probability": 12, "hail_percent": 0}
  ],
  "method": "lagrangian-persistence",
  …
}
```

Lead 0 is the observation the forecast starts from. A step has status
`nodata` when the rain would come from outside the radar coverage.
`value` is the expected rain rate at the point; `rain_probability` (%) is
usually the more useful figure for "will it rain here?", especially beyond
20 minutes.
`hail_percent` is `null` when POH is not followed or its latest frame does not
have the same time as the rain frame yet (they are published a few seconds
apart).
The endpoint exists only when SRI is among the followed `--products`, and
answers `unavailable` until two SRI frames have been downloaded.

**Limits.** Rain is moved but never created, grown or dissipated: storms that
form from nothing are not anticipated, and decaying ones seem to last. Skill
drops with lead time; treat 30–60 minutes as indicative.

### IRENE

[IRENE](https://huggingface.co/it4lia/irene) is a neural network by
Fondazione Bruno Kessler (BSD 2-Clause), trained on years of the same
Radar-DPC composite. From the last 6 frames it produces an ensemble of
forecasts for the next hour; unlike the extrapolation, it has learnt how rain
cells grow, decay and split. `radarpointd` can use it through a separate
service, [`irene/`](irene/README.md), so the Go binary stays free of Python:

- after each new SRI frame, `radarpointd` sends the last 6 frames to the
  service (`--irene-url`) and gets back, for each step, the ensemble mean and
  the probability of rain;
- `/v1/nowcast` uses IRENE as soon as its forecast for the latest frame is
  ready (about a minute later), and the extrapolation otherwise: `method`
  says which, and `?method=extrapolation|irene` forces one;
- the motion and the hail probability still come from the extrapolation;
- if the service is down or slow, nothing breaks: the extrapolation is
  served.

On CPU (12 cores), the whole grid takes about 50–85 s with 4 members
(`--irene-members`, the default) and needs about 3 GB of memory; one member
takes about 25 s, ten about 3.5 minutes.

**Scores.** IRENE was trained on 2021–2025, so it is scored on data it has
never seen. The archive has 5-minute frames from July 2020, which leaves
July–December 2020: 120 cases, at most one per day, from the IT-DPC-SRI
archive (40 summer storms, 30 heavy autumn rain, 20 December rain, 30 random
moments with rain). CSI of the IRENE ensemble mean (4 members) and of the
extrapolation:

| Lead | ≥0.5 mm/h IRENE | extrapolation | ≥5 mm/h IRENE | extrapolation |
|---|---|---|---|---|
| +15 min | 0.747 | 0.695 | 0.511 | 0.469 |
| +30 min | 0.663 | 0.597 | 0.388 | 0.347 |
| +45 min | 0.595 | 0.525 | 0.310 | 0.270 |
| +60 min | 0.547 | 0.478 | 0.247 | 0.214 |

- IRENE is better overall at every lead: 7–14% for rain in general, 9–15%
  for heavy rain, and in every category and region. The only near ties are heavy rain at +60 min
  on the random moments (0.104 vs 0.099) and over Sardinia (0.238 vs 0.241).
- At points that are dry now, "will it rain within 60 minutes?": both catch
  58% of the onsets, but IRENE raises a third fewer false alarms (20% of its
  warnings against 29%) and is closer on the time (7.7 against 9.3 minutes
  off, while the extrapolation is about 2 minutes late on average). The 42%
  of onsets that both miss are likely new cells, which no radar nowcast sees
  coming.
- The share of members with rain is overconfident (with 2 of 4 members it
  rains 43–49% of the time), so the client maps it to the observed frequency
  per step (`internal/irene/calibrate.go`). Fitted on half of the cases, the
  map lowers the Brier score on the other half by 2–4%. Even without it,
  IRENE's Brier score is 10–14% below that of the extrapolation's probability.
- On 25 of the summer storms, a single member alone was slightly worse than
  the extrapolation for heavy rain: the mean is what makes the difference.

On recent data, the 20 cases of September 2026 used below, IRENE is 7–14%
better for rain in general and level for heavy rain (CSI at +30 min: 0.591
against 0.538 at 0.5 mm/h, 0.287 against 0.291 at 5 mm/h); those cases had
no summer storms.

### Verification

`go run ./cmd/nowcastverify` forecasts from frames one hour old and scores each
step against what the radar then observed, next to persistence (rain that
stays where it is). The score is the critical success index (CSI):
hits / (hits + misses + false alarms), from 0 (no skill) to 1 (perfect),
summed over all pixels with rain above a threshold.

With `--cases 20` it scans the last 13 days (one frame every 3 hours), picks
the 20 moments with the most rain and adds up their scores. Downloaded files
are cached (in `~/.cache/radarpoint/sri` on Linux), so later runs are quick
and do not load Radar-DPC again; the first run downloads about 430 files
(~200 MB).

Results of `--cases 20` on 1 October 2026 (18 September – 1 October, rain
over 2–6% of the covered area), without and with the lead-time smoothing:

| Lead | ≥0.5 mm/h nowcast | without smoothing | persistence | ≥5 mm/h nowcast | without smoothing | persistence |
|---|---|---|---|---|---|---|
| +5 min | 0.813 | 0.808 | 0.789 | 0.658 | 0.653 | 0.633 |
| +15 min | 0.665 | 0.651 | 0.583 | 0.440 | 0.412 | 0.347 |
| +30 min | 0.538 | 0.511 | 0.425 | 0.291 | 0.258 | 0.194 |
| +45 min | 0.451 | 0.414 | 0.329 | 0.211 | 0.179 | 0.126 |
| +60 min | 0.391 | 0.348 | 0.270 | 0.151 | 0.129 | 0.083 |

The nowcast beats persistence at every lead time, by about 25% at 30 minutes
and 50–80% for heavier rain. Absolute skill for heavy rain is low beyond
30 minutes: intense cells grow and decay faster than they move, which
extrapolation cannot capture. None of these cases had widespread rain (more
than 10% of the area); results on such days are welcome.

The CSI is strict: a cell forecast 3 km off counts both as a miss and as a
false alarm. For "will it rain here?" the probability is the better guide,
and the tool also checks that it is calibrated. Observed frequency of rain
for each forecast probability, same 20 cases:

| Forecast | 10–20% | 30–40% | 50–60% | 70–80% | 90–100% |
|---|---|---|---|---|---|
| +15 min | 10% | 29% | 57% | 78% | 96% |
| +30 min | 12% | 33% | 56% | 74% | 94% |
| +60 min | 14% | 32% | 50% | 69% | 87% |

Its Brier score (lower is better) is 30–45% below that of a yes/no forecast
from the same extrapolation (0.019 against 0.030 at 30 minutes).

Options to try other settings, with their current defaults:
`--smooth 0.75` (smoothing growth, pixels per step), `--prob-radius 2` and
`--prob-growth 2` (probability square, half side and growth in pixels),
`--robust` and `--decay 1` (median and recency weights when combining frame
pairs; neither made a measurable difference on these cases).

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
cmd/radarpointd/    the HTTP service
irene/              optional IRENE forecast service (Python, Docker)
cmd/tiffdump/       inspect a GeoTIFF
cmd/tiffcrop/       cut a band of rows out of a GeoTIFF
cmd/nowcastverify/ score the nowcast against real observations
cmd/tempmask/      rebuild the TEMP no-data mask
.github/        CI workflow and issue templates
internal/dpc/       Radar-DPC API client and product catalogue (units, nodata)
internal/ingest/    keeps the store up to date with the latest products
internal/store/     frames held in memory
internal/irene/     client and background runner for the IRENE service
internal/nowcast/   motion estimation and rain extrapolation
internal/api/       HTTP API, built with Huma
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
| Nodata | −9999 | −9999 (0 means under 30%) | −99999, and exactly 0 |
| Unit | mm/h, stored directly | probability 0–1, shown in % | °C, stored directly |

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
- **POH**: probability of hail as a fraction from 0 to 1, in steps of 1/254
  (an 8-bit value rescaled). **Values under 0.30 are set to 0**, so 0 means
  "under 30%", not "no hail". Verified on the stormiest moments between
  18 and 25 September 2026: the maximum is exactly 1, the smallest positive
  value is 0.30, and pixels with POH > 0 have 27–140 mm/h of rain on average.
  The tool and the API show it in % (0–100).
- **TEMP**: °C, no scale or offset. `−99999` marks a few missing pixels, and
  **exactly 0 marks sea and areas outside Italy** (about three quarters of the
  grid). To tell that 0 from a real 0 °C, `internal/dpc/tempmask.png` marks
  the pixels that were exactly 0 in every file over two weeks (104 files, day
  and night): sea, foreign countries, San Marino and Vatican City. An exact 0
  is no data only there; on land it is shown as 0 °C. The mask is rebuilt with
  `go run ./cmd/tempmask`, and ignored if Radar-DPC changes the TEMP grid.
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

Issues and pull requests are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md) for the
development setup, checks and commit style. Especially useful:

- verification results on days with widespread or convective rain;
- format notes for other products (VMI, SRT1, CUM*, CAPPI*, …);
- reports when Radar-DPC changes its API or file format.

Please keep the code dependency-free where reasonable, run `gofmt` and
`go test ./...`, and make sure the Radar-DPC attribution stays visible in any
output that shows data.

## Roadmap

1. ~~A service that downloads each product once and keeps the last 30 minutes
   in memory.~~ Done: `radarpointd`.
2. ~~`/v1/now` HTTP API.~~ Done.
3. ~~Nowcast by motion extrapolation behind `/v1/nowcast`.~~ Done, with the
   optional [IRENE](#irene) service, verified on summer storms from the
   archive. Next: blending with the ICON-2I model beyond one hour.
4. A web map: radar layers over a map, click a point to see its current value
   and the last 30 minutes, built on the HTTP API.
5. ~~Container image for easy deployment.~~ Done: `Dockerfile` and
   `compose.yaml`. Next: published images and binaries for each release.
6. Integrations: Home Assistant, Telegram bot, webhooks.

Lightning data is not available from the Radar-DPC API and is out of scope.
