"""Do VIL and ETM improve hail warnings beyond moved POH? Rules and the
logistic model of internal/hailrisk are fitted on the even days of the
rows written by "seasonverify hailfeat" and scored on the odd days. The
coefficients are printed as the Go literal of internal/hailrisk.

    python hail_model.py ROWS_FILE"""
import sys
import numpy as np

a = np.fromfile(sys.argv[1], dtype="<f4").reshape(-1, 16)
a = np.nan_to_num(a)
(poh, poh5, vil, vil5, dvil, dvil5, etm, etm5, detm, detm5, vil0, poh0,
 y, first, day, w) = a.T
y = y.astype(bool)
train, test = day % 2 == 0, day % 2 == 1
print(f"rows {len(a)}, positives {y.sum()}, days {len(np.unique(day))}, base rate {(w*y).sum()/w.sum():.4f}")

def score(warn, m, label=""):
    h = (w * (warn & y))[m].sum(); mi = (w * (~warn & y))[m].sum(); f = (w * (warn & ~y))[m].sum()
    lead = first[m & warn & y].mean()
    pod, far, csi = h / (h + mi), f / (h + f), h / (h + mi + f)
    if label:
        print(f"  {label:58s} POD {pod:.3f}  FAR {far:.3f}  CSI {csi:.3f}  lead {lead:4.1f} min")
    return pod, far, csi

print("\n== Baselines on test days (moved POH ≥ 50%)")
score(poh >= 0.5, test, "moved POH at the point")
score(poh5 >= 0.5, test, "moved POH within 5 km")

print("\n== Hail missed by moved POH at the point (test days): share with ...")
miss = test & y & (poh < 0.5)
for name, v, ths in [("vil_max", vil, [5, 10, 15, 20]), ("dvil_max", dvil, [2, 5, 10]),
                     ("etm_max (m)", etm, [7000, 8000, 9000, 10000]), ("vil_max5", vil5, [10, 20, 30])]:
    print("  " + name + ": " + "  ".join(f"≥{t}: {(w*(v >= t))[miss].sum()/w[miss].sum():.2f}" for t in ths))

print("\n== Rules fitted on even days (best CSI), scored on odd days")
best = {}
grid_v = [5, 8, 10, 12, 15, 20, 25]
grid_d = [0, 2, 4, 6, 8, 10]
grid_e = [0, 6000, 7000, 8000, 9000, 10000]
for name, base in [("POH at point", poh >= 0.5), ("POH within 5 km", poh5 >= 0.5)]:
    cand = []
    for v in grid_v:
        for d in grid_d:
            for e in grid_e:
                warn = base | ((vil >= v) & (dvil >= d) & (etm >= e))
                cand.append((score(warn, train)[2], v, d, e))
    c, v, d, e = max(cand)
    warn = base | ((vil >= v) & (dvil >= d) & (etm >= e))
    score(base, test, f"{name}")
    score(warn, test, f"{name} OR (VIL≥{v} & ΔVIL≥{d} & ETM≥{e} m)")

print("\n== Logistic model on all signals (fitted on even days)")
def feats(m):
    X = np.stack([poh, poh5, np.minimum(vil, 40) / 10, np.minimum(vil5, 40) / 10,
                  np.clip(dvil, -20, 20) / 10, np.clip(dvil5, -20, 40) / 10,
                  etm / 10000, etm5 / 10000, np.clip(detm, -5000, 8000) / 5000,
                  np.clip(detm5, -5000, 10000) / 5000, np.minimum(vil0, 40) / 10, poh0], 1)[m]
    return np.hstack([np.ones((len(X), 1)), X])
Xtr, ytr, wtr = feats(train), y[train].astype(float), w[train]
beta = np.zeros(Xtr.shape[1])
for it in range(25):  # Newton–Raphson
    p = 1 / (1 + np.exp(-np.clip(Xtr @ beta, -30, 30)))
    g = Xtr.T @ (wtr * (ytr - p))
    H = (Xtr * (wtr * p * (1 - p))[:, None]).T @ Xtr + 1e-6 * np.eye(len(beta))
    beta += np.linalg.solve(H, g)
pt = 1 / (1 + np.exp(-np.clip(feats(test) @ beta, -30, 30)))
p_full = np.zeros(len(a)); p_full[test] = pt
names = ["const", "poh", "poh5", "vil", "vil5", "dvil", "dvil5", "etm", "etm5", "detm", "detm5", "vil_now", "poh_now"]
print("  coefficients: " + ", ".join(f"{n} {b:+.2f}" for n, b in zip(names, beta)))
for target_name, warn_base in [("POH at point", poh >= 0.5), ("POH within 5 km", poh5 >= 0.5)]:
    pod_b, far_b, csi_b = score(warn_base, test)
    # Model threshold with the same FAR as the baseline, and with the same POD.
    ths = np.quantile(pt, np.linspace(0.5, 0.9999, 400))
    res = [(th,) + score(p_full >= th, test) for th in ths]
    same_far = min(res, key=lambda r: abs(r[2] - far_b))
    same_pod = min(res, key=lambda r: abs(r[1] - pod_b))
    print(f"  vs {target_name}: POD {pod_b:.3f} FAR {far_b:.3f}")
    score(p_full >= same_far[0], test, f"model, same false alarm rate (p≥{same_far[0]:.2f})")
    score(p_full >= same_pod[0], test, f"model, same detection rate (p≥{same_pod[0]:.2f})")
best_csi = max(((th,) + score(p_full >= th, test) for th in np.linspace(0.05, 0.9, 86)), key=lambda r: r[3])
score(p_full >= best_csi[0], test, f"model, best CSI (p≥{best_csi[0]:.2f})")

print("\n== Reliability of the model probability on odd days (hail within 30 min)")
for lo, hi in [(0, .02), (.02, .05), (.05, .1), (.1, .2), (.2, .3), (.3, .5), (.5, .7), (.7, 1)]:
    m = test & (p_full >= lo) & (p_full < hi)
    if w[m].sum():
        print(f"  {lo*100:3.0f}–{hi*100:3.0f}%: observed {100*(w*y)[m].sum()/w[m].sum():5.1f}%  [{100*w[m].sum()/w[test].sum():5.2f}% of points]")

print("\nGo coefficients for internal/hailrisk:")
print("var beta = [...]float64{" + ", ".join(repr(float(b)) for b in beta) + "}")
