"""Pick a varied set of verification cases from July–December 2020 of the
IT-DPC-SRI archive (5-minute frames, before IRENE's training period) and
export 6 past and 12 future frames for each, at most one case per day.

    python archive_cases.py OUT_DIR

The archive (CC BY-SA 4.0, Italian Civil Protection Department) is a public
Zarr store on the ECMWF European Weather Cloud; no account is needed. The
output has the layout of seasonverify cases: <tag>_past.npy, <tag>_obs.npy
and cases.csv."""
import csv, os, random, sys
from concurrent.futures import ThreadPoolExecutor
import numpy as np
import s3fs, zarr

OUT = sys.argv[1]
os.makedirs(OUT, exist_ok=True)

fs = s3fs.S3FileSystem(anon=True, client_kwargs={"endpoint_url": "https://object-store.os-api.cci2.ecmwf.int"})
g = zarr.open_consolidated(s3fs.S3Map("mlcast-source-datasets/IT-DPC-SRI/v0.1.0/italian-radar-dpc-sri.zarr", s3=fs), mode="r")
RR = g["RR"]
times = np.datetime64("2010-01-01T00:00") + g["time"][:].astype("timedelta64[m]")
index = {t: i for i, t in enumerate(times)}
step = np.timedelta64(5, "m")

def complete(base):
    """All 18 frames (base-25 min … base+60 min) are in the archive."""
    return all(base + k * step in index for k in range(-5, 13))

cands = [t for t in np.arange(np.datetime64("2020-07-01T01:00"), np.datetime64("2020-12-31T22:00"), np.timedelta64(1, "h"))
         if t in index and complete(t)]
print(f"{len(cands)} candidate moments", flush=True)

def fractions(t):
    a = RR[index[t]]
    v = a[~np.isnan(a)]
    if v.size < a.size // 10:  # mostly no data: never a candidate
        return np.zeros(3)
    return np.array([(v >= 0.5).mean(), (v >= 5).mean(), (v >= 10).mean()])

def score(t):
    # The weaker of two consecutive frames, so a single corrupted frame
    # (the archive has a few, with heavy rain everywhere) cannot win.
    return t, np.minimum(fractions(t), fractions(t - step))

with ThreadPoolExecutor(16) as ex:
    scored = list(ex.map(score, cands))
print("scored", flush=True)

def clean(frames):
    """No frame has far more heavy rain than the window as a whole."""
    counts = np.array([(np.nan_to_num(f) >= 10).sum() for f in frames])
    return counts.max() <= 3 * max(np.median(counts), 100)

def month(t):
    return int(str(t)[5:7])

# Category: (months, sort key, number of cases). Picked in this order, so a
# day used by one category is not reused by the next.
categories = [
    ("storm",  (7, 8, 9),   lambda f: f[2], 40),  # largest area ≥10 mm/h
    ("autumn", (10, 11),    lambda f: f[1], 30),  # largest area ≥5 mm/h
    ("winter", (12,),       lambda f: f[0], 20),  # largest area ≥0.5 mm/h
]
days, picked = set(), []
for name, months, key, n in categories:
    pool = sorted((s for s in scored if month(s[0]) in months), key=lambda s: -key(s[1]))
    got = 0
    for t, f in pool:
        if got == n + 3:  # spares, in case some are skipped
            break
        if str(t)[:10] in days or f[0] < 0.01:
            continue
        picked.append((name, t, f)); days.add(str(t)[:10]); got += 1
# Random moments with at least 1% of the area wet, any month.
rng = random.Random(2020)
pool = [s for s in scored if s[1][0] >= 0.01]
rng.shuffle(pool)
got = 0
for t, f in pool:
    if got == 33:
        break
    if str(t)[:10] in days:
        continue
    picked.append(("random", t, f)); days.add(str(t)[:10]); got += 1

def export(item):
    name, t, f = item
    past = np.stack([RR[index[t + k * step]] for k in range(-5, 1)]).astype("<f4")
    obs = np.stack([RR[index[t + k * step]] for k in range(1, 13)]).astype("<f4")
    if not clean(np.concatenate([past, obs])):
        return None
    tag = str(t).replace("-", "").replace("T", "").replace(":", "")[:12]
    np.save(f"{OUT}/{tag}_past.npy", past)
    np.save(f"{OUT}/{tag}_obs.npy", obs)
    return tag, name, f

want = {"storm": 40, "autumn": 30, "winter": 20, "random": 30}
with ThreadPoolExecutor(4) as ex:
    results = list(ex.map(export, picked))
rows, have = [], {k: 0 for k in want}
for item, r in zip(picked, results):
    if r is None:
        print(f"{item[1]} {item[0]}: skipped, corrupted frame", flush=True)
        continue
    tag, name, f = r
    if have[name] == want[name]:  # spare not needed
        os.remove(f"{OUT}/{tag}_past.npy"); os.remove(f"{OUT}/{tag}_obs.npy")
        continue
    have[name] += 1
    rows.append((tag, name, f"{f[0]:.4f}", f"{f[1]:.4f}", f"{f[2]:.4f}"))
with open(f"{OUT}/cases.csv", "w", newline="") as fh:
    w = csv.writer(fh)
    w.writerow(["tag", "category", "wet05", "wet5", "wet10"])
    w.writerows(sorted(rows))
print("cases per category:", have, flush=True)
