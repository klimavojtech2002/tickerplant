# syntax=docker/dockerfile:1

# The image tag fixes the Go that builds the binary; keep it equal to the toolchain line in
# go.mod. GOTOOLCHAIN=local only stops the build from downloading another Go.
FROM golang:1.26.11-alpine@sha256:ecb67f987aa1a57794c91d19ade7ae0209f2f0f5496aaef9e6ae85ddf92d02ab AS builder
ENV GOTOOLCHAIN=local
# The version the binary reports (-version). Pass --build-arg VERSION=v1.2.3 to set it.
ARG VERSION=dev
WORKDIR /src
# go.mod/go.sum first so dependency downloads cache independently of source changes.
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -X github.com/klimavojtech2002/tickerplant/internal/version.Version=${VERSION}" -o /out/tickerplant ./cmd/tickerplant

# Final: distroless nonroot already ships CA certs (needed for live wss://) and a
# non-root user (uid 65532) — no shell, no package manager, nothing to exploit beyond
# the binary itself.
FROM gcr.io/distroless/static-debian12:nonroot AS final
COPY --from=builder /out/tickerplant /tickerplant
USER nonroot:nonroot
ENTRYPOINT ["/tickerplant"]
