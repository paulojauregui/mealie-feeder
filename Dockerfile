# Stage 1: Static Go 1.27.4 Builder
FROM golang:1.27.4-bookworm AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY configs/ ./configs/
COPY seeds/ ./seeds/
ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -trimpath \
    -ldflags="-s -w -X main.BuildVersion=${VERSION} -X main.BuildCommit=${COMMIT} -X main.BuildDate=${DATE}" \
    -o /out/mealie-feeder ./cmd/mealie-feeder

# Stage 2: Minimal Non-Root Distroless Runtime
FROM gcr.io/distroless/static-debian12:nonroot
USER 10001:10001
WORKDIR /app
COPY --from=builder /out/mealie-feeder /usr/local/bin/mealie-feeder
COPY --from=builder /src/configs/sources.yaml /app/configs/sources.yaml
COPY --from=builder /src/seeds/greatest-hits.yaml /app/seeds/greatest-hits.yaml
ENV MEALIE_FEEDER_SOURCES=/app/configs/sources.yaml \
    MEALIE_FEEDER_SEEDS=/app/seeds/greatest-hits.yaml \
    MEALIE_FEEDER_DB=/var/lib/mealie-feeder/state.db
ENTRYPOINT ["/usr/local/bin/mealie-feeder"]
CMD ["sync"]
