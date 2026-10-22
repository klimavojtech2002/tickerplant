package main

import (
	"bytes"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestHealthcheckPort(t *testing.T) {
	for _, tc := range []struct {
		addr string
		want int
		ok   bool
	}{
		{":8080", 8080, true},
		{"0.0.0.0:8080", 8080, true},
		{"[::]:8080", 8080, true},
		{"host:8080", 8080, true},
		{"127.0.0.1:1", 1, true},
		{":65535", 65535, true},
		{"8080", 0, false},       // no port: nothing before it says it is one
		{"", 0, false},           // not given
		{":0", 0, false},         // listens on a random port, so there is nothing to check
		{":65536", 0, false},     // out of range
		{":http", 0, false},      // a service name is not a number
		{"host:", 0, false},      // empty port
		{"host:80:80", 0, false}, // too many colons
	} {
		got, err := healthcheckPort(tc.addr)
		if tc.ok && (err != nil || got != tc.want) {
			t.Errorf("healthcheckPort(%q) = %d, %v; want %d", tc.addr, got, err, tc.want)
		}
		if !tc.ok && err == nil {
			t.Errorf("healthcheckPort(%q) = %d, nil; want an error", tc.addr, got)
		}
		if !tc.ok && err != nil && !strings.Contains(err.Error(), "-http") {
			t.Errorf("healthcheckPort(%q) error %q does not name -http", tc.addr, err)
		}
	}
}

// portOf returns the port a test server listens on.
func portOf(t *testing.T, srv *httptest.Server) int {
	t.Helper()
	_, p, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	n, _ := strconv.Atoi(p)
	return n
}

func TestRunHealthcheck(t *testing.T) {
	var asked string
	status := http.StatusOK
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.Path
		switch status {
		case http.StatusFound:
			http.Redirect(w, r, "/elsewhere", status)
		case 0: // hang until released
			<-release
		default:
			w.WriteHeader(status)
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) }) // runs before srv.Close: lets a hung handler go

	old := healthTimeout
	healthTimeout = 150 * time.Millisecond
	t.Cleanup(func() { healthTimeout = old })

	t.Run("200 is healthy", func(t *testing.T) {
		status = http.StatusOK
		var out bytes.Buffer
		if code := runHealthcheck(portOf(t, srv), &out); code != 0 || out.Len() != 0 {
			t.Fatalf("code %d, output %q; want 0 and no output", code, out.String())
		}
		if asked != "/healthz" {
			t.Fatalf("asked %q, want /healthz", asked)
		}
	})
	for name, st := range map[string]int{"503": http.StatusServiceUnavailable, "404": http.StatusNotFound, "a redirect": http.StatusFound} {
		t.Run(name+" is not healthy", func(t *testing.T) {
			status = st
			var out bytes.Buffer
			if code := runHealthcheck(portOf(t, srv), &out); code != 1 {
				t.Fatalf("code %d, want 1", code)
			}
			if !strings.Contains(out.String(), "healthcheck failed") || !strings.Contains(out.String(), strconv.Itoa(st)) {
				t.Fatalf("output %q, want one line naming the failure and the status", out.String())
			}
		})
	}
	t.Run("a server that never answers times out", func(t *testing.T) {
		status = 0
		var out bytes.Buffer
		start := time.Now()
		if code := runHealthcheck(portOf(t, srv), &out); code != 1 {
			t.Fatalf("code %d, want 1", code)
		}
		if time.Since(start) > 3*time.Second {
			t.Fatal("the check did not stop at its timeout")
		}
		if !strings.Contains(out.String(), "healthcheck failed") {
			t.Fatalf("output %q", out.String())
		}
	})
	t.Run("nothing listening", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := ln.Addr().(*net.TCPAddr).Port
		ln.Close()
		var out bytes.Buffer
		if code := runHealthcheck(port, &out); code != 1 || !strings.Contains(out.String(), "healthcheck failed") {
			t.Fatalf("code %d, output %q; want 1 and a failure line", code, out.String())
		}
	})
}

// The built command: -healthcheck needs -http, takes the port from it, asks only the
// loopback address and starts nothing else.
func TestMainHealthcheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	port := strconv.Itoa(portOf(t, srv))

	run := func(args ...string) (int, string) {
		cmd := exec.Command(os.Args[0], args...)
		cmd.Env = append(os.Environ(), "TICKERPLANT_AS_MAIN=1")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		err := cmd.Run()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode(), stderr.String()
		}
		if err != nil {
			t.Fatal(err)
		}
		return 0, stderr.String()
	}
	if code, msg := run("-healthcheck", "-http", ":"+port); code != 0 {
		t.Fatalf("healthy edge: exit %d, stderr %q; want 0", code, msg)
	}
	// An address with a different host still checks the loopback address.
	if code, msg := run("-healthcheck", "-http", "192.0.2.1:"+port); code != 0 {
		t.Fatalf("host in -http must be ignored: exit %d, stderr %q", code, msg)
	}
	if code, msg := run("-healthcheck"); code != 2 || !strings.Contains(msg, "-healthcheck needs -http") {
		t.Fatalf("no -http: exit %d, stderr %q; want 2 naming the flags", code, msg)
	}
	if code, msg := run("-healthcheck", "-http", "8080"); code != 2 || !strings.Contains(msg, "-http") {
		t.Fatalf("no port: exit %d, stderr %q; want 2", code, msg)
	}
	srv.Close()
	if code, msg := run("-healthcheck", "-http", ":"+port); code != 1 || !strings.Contains(msg, "healthcheck failed") {
		t.Fatalf("dead edge: exit %d, stderr %q; want 1", code, msg)
	}
}
