# tickerplant dashboard

A thin renderer over the pipeline's HTTP edge: the live top-of-book and depth from
`GET /stream` (one complete view per SSE event) and the health counters from
`GET /metrics`. Prices and sizes are the venue's decimal strings end to end — this
page never parses them into floats (ADR-0018).

## Run

```bash
# terminal 1: the pipeline with its HTTP edge (synthetic feed, no account needed)
go run ./cmd/tickerplant -http :8080

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
