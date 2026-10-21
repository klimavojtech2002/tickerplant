// Package bench measures the pipeline's internal processing latency honestly:
// open-loop (events are due on a fixed schedule, not when the previous one finishes)
// and coordinated-omission-aware (latency is measured from when an event was due, not
// from when we got around to processing it). A stall therefore inflates the tail
// instead of hiding behind a slower send rate — a common measurement
// mistake (ADR-0009). The measured span is internal only: from an event
// being due at the source to hub.Publish returning with the updated view; network is excluded.
package bench
