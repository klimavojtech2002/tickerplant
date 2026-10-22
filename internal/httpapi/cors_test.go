package httpapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidateCORSOrigin(t *testing.T) {
	for _, tc := range []struct {
		origin string
		ok     bool
	}{
		// accepted
		{"", true},
		{"*", true},
		{"http://localhost:3000", true},
		{"https://app.example", true},
		{"http://app.example:8080", true},
		{"http://127.0.0.1:8080", true},
		{"http://[::1]:3000", true},
		{"http://[2001:db8::1]:3000", true},
		{"http://a-b.c1.example", true},
		{"http://[::1]", true},
		{"https://app.example:8443", true},
		// refused: the scheme
		{"ftp://x", false},
		{"null", false},
		{"app.example", false},
		{"HTTPS://app.example", false},
		// refused: more than an origin
		{"https://app.example/", false},
		{"https://app.example/path", false},
		{"https://app.example?x=1", false},
		{"https://app.example?", false},
		{"https://app.example#f", false},
		{"https://app.example#", false},
		{"https://user@app.example", false},
		// refused: the host
		{"http://:3000", false},
		{"https://App.example", false},
		{"http://[FE80::1]:3000", false},
		{"http://bücher.example", false},
		{"http://a b.example", false},
		{"http://::1", false},
		{"http://host;x", false},
		{`http://ho"st`, false},
		{"https://*.example", false},
		{"http://ho_st:5", false},
		{"http://[fe80::1%25en0]:3000", false},
		{"http://[1.2.3.4]:3000", false},
		{"http://[nonsense]:3000", false},
		{"http://-host", false},
		{"Http://host", false},
		// refused: the port
		{"http://host:", false},
		{"http://[::1]:", false},
		{"http://host:0", false},
		{"http://host:abc", false},
		{"http://host:99999999999999999999", false},
		{"http://host:+80", false},
		{"https://app.example:65536", false},
		{"https://app.example:03000", false},
		{"https://app.example:443", false},
		{"http://app.example:80", false},
	} {
		t.Run(tc.origin, func(t *testing.T) {
			err := ValidateCORSOrigin(tc.origin)
			if tc.ok && err != nil {
				t.Fatalf("ValidateCORSOrigin(%q) = %v, want accepted", tc.origin, err)
			}
			if !tc.ok && !errors.Is(err, ErrConfig) {
				t.Fatalf("ValidateCORSOrigin(%q) = %v, want an ErrConfig error", tc.origin, err)
			}
		})
	}
}

// New and the validator are one rule: whatever the validator refuses, New refuses.
func TestNewRefusesWhatTheValidatorRefuses(t *testing.T) {
	good := Config{Hub: newHub(t), Engine: &stubEngine{}}
	for _, origin := range []string{"https://app.example/", "ftp://x", "http://host:", "https://App.example"} {
		cfg := good
		cfg.CORSOrigin = origin
		if h, err := New(cfg); !errors.Is(err, ErrConfig) || h != nil {
			t.Errorf("New with CORSOrigin %q = %v, %v; want a nil handler and ErrConfig", origin, h, err)
		}
	}
	for _, origin := range []string{"", "*", "https://app.example"} {
		cfg := good
		cfg.CORSOrigin = origin
		if h, err := New(cfg); err != nil || h == nil {
			t.Errorf("New with CORSOrigin %q = %v, %v; want a handler", origin, h, err)
		}
	}
}

func TestCorsHeaders(t *testing.T) {
	req := func(origin string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		return r
	}
	t.Run("any origin: allow-origin star, no Vary", func(t *testing.T) {
		for _, policy := range []string{"", "*"} {
			h := http.Header{}
			corsHeaders(h, policy, req("https://other.example"))
			if h.Get("Access-Control-Allow-Origin") != "*" || len(h.Values("Vary")) != 0 {
				t.Fatalf("policy %q: headers = %v", policy, h)
			}
		}
	})
	t.Run("one origin, matching request", func(t *testing.T) {
		h := http.Header{}
		corsHeaders(h, "https://app.example", req("https://app.example"))
		if h.Get("Access-Control-Allow-Origin") != "https://app.example" || h.Get("Vary") != "Origin" {
			t.Fatalf("headers = %v", h)
		}
	})
	for name, origin := range map[string]string{"another origin": "https://other.example", "no Origin header": ""} {
		t.Run("one origin, "+name, func(t *testing.T) {
			h := http.Header{}
			corsHeaders(h, "https://app.example", req(origin))
			if _, ok := h["Access-Control-Allow-Origin"]; ok || h.Get("Vary") != "Origin" {
				t.Fatalf("headers = %v, want only Vary: Origin", h)
			}
		})
	}
	t.Run("an existing Vary value is kept", func(t *testing.T) {
		h := http.Header{"Vary": {"Accept"}}
		corsHeaders(h, "https://app.example", req(""))
		if got := h.Values("Vary"); len(got) != 2 || got[0] != "Accept" || got[1] != "Origin" {
			t.Fatalf("Vary = %v, want [Accept Origin]", got)
		}
	})
}

// The message says what is wrong, because the value is typed by a person at a command line.
func TestValidateCORSOriginSaysWhatIsWrong(t *testing.T) {
	for origin, want := range map[string]string{
		"HTTPS://app.example":         "scheme must be lowercase",
		"Http://host":                 "scheme must be lowercase",
		"ftp://x":                     "scheme must be http or https",
		"http://::1":                  "must be written in brackets",
		"http://[fe80::1%25en0]:3000": "without a zone",
		"http://[1.2.3.4]:3000":       "invalid",
		"http://[nonsense]:3000":      "invalid",
		"https://App.example":         "lowercase",
		"http://host:abc":             "invalid port",
		"https://app.example:03000":   "leading zero",
		"https://app.example:443":     "default port",
		"https://app.example/x":       "nothing else",
	} {
		err := ValidateCORSOrigin(origin)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ValidateCORSOrigin(%q) = %v, want a message containing %q", origin, err, want)
		}
	}
}
