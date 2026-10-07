"""Minimal HTTP service running the IRENE nowcasting model on CPU.

IRENE (Fondazione Bruno Kessler, BSD 2-Clause, https://huggingface.co/it4lia/irene)
turns the last 6 radar frames (30 minutes of rain rates in mm/h) into an
ensemble of forecasts for the next hour. This service keeps the model loaded
and returns only what radarpointd needs, for every forecast step: the
ensemble mean rain rate and the share of members with rain.

    GET  /health
    POST /forecast?height=1400&width=1200&members=4&steps=12&threshold=0.2

The request body is the past frames as little-endian float32, shape
(6, height, width), oldest first, NaN where there is no data; it may be
gzip-compressed (Content-Encoding: gzip). The response body, gzip-compressed
when the client accepts it, holds for each step the mean as float32
(height × width) followed by the probability as uint8 percent (height × width).
"""

import gzip
import json
import logging
import os
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse

import numpy as np
import torch

MODEL_REPO = "it4lia/irene"
MODEL_REVISION = os.environ.get("IRENE_REVISION", "6a37e54cebe0b5bb876490e88d4facc13197666e")
MODEL_PATH = os.environ.get("IRENE_MODEL", "/model/model.ckpt")
PAST_FRAMES = 6
MAX_STEPS = 12
MAX_MEMBERS = 10

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
log = logging.getLogger("irene")


def strip_training_state(path):
    """Rewrite the checkpoint without the optimizer state, once.

    Two thirds of the published checkpoint is Adam state, useful only for
    training: without it the file drops from 770 to 257 MB and every later
    start needs half the memory to load it.
    """
    ckpt = torch.load(path, map_location="cpu", weights_only=False)
    if "optimizer_states" not in ckpt:
        return
    for key in ("optimizer_states", "lr_schedulers", "loops", "callbacks"):
        ckpt.pop(key, None)
    torch.save(ckpt, path + ".tmp")
    os.replace(path + ".tmp", path)
    log.info("removed the training state from %s", path)


def load_model():
    """Load the checkpoint, downloading it on first start (770 MB)."""
    if not os.path.exists(MODEL_PATH):
        from huggingface_hub import hf_hub_download

        log.info("downloading %s@%s to %s", MODEL_REPO, MODEL_REVISION[:8], MODEL_PATH)
        path = hf_hub_download(MODEL_REPO, "model.ckpt", revision=MODEL_REVISION,
                               local_dir=os.path.dirname(MODEL_PATH))
        if path != MODEL_PATH:
            os.replace(path, MODEL_PATH)
    strip_training_state(MODEL_PATH)
    from convgru_ensemble import RadarLightningModel

    torch.set_num_threads(int(os.environ.get("IRENE_THREADS", os.cpu_count() or 1)))
    t = time.perf_counter()
    model = RadarLightningModel.from_checkpoint(MODEL_PATH, device="cpu")
    log.info("model loaded in %.1fs, %d threads", time.perf_counter() - t, torch.get_num_threads())
    return model


MODEL = load_model()
LOCK = threading.Lock()  # one forecast at a time: each one uses every core


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):
        log.info("%s %s", self.address_string(), fmt % args)

    def reply(self, status, body, content_type="application/json", headers=None):
        if "gzip" in self.headers.get("Accept-Encoding", "") and len(body) > 1024:
            body = gzip.compress(body, compresslevel=1)
            headers = {**(headers or {}), "Content-Encoding": "gzip"}
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(body)))
        for k, v in (headers or {}).items():
            self.send_header(k, v)
        self.end_headers()
        self.wfile.write(body)

    def error(self, status, message):
        self.reply(status, json.dumps({"error": message}).encode())

    def do_GET(self):
        if urlparse(self.path).path != "/health":
            return self.error(404, "not found")
        self.reply(200, json.dumps({"ok": True, "model": f"{MODEL_REPO}@{MODEL_REVISION[:8]}",
                                    "busy": LOCK.locked()}).encode())

    def do_POST(self):
        url = urlparse(self.path)
        if url.path != "/forecast":
            return self.error(404, "not found")
        q = {k: v[0] for k, v in parse_qs(url.query).items()}
        try:
            h, w = int(q["height"]), int(q["width"])
            members = int(q.get("members", 4))
            steps = int(q.get("steps", MAX_STEPS))
            threshold = float(q.get("threshold", 0.2))
        except (KeyError, ValueError):
            return self.error(400, "height and width are required; members, steps, threshold must be numbers")
        if not (1 <= members <= MAX_MEMBERS and 1 <= steps <= MAX_STEPS and 0 < h <= 4096 and 0 < w <= 4096):
            return self.error(400, f"members 1–{MAX_MEMBERS}, steps 1–{MAX_STEPS}, size up to 4096")

        body = self.rfile.read(int(self.headers.get("Content-Length", 0)))
        if self.headers.get("Content-Encoding") == "gzip":
            body = gzip.decompress(body)
        if len(body) != PAST_FRAMES * h * w * 4:
            return self.error(400, f"expected {PAST_FRAMES}×{h}×{w} float32 values, got {len(body)} bytes")
        past = np.frombuffer(body, dtype="<f4").reshape(PAST_FRAMES, h, w)

        with LOCK:
            t = time.perf_counter()
            # One member at a time, folded into running totals: the memory
            # peak is that of a single member, whatever the ensemble size.
            total = np.zeros((steps, h, w), np.float32)
            wet = np.zeros((steps, h, w), np.uint8)
            for _ in range(members):
                m = np.asarray(MODEL.predict(past, forecast_steps=steps, ensemble_size=1))[0, :, :h, :w]
                total += m
                wet += m >= threshold
                del m
            took = time.perf_counter() - t
        mean = (total / members).astype("<f4")                           # (steps, h, w)
        prob = np.rint(wet * (100 / members)).astype(np.uint8)            # (steps, h, w)
        out = b"".join(mean[k].tobytes() + prob[k].tobytes() for k in range(steps))
        log.info("forecast %dx%d, %d members, %d steps in %.1fs", h, w, members, steps, took)
        self.reply(200, out, "application/octet-stream",
                   {"X-Steps": str(steps), "X-Members": str(members), "X-Seconds": f"{took:.1f}"})


if __name__ == "__main__":
    port = int(os.environ.get("PORT", 8000))
    log.info("listening on :%d", port)
    ThreadingHTTPServer(("", port), Handler).serve_forever()
