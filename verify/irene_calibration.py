"""Observed frequency of rain (≥ 0.2 mm/h) for each share of IRENE members
with rain (0, 1, 2, 3 or 4 of 4) and each 5-minute step, on all cases and
on the odd and even halves, and the Brier score gain of the calibration
fitted on one half and applied to the other. The table for all cases is
printed as the Go literal of internal/irene/calibrate.go.

    python irene_calibration.py CASES_DIR IRENE_DIR"""
import glob, os, sys
import numpy as np

cases_dir, irene_dir = sys.argv[1], sys.argv[2]
tags = sorted(os.path.basename(p)[:12] for p in glob.glob(f"{cases_dir}/*_past.npy"))
count = np.zeros((2, 12, 5), np.int64)  # half, step, level
wet = np.zeros((2, 12, 5), np.int64)
for i, tag in enumerate(tags):
    latest = np.load(f"{cases_dir}/{tag}_past.npy", mmap_mode="r")[-1]
    obs = np.load(f"{cases_dir}/{tag}_obs.npy", mmap_mode="r")
    prob = np.load(f"{irene_dir}/{tag}_prob.npy", mmap_mode="r")
    for k in range(12):
        valid = ~np.isnan(obs[k]) & ~np.isnan(latest)
        lvl = np.rint(prob[k][valid] / 25).astype(np.int64)
        ow = obs[k][valid] >= 0.2
        count[i % 2, k] += np.bincount(lvl, minlength=5)
        wet[i % 2, k] += np.bincount(lvl, weights=ow, minlength=5).astype(np.int64)

print(f"{len(tags)} cases. Observed % of rain for 0/1/2/3/4 members of 4")
print("step   all cases                          odd half                           even half")
for k in range(12):
    row = []
    for c, w in ((count.sum(0)[k], wet.sum(0)[k]), (count[1, k], wet[1, k]), (count[0, k], wet[0, k])):
        row.append(" ".join(f"{100 * w[l] / c[l]:5.1f}" for l in range(5)))
    print(f"+{(k + 1) * 5:2d}m  " + "  |  ".join(row))

levels = np.array([0, 0.25, 0.5, 0.75, 1])
def brier(c, w, p):
    return (c * p**2 - 2 * p * w + w).sum() / c.sum()
print("\nBrier score on the even half, calibration fitted on the odd half:")
for k in (2, 5, 11):
    raw, cal = brier(count[0, k], wet[0, k], levels), brier(count[0, k], wet[0, k], wet[1, k] / count[1, k])
    print(f"  +{(k + 1) * 5}m: raw {raw:.4f}, calibrated {cal:.4f} ({100 * (raw - cal) / raw:.1f}% lower)")

print("\nGo table (all cases):")
for k in range(12):
    f = wet.sum(0)[k] / count.sum(0)[k]
    print("\t{" + ", ".join(f"{v:.3f}" for v in f) + "},")
