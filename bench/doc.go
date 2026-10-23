// Package bench measures the pipeline's internal processing latency honestly:
// open-loop (events are due on a fixed schedule, not when the previous one finishes)
// and coordinated-omission-aware (latency is measured from when an event was due, not
// from when we got around to processing it). A stall therefore inflates the tail
// instead of hiding behind a slower send rate — a common measurement
// mistake (ADR-0009). Network is excluded. Three spans are reported and kept apart: the
// engine span (frame taken off the channel to the view stored), due-to-done (an event
// due at the source to hub.Publish returning), and the Publish call alone.
package bench
