# IRENE nowcasting service, CPU only.
#
# The official IRENE lockfile installs CUDA builds of PyTorch; on machines
# without a GPU the CPU build is the same model in a far smaller image.
FROM python:3.13-slim

RUN pip install --no-cache-dir torch torchvision --index-url https://download.pytorch.org/whl/cpu \
 && pip install --no-cache-dir "pytorch-lightning>=2.6" numpy huggingface_hub

# The IRENE code (BSD 2-Clause, Fondazione Bruno Kessler) at a fixed revision.
# The 770 MB checkpoint is not baked in: it is downloaded on first start into
# /model, which should be a volume so it survives restarts.
ARG IRENE_REVISION=6a37e54cebe0b5bb876490e88d4facc13197666e
ENV IRENE_REVISION=${IRENE_REVISION}
RUN python -c "from huggingface_hub import snapshot_download; \
snapshot_download('it4lia/irene', revision='${IRENE_REVISION}', \
allow_patterns=['convgru_ensemble/*.py', 'LICENSE'], local_dir='/app')"

WORKDIR /app
COPY server.py .
ENV PYTHONPATH=/app IRENE_MODEL=/model/model.ckpt PORT=8000
EXPOSE 8000
HEALTHCHECK --interval=30s --timeout=5s --start-period=300s \
  CMD python -c "import urllib.request; urllib.request.urlopen('http://localhost:8000/health')"
CMD ["python", "server.py"]
