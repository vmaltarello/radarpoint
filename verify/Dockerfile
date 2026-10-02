# The verification scripts: the IRENE image (build it first from irene/)
# plus the readers of the IT-DPC-SRI Zarr archive.
#
#   docker build -t radarpoint-irene irene/
#   docker build -t radarpoint-verify verify/
ARG BASE=radarpoint-irene
FROM ${BASE}
RUN pip install --no-cache-dir "zarr<3" s3fs fsspec numcodecs
# The bundle of certifi lacks the root of the ECMWF object store; the
# system bundle has it.
ENV SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt AWS_CA_BUNDLE=/etc/ssl/certs/ca-certificates.crt
COPY *.py /verify/
WORKDIR /verify
