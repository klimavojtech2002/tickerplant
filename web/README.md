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
pnpm format:check   # pnpm format rewrites the files
pnpm test:coverage  # pnpm test runs the same tests without the report
pnpm build
```

The lint config refuses `parseFloat`, `Number.parseFloat`, `.toFixed(` and `Number(...)`: they are the ways a
price or size string becomes a binary float. The one place that needs `Number` (a whole percent from bigint
division, in `lib/ticks.ts`) says why where it disables the rule. `lib/noFloat.test.ts` lints snippets and the
real modules with the real config, so the rule cannot be dropped quietly. It guards against accidents, not against
evasion: `globalThis.parseFloat`, `x['toFixed']()` or a unary `+x` get past it. The pnpm version is the
`packageManager` field of `package.json`; the `Dockerfile` repeats it and must be changed with it.

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
- **A bad frame** (a view with a side out of order, a repeated price, mixed decimal widths or a bad
  `seq`, a telemetry event of the wrong shape, or a crossed book) is dropped in every build. The book on
  screen stays as it was, the status pill shows "N frames dropped", and one line a second at most is
  logged.

## The connection

The status pill is computed from what has arrived, on the browser's own clock:

| Pill                            | When                                                                            |
| ------------------------------- | ------------------------------------------------------------------------------- |
| Connecting…                     | before the first connection opens                                               |
| Live · 1.2 s                    | open; telemetry is fresh; the last view is 1.2 s old                            |
| Live · market quiet             | open; telemetry is fresh; no view for 10 s                                      |
| No data from edge · 4 s         | open, but no telemetry for more than 3 s                                        |
| Reconnecting…                   | the browser is reconnecting by itself, or a rebuilt connection is still opening |
| Reconnecting in 4 s (attempt 3) | the connection was refused; a new one is scheduled                              |

After a refusal (for example the edge's stream cap) the page builds a new connection after a wait
that starts at 1 s, doubles to at most 30 s, has random jitter and never goes below 250 ms. A
panel that fails to render shows "This panel failed to render" and a Retry button; the rest of the
page keeps working.
