# tickerplant dashboard

A thin renderer over the pipeline's HTTP edge: the live top-of-book and depth from
`GET /stream` (one complete view per `view` event, and a `telemetry` event each second) and the
health counters from `GET /api/status`. Prices and sizes are the venue's decimal strings end to end — this
page never parses them into floats (ADR-0018).

## Run

```bash
# terminal 1: the pipeline with its HTTP edge (synthetic feed, no account needed)
go run ./cmd/tickerplant -http :8080 -steps 100000000   # the default 5000 steps ends after about 12.5 minutes

# or live:
go run ./cmd/tickerplant -live -venue kraken -symbol BTC/USD -http :8080

# terminal 2: the dashboard
cd web
pnpm install
pnpm dev            # http://localhost:3000, edge defaults to http://127.0.0.1:8080
```

Point it at another edge with `NEXT_PUBLIC_EDGE_URL`.

## Checks

```bash
pnpm lint
pnpm type-check
pnpm test
pnpm build
```

## What the page trusts

Every body from the edge is checked before it is used: `parseStatus`, `parseTelemetry` and `parseView`
in `lib/types.ts` return null for anything the contract does not allow, and the tests read the same
golden files as the Go tests (`internal/httpapi/testdata`), so a renamed field fails on both sides.
Run the tests from a full checkout for that reason.

- **Health.** `/api/status` is polled every 2 s with a 1.5 s limit on each request (headers and
  body). The tiles keep the last good numbers through one or two failed polls and are dimmed and
  marked unavailable after three in a row.
- **Latency** is shown with one decimal and is cut, not rounded, so a figure never reads better than
  it is (1999 ns shows as 1.9 µs).
- **`mode`** must be `synthetic`, `live` or `unknown`; a new mode needs a change here.
- **A bad view frame** (a side out of order, a repeated price, mixed decimal widths, a bad `seq`) is
  logged and dropped, and the book on screen stays as it was. The page does not yet say that frames
  are being dropped.
