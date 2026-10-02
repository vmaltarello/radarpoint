"""Run IRENE on every case of a directory and save, per case, the ensemble
mean (mm/h) and the share of members with rain ≥ 0.2 mm/h (percent), as
<tag>_mean.npy (float32) and <tag>_prob.npy (uint8). Cases that already
have both outputs are skipped, so an interrupted run resumes.

    python run_irene.py CASES_DIR OUT_DIR [MEMBERS]

It runs in the image of irene/, which has the IRENE code in /app; the
checkpoint is read from $IRENE_MODEL (default /model/model.ckpt)."""
import glob, os, sys, time
import numpy as np
import torch

sys.path.insert(0, "/app")
from convgru_ensemble import RadarLightningModel

cases_dir, out_dir = sys.argv[1], sys.argv[2]
members = int(sys.argv[3]) if len(sys.argv) > 3 else 4
os.makedirs(out_dir, exist_ok=True)
torch.set_num_threads(os.cpu_count())
model = RadarLightningModel.from_checkpoint(os.environ.get("IRENE_MODEL", "/model/model.ckpt"), device="cpu")

cases = sorted(glob.glob(f"{cases_dir}/*_past.npy"))
for i, path in enumerate(cases):
    tag = os.path.basename(path)[:12]
    mean_p, prob_p = f"{out_dir}/{tag}_mean.npy", f"{out_dir}/{tag}_prob.npy"
    if os.path.exists(mean_p) and os.path.exists(prob_p):
        continue
    past = np.load(path)
    t = time.perf_counter()
    ens = np.asarray(model.predict(past, forecast_steps=12, ensemble_size=members))[:, :, : past.shape[1], : past.shape[2]]
    prob = np.rint((ens >= 0.2).mean(0) * 100).astype(np.uint8)
    # Temporary names first, so a crash never leaves half a file.
    np.save(mean_p + ".tmp.npy", ens.mean(0).astype("<f4"))
    np.save(prob_p + ".tmp.npy", prob)
    os.replace(mean_p + ".tmp.npy", mean_p)
    os.replace(prob_p + ".tmp.npy", prob_p)
    print(f"case {i + 1}/{len(cases)} {tag}: {time.perf_counter() - t:.0f}s", flush=True)
print("all done", flush=True)
