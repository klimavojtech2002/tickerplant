package httpapi

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"
)

// recorder wraps a ResponseWriter so the layers around a handler can see what it sent:
// the first status, the bytes, and whether the response has started. It passes flushes
// and write deadlines through to the real connection (FlushError and Unwrap), which the
// SSE handler relies on.
type recorder struct {
	http.ResponseWriter
	status int   // first status sent; 0 until the response starts
	bytes  int64 // body bytes written
}

// started reports whether the response has begun, after which a status can no longer change.
func (r *recorder) started() bool { return r.status != 0 }

func (r *recorder) WriteHeader(code int) {
	if r.started() {
		return
	}
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(p []byte) (int, error) {
	if !r.started() {
		r.WriteHeader(http.StatusOK)
	}
	n, err := r.ResponseWriter.Write(p)
	r.bytes += int64(n)
	return n, err
}

// FlushError flushes the underlying writer. A flush sends the headers, so it starts the
// response (an implicit 200) even if the handler never wrote.
func (r *recorder) FlushError() error {
	if !r.started() {
		r.status = http.StatusOK
	}
	return http.NewResponseController(r.ResponseWriter).Flush()
}

// Unwrap lets http.NewResponseController reach the real writer for write deadlines.
func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// quietRoutes are logged at Debug: an orchestrator polls them every few seconds.
var quietRoutes = map[string]bool{"GET /healthz": true, "GET /readyz": true}

// chain wraps the mux in the edge's layers, outermost first: the access log, which sees
// the final status; the panic recovery; and the headers every response carries. Each layer
// passes the same *http.Request on, because the mux records the matched route on it.
func chain(next http.Handler, log *slog.Logger, corsOrigin string) http.Handler {
	return accessLog(log, recoverer(log, edgeHeaders(corsOrigin, next)))
}

// accessLog writes one line when a response ends: method, route, status, bytes and
// duration. It logs the matched route, not the request path, so a scanner cannot write
// unbounded text into the log, and it leaves out the query string and the client address.
// A long SSE stream logs once, when it ends.
func accessLog(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &recorder{ResponseWriter: w}
		completed := false
		defer func() {
			status := rec.status
			if status == 0 && completed {
				status = http.StatusOK // a handler that wrote nothing still sent a 200
			}
			route := r.Pattern
			level := slog.LevelInfo
			if quietRoutes[route] {
				level = slog.LevelDebug
			}
			if route == "" {
				route = "unmatched"
			}
			log.Log(r.Context(), level, "request", "method", r.Method, "route", route,
				"status", status, "bytes", rec.bytes, "duration", time.Since(start))
		}()
		next.ServeHTTP(rec, r)
		completed = true
	})
}

// recoverer turns a handler panic into a logged 500, so one bad request cannot take the
// connection down silently. If the response has already started, a 500 can no longer be
// sent and a truncated body must not look complete, so the connection is aborted.
func recoverer(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			v := recover()
			if v == nil {
				return
			}
			if v == http.ErrAbortHandler {
				panic(v) // the standard way to abort a response: pass it on, unlogged
			}
			log.Error("handler panic", "panic", fmt.Sprint(v), "stack", string(debug.Stack()))
			if rec, ok := w.(*recorder); ok && rec.started() {
				panic(http.ErrAbortHandler)
			}
			http.Error(w, "internal server error", http.StatusInternalServerError)
		}()
		next.ServeHTTP(w, r)
	})
}

// edgeHeaders sets the headers every response carries, the error responses of the mux
// included: nosniff and the CORS policy.
func edgeHeaders(corsOrigin string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		corsHeaders(h, corsOrigin, r)
		next.ServeHTTP(w, r)
	})
}
