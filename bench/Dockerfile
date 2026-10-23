# syntax=docker/dockerfile:1

# The published benchmark run, on Linux. On Windows the clock advances in steps of about half a millisecond,
# too coarse to hold or measure a 50 µs schedule, and the benchmarks refuse to run there. This file runs
# the header program and the benchmarks inside a build and exports the log:
#
#   docker build -f bench/Dockerfile --output type=local,dest=bench/results \
#     --build-arg BENCH_COMMIT=$(git rev-parse HEAD) \
#     --build-arg BENCH_DIRTY=$(test -z "$(git status --porcelain --untracked-files=no)" && echo false || echo true) \
#     --build-arg BENCH_NOTE="Docker Desktop, WSL2 virtual machine on a Windows laptop" \
#     --build-arg RUN_ID=$(date +%s) .
#
# It writes bench/results/bench.txt; rename it to <date>-<cpu>.txt. RUN_ID changes the cache key so the
# benchmark runs again instead of being served from the build cache. The build has no .git, so the commit
# and the clean/dirty flag come in as arguments.
FROM golang:1.26.10-alpine@sha256:9ce231b28998b32e116927c61aa5c406183061d4f3f2d61b292c88e7f6bdf7ca AS run
ENV GOTOOLCHAIN=local CGO_ENABLED=0
ARG BENCH_COMMIT=unknown
ARG BENCH_DIRTY=true
ARG BENCH_NOTE=none
ARG RUN_ID=0
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY internal ./internal
COPY bench ./bench
RUN mkdir /out && echo "run $RUN_ID" > /dev/null && { \
      BENCH_COMMIT="$BENCH_COMMIT" BENCH_DIRTY="$BENCH_DIRTY" BENCH_NOTE="$BENCH_NOTE" go run ./bench/header; \
      go test -run '^$' -bench . -benchtime=1x -count=10 -cpu 4 ./bench; \
    } > /out/bench.txt 2>&1 || { cat /out/bench.txt; exit 1; }

FROM scratch
COPY --from=run /out/bench.txt /bench.txt
