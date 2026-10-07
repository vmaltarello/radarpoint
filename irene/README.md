# IRENE service

A small HTTP service that runs [IRENE](https://huggingface.co/it4lia/irene), the
radar nowcasting model of Fondazione Bruno Kessler (BSD 2-Clause), on CPU.
`radarpointd` sends it the last 30 minutes of radar frames and gets back, for
each 5-minute step of the next hour, the ensemble mean rain rate and the
probability of rain. See the main README for how it is used and how it
compares with the built-in extrapolation.

```
docker build -t radarpoint-irene irene
docker run -p 8000:8000 -v irene-model:/model radarpoint-irene
```

The first start downloads the model (770 MB) into `/model` and rewrites it
without the training state (257 MB). A forecast for the whole Radar-DPC grid
with 4 members takes about 40 s on a 12-core CPU and 75 s on 2 cores; members
are computed one at a time, so it fits in 2 GB of memory.

Environment: `IRENE_MODEL` (checkpoint path), `IRENE_THREADS` (CPU threads,
default all), `PORT` (default 8000).
