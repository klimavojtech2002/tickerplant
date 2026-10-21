# syntax=docker/dockerfile:1

# The image tag fixes the Go that builds the binary; keep it equal to the toolchain line in
# go.mod. GOTOOLCHAIN=local only stops the build from downloading another Go.
FROM golang:1.26.10-alpine@sha256:9ce231b28998b32e116927c61aa5c406183061d4f3f2d61b292c88e7f6bdf7ca AS builder
ENV GOTOOLCHAIN=local
WORKDIR /src
# go.mod/go.sum first so dependency downloads cache independently of source changes.
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
