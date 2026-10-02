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

The first start downloads the model (770 MB) into `/model`. A forecast for
the whole Radar-DPC grid takes about 85 s with 4 members on a 12-core CPU and
needs about 3 GB of memory.

Environment: `IRENE_MODEL` (checkpoint path), `IRENE_THREADS` (CPU threads,
default all), `PORT` (default 8000).
