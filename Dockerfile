# radarpointd: a static Go binary on a minimal base image.
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /radarpointd ./cmd/radarpointd

# distroless/static has CA certificates (for the HTTPS calls to Radar-DPC)
# and runs as a non-root user.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /radarpointd /radarpointd
EXPOSE 8080
ENTRYPOINT ["/radarpointd"]
CMD ["--listen", ":8080"]
