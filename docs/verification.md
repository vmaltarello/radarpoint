# Verification

How good are radarpoint's forecasts? This page describes how they were
scored, the results and their limits, and how to reproduce every number.
The README shows only the main tables.

All scores compare a forecast with what the radar observed afterwards.
Rain is checked against the radar rain rate (SRI). Hail is checked against
the radar probability of hail (POH), **not against hail seen on the
ground**: see [Limits](#limits).

## Data

**Radar-DPC files.** The Italian Civil Protection Department publishes
every product every 5 minutes (CC BY-SA 4.0) and keeps about four and a
half months of files: on 2 October 2026 the oldest available instant was
12 May 2026. [`cmd/dpcarchive`](../cmd/dpcarchive) downloads them. The
whole season, 12 May to 2 October 2026, is 41,340 frames for each of SRI,
POH, VIL and ETM, 61 GB.

**IT-DPC-SRI archive.** The rain rate of the same composite from 2010 to
2025 (CC BY-SA 4.0), a public Zarr store on the ECMWF European Weather
Cloud. Frames are every 5 minutes only from July 2020 (every 10 minutes
before), and IRENE was trained, validated and tested on 1 January 2021 to
11 December 2025 (90/5/5% split in time). So the archive gives IRENE
unseen data only for July–December 2020.

Two independent samples of unseen data follow:

| Sample | Period | Cases | Source |
|---|---|---|---|
| 2020 | July–December 2020 | 120: 40 summer storms, 30 heavy autumn rain, 20 December rain, 30 random moments with rain | IT-DPC-SRI archive |
| 2026 | 12 May – 1 October 2026 | 102: 85 storms, 17 random moments with rain | Radar-DPC files |

At most one case per day. Storms are the moments with the largest area of
rain ≥ 10 mm/h; heavy autumn rain, of rain ≥ 5 mm/h; December rain, of rain
≥ 0.5 mm/h. Each score uses the weaker of two consecutive frames, and a
window whose frames have far more heavy rain than its median is skipped:
the archive has a few corrupted frames with heavy rain almost everywhere.
A case is the 6 frames up to the base time (the input of the forecasts)
and the 12 frames of the next hour (the truth).

## Measures

- **CSI** (critical success index) at 0.5 and 5 mm/h, per 5-minute step:
  hits / (hits + misses + false alarms) over the pixels with data, from 0
  (no skill) to 1. It is strict: a cell forecast 3 km off counts both as a
  miss and as a false alarm.
- **FSS** (fractions skill score): the same comparison over squares of 5
  to 81 km, which tells at which scale a forecast becomes reliable.
- **Brier score** and **reliability** of the probability of rain (≥ 0.2
  mm/h): for each forecast probability, how often it rained.
- **Onset at a point**: at pixels dry now, "will it rain (≥ 0.5 mm/h)
  within 30 or 60 minutes?", with the detection rate (POD), the share of
  warnings without rain (FAR) and the error on the time of the first rain.
- **Persistence** (rain that stays where it is) as the reference any
  forecast must beat.

## Rain

The extrapolation is `internal/nowcast` as served; IRENE is the ensemble
mean of 4 members.

### CSI

| Lead | 2020 ≥0.5 mm/h IRENE / extrap. | 2026 ≥0.5 mm/h IRENE / extrap. | 2020 ≥5 mm/h IRENE / extrap. | 2026 ≥5 mm/h IRENE / extrap. |
|---|---|---|---|---|
| +15 min | 0.747 / 0.695 | 0.727 / 0.674 | 0.511 / 0.469 | 0.549 / 0.497 |
| +30 min | 0.663 / 0.597 | 0.616 / 0.547 | 0.388 / 0.347 | 0.388 / 0.339 |
| +45 min | 0.595 / 0.525 | 0.545 / 0.474 | 0.310 / 0.270 | 0.301 / 0.261 |
| +60 min | 0.547 / 0.478 | 0.489 / 0.419 | 0.247 / 0.214 | 0.243 / 0.210 |

Persistence at +30 min: 0.496 (2020) and 0.450 (2026) at 0.5 mm/h.

By category (CSI at +30 min, IRENE / extrapolation):

| Category | ≥0.5 mm/h | ≥5 mm/h |
|---|---|---|
| 2020 summer storms | 0.667 / 0.592 | 0.418 / 0.368 |
| 2020 autumn heavy rain | 0.671 / 0.613 | 0.418 / 0.384 |
| 2020 December rain | 0.678 / 0.619 | 0.312 / 0.273 |
| 2020 random moments | 0.545 / 0.465 | 0.240 / 0.227 |
| 2026 storms | 0.625 / 0.556 | 0.391 / 0.343 |
| 2026 random moments | 0.488 / 0.417 | 0.227 / 0.197 |

By region IRENE is better everywhere (Alps, North, Centre, South and
Sicily, Sardinia), except for near ties on heavy rain at +60 min: Sardinia
in 2020 (0.238 vs 0.241) and South and Sicily in 2026 (0.136 vs 0.142).

FSS at +30 min, 0.5 mm/h, 2020: 0.797 / 0.747 at 1 km, 0.898 / 0.853 at
11 km, 0.960 / 0.939 at 41 km (IRENE / extrapolation). The gap stays at
every scale.

### Onset at a point

| "Dry now, rain within 60 min?" | POD | FAR | Error on the first rain |
|---|---|---|---|
| 2020 extrapolation | 0.584 | 0.285 | 9.3 min, 2.1 min late on average |
| 2020 IRENE | 0.582 | 0.204 | 7.7 min, no bias |
| 2026 extrapolation | 0.554 | 0.348 | 10.2 min, 3.5 min late |
| 2026 IRENE | 0.533 | 0.229 | 8.0 min, 0.4 min late |

Both catch a little over half of the onsets; IRENE raises a third fewer
false alarms. The onsets that both miss are likely new cells, which no
radar nowcast sees coming.

### Probability of rain

Brier score at +15 / +30 / +60 min, IRENE / extrapolation: 0.0213 / 0.0247,
0.0292 / 0.0335, 0.0413 / 0.0457 in 2020; 0.0124 / 0.0142, 0.0183 / 0.0205,
0.0257 / 0.0277 in 2026 (IRENE's raw share of members).

The share of members with rain is overconfident. On the 2020 cases, with
2 of 4 members it rained 43–49% of the time depending on the step, with 3
of 4 62–68%; the odd and even halves of the cases agree within one point.
`internal/irene/calibrate.go` maps the share to the observed frequency;
fitted on one half, it lowers the Brier score on the other by 2–4%. The
same table holds on 2026: at +30 min, 2 of 4 members verified at 46% (table
45%), 3 of 4 at 65% (table 65%).

### Recent cases

`cmd/nowcastverify` scores the extrapolation on the rainiest moments of
the last days, downloading what it needs. On 20 cases of 18 September – 1
October 2026 (no summer storms), IRENE was 7–14% better at 0.5 mm/h and
level at 5 mm/h (CSI at +30 min: 0.591 vs 0.538, and 0.287 vs 0.291).

## Hail

### Moving POH

The 150 moments with the largest area of POH ≥ 50% from 12 May to 30
September 2026, at most three a day and two hours apart (70 days). Four
ways of forecasting POH, scored against the POH observed later:

1. moved and smoothed like the rain (what radarpoint did before the fix);
2. moved without smoothing (what it does now, `Nowcast.HailFields`);
3. moved without smoothing, then the maximum within a square growing 1 km
   every 10 minutes;
4. persistence.

| CSI | POH ≥ 50%: 1 | 2 | 3 | 4 | POH ≥ 80%: 1 | 2 | 3 | 4 |
|---|---|---|---|---|---|---|---|---|
| +15 min | 0.215 | 0.229 | 0.225 | 0.137 | 0.135 | 0.181 | 0.174 | 0.097 |
| +30 min | 0.046 | 0.073 | 0.075 | 0.033 | 0.008 | 0.050 | 0.050 | 0.015 |
| +60 min | 0.002 | 0.010 | 0.013 | 0.006 | 0 | 0.006 | 0.007 | 0.002 |

The smoothing of the rain wiped out hail cells, a few kilometres wide,
within half an hour; moving POH unsmoothed is best or level at every lead
and at every FSS scale from 5 km. Hail is far less predictable than rain:
the useful range is about 15–20 minutes.

### Hail probability within 30 minutes

`internal/hailrisk` combines the moved POH with VIL and ETM, moved the same
way, and their growth over the last 10 minutes (signals at the pixel and
within 5 km). The logistic model was fitted on the even days of the 150
cases (rows near storms written by `seasonverify hailfeat`) and scored on
the odd days, at pixels without hail now: "POH ≥ 50% here within 30
minutes?".

| Warning | POD | FAR | Lead |
|---|---|---|---|
| Moved POH at the point | 0.354 | 0.568 | 12.0 min |
| Model, same FAR | 0.409 | 0.564 | 13.0 min |
| Model, same POD | 0.349 | 0.521 | 12.7 min |
| Moved POH within 5 km | 0.732 | 0.814 | 14.6 min |
| Model, same POD as within 5 km | 0.731 | 0.782 | 14.6 min |

Simple rules on VIL and ETM ("moved POH, or VIL high, growing and high
tops") gained almost nothing. Of the hail that moved POH misses, only 11%
had VIL ≥ 10 kg/m² at the point: mostly new cells.

Reliability on the odd days: a probability of 2–5% verified at 4%, 5–10%
at 9%, 10–20% at 17%, 20–30% at 26%, 30–50% at 38%, 50–70% at 49% and
70–100% at 67%. Above 50% the model is overconfident, so radarpoint scales
the top of its range down to at most 70%.

### Warnings as a service would send them

2,809 points every 10 km over Italian land with radar coverage, every 5
minutes from 12 May to 30 September 2026 (142 days). A point gets at most
one warning per hour, and none while hail is already over it. A warning is
right if POH ≥ 50% follows at the point within 30 minutes. A hail episode
is POH ≥ 50% at the point after at least an hour without; it is warned if a
warning came before it started, late if only in its first half hour.
There were 3.4 episodes per point.

| Warning when | Warnings per point | Without hail at the point | Episodes warned | Late | Median lead |
|---|---|---|---|---|---|
| POH ≥ 50% within 5 km now | 14.0 | 80% | 78% | 16% | 10 min |
| Moved POH ≥ 50% within 5 km | 18.2 | 86% | 70% | 17% | 20 min |
| Moved POH ≥ 50% at the point | 5.2 | 60% | 57% | 6% | 15 min |
| Hail probability ≥ 10% | 10.8 | 77% | 70% | 9% | 20 min |
| Hail probability ≥ 20% | 6.6 | 65% | 64% | 7% | 15 min |
| Hail probability ≥ 30% | 4.6 | 57% | 55% | 6% | 15 min |
| Hail probability ≥ 50% | 2.5 | 44% | 39% | 3% | 10 min |

On the 107 days not used to fit the model (the odd days of its cases, and
every day without a case) the figures are the same within about a point.
At 10% the model warns as many episodes as the moved POH within 5 km, as
early, with 40% fewer warnings. The threshold trades warnings for
detection; it suits a setting per user.

## Limits

- **Hail is radar POH, not hail on the ground.** POH estimates hail from
  how high strong echoes rise above the freezing level; it may count more
  episodes than fall. The European Severe Weather Database has ground
  reports, but its data is for non-commercial use only.
- **The hail model was fitted and scored on the same season**, on
  different days. A second season will tell if it holds.
- **Scores are per pixel of 1 km.** A forecast at a nearby pixel counts as
  wrong; FSS and the warnings within 5 km show how much that matters.
- **2020 is not today's radar network.** Changes in the network or in the
  processing since then would penalise IRENE, which learnt from 2021–2025.
- **Samples are small for rare events**: 120 and 102 rain cases, 150 hail
  moments.

## Reproducing

Everything runs on a CPU. The Go tools are in `cmd/`; the Python scripts in
[`verify/`](../verify) run in a Docker image built on the IRENE one.

```
docker build -t radarpoint-irene irene/
docker build -t radarpoint-verify verify/
# IRENE's checkpoint (770 MB) into a volume, once
docker run --rm -v irene-model:/model radarpoint-irene python -c "import server; server.load_model()"
```

**Rain, 2026.** Download the season (resumable; about two and a half hours
for four products), export the cases, run IRENE, score:

```
go run ./cmd/dpcarchive --from 2026-05-12T00:00:00Z --products SRI,POH,VIL,ETM
go run ./cmd/seasonverify cases --from 2026-05-12T00:00:00Z --to 2026-07-01T00:00:00Z --storms 30 --random 10 --out cases2026
docker run --rm -v $PWD:/w -v irene-model:/model radarpoint-verify python run_irene.py /w/cases2026 /w/irene2026
go run ./cmd/seasonverify score --cases cases2026 --irene irene2026
```

The 102 cases above are four such exports, merged into one directory with
their `cases.csv` concatenated: May–June (30 storms, 10 random), July (20,
5), August (20, 5) and September (15, 10); fewer random moments were
found where storms took most days.

**Rain, 2020.** Export the cases from the archive (no account needed),
then run IRENE and score as above:

```
docker run --rm -v $PWD:/w radarpoint-verify python archive_cases.py /w/cases2020
```

**IRENE's calibration:**

```
docker run --rm -v $PWD:/w radarpoint-verify python irene_calibration.py /w/cases2020 /w/irene2020
```

**Hail.** Score POH on the 150 moments and keep their times; write the
training rows and fit the model; simulate the warnings:

```
go run ./cmd/seasonverify hail --from 2026-05-12T00:00:00Z --to 2026-10-02T12:00:00Z --n 150 --cases-out hail-cases.txt
go run ./cmd/seasonverify hailfeat --cases hail-cases.txt --out hailfeat.bin
docker run --rm -v $PWD:/w radarpoint-verify python hail_model.py /w/hailfeat.bin
go run ./cmd/seasonverify sites --from 2026-05-12T03:00:00Z --to 2026-10-01T00:00:00Z --cases hail-cases.txt
```

`hail_model.py` prints the coefficients of `internal/hailrisk`;
`irene_calibration.py` prints the table of `internal/irene/calibrate.go`.
On 12 CPUs, `hail` takes about 15 minutes, `sites` about an hour, and IRENE
about 50 seconds per case.
