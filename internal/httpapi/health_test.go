package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func healthEdge(t *testing.T, eng *stubEngine) *httptest.Server {
	t.Helper()
	h, err := New(Config{Hub: newHub(t), Engine: eng, PriceScale: 1, SizeScale: 8})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// /healthz says the process is up, whatever the book is doing.
func TestHealthzIsUpWhetherOrNotTheBookIsReady(t *testing.T) {
	eng := &stubEngine{}
	srv := healthEdge(t, eng)
	for _, ready := range []bool{false, true} {
		eng.ready.Store(ready)
		resp, body, err := get(t, http.MethodGet, srv.URL+"/healthz", nil)
		if err != nil || resp.StatusCode != http.StatusOK || body != "ok" {
			t.Fatalf("ready=%v: /healthz = %v, %q, %v; want 200 ok", ready, resp, body, err)
		}
		if resp.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("ready=%v: Cache-Control = %q, want no-store", ready, resp.Header.Get("Cache-Control"))
		}
	}
}

// /readyz follows the engine, in both directions.
func TestReadyzFollowsTheEngine(t *testing.T) {
	eng := &stubEngine{}
	srv := healthEdge(t, eng)
	for _, tc := range []struct {
		ready  bool
		status int
		body   string
	}{
		{false, http.StatusServiceUnavailable, "not ready"},
		{true, http.StatusOK, "ok"},
		{false, http.StatusServiceUnavailable, "not ready"},
	} {
		eng.ready.Store(tc.ready)
		resp, body, err := get(t, http.MethodGet, srv.URL+"/readyz", nil)
		if err != nil || resp.StatusCode != tc.status || body != tc.body {
			t.Fatalf("ready=%v: /readyz = %v, %q, %v; want %d %q", tc.ready, resp, body, err, tc.status, tc.body)
		}
		if resp.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("ready=%v: Cache-Control = %q, want no-store", tc.ready, resp.Header.Get("Cache-Control"))
		}
	}
}
