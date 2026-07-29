# syntax=docker/dockerfile:1

# Builder: go.mod/go.sum first so dependency downloads cache independently of source
# changes.
FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/tickerplant ./cmd/tickerplant

# Final: distroless nonroot already ships CA certs (needed for live wss://) and a
# non-root user (uid 65532) — no shell, no package manager, nothing to exploit beyond
# the binary itself.
FROM gcr.io/distroless/static-debian12:nonroot AS final
COPY --from=builder /out/tickerplant /tickerplant
USER nonroot:nonroot
ENTRYPOINT ["/tickerplant"]
