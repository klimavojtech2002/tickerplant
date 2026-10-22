package httpapi

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// hostName is a host name or IPv4 address as a browser writes it in an origin: lowercase
// letters, digits and hyphens in dot-separated labels.
var hostName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)

// corsAny is the policy that lets any origin read the edge: the demo profile.
const corsAny = "*"

// ValidateCORSOrigin checks the value of the cors-origin setting. It accepts "*" (and the
// empty string, which means "*"), or one origin in the exact form a browser sends in the
// Origin header: http or https, a lowercase host (a name, an IPv4 address or a bracketed
// IPv6 address) and an optional port that is not the scheme's default. A value a browser
// would never send is refused, because the policy would then never match and nothing would
// say why. Every error wraps ErrConfig.
func ValidateCORSOrigin(s string) error {
	if s == "" || s == corsAny {
		return nil
	}
	bad := func(format string, args ...any) error {
		return fmt.Errorf("%w: cors origin %q: %s", ErrConfig, s, fmt.Sprintf(format, args...))
	}
	u, err := url.Parse(s)
	if err != nil {
		return bad("%v", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return bad("scheme must be http or https")
	}
	if !strings.HasPrefix(s, u.Scheme+"://") {
		return bad("scheme must be lowercase")
	}
	if u.Hostname() == "" {
		return bad("no host")
	}
	if strings.HasSuffix(u.Host, ":") {
		return bad("empty port")
	}
	if u.Hostname() != strings.ToLower(u.Hostname()) {
		return bad("host must be lowercase")
	}
	for _, r := range u.Host {
		if r > 127 {
			return bad("host must be ASCII (a browser sends punycode)")
		}
	}
	if strings.HasPrefix(u.Host, "[") {
		if ip := net.ParseIP(u.Hostname()); ip == nil || !strings.Contains(u.Hostname(), ":") {
			return bad("a host in brackets must be an IPv6 address without a zone")
		}
	} else if strings.Count(u.Host, ":") > 1 {
		return bad("an IPv6 address must be written in brackets")
	} else if !hostName.MatchString(u.Hostname()) {
		return bad("host has characters a browser does not send in an origin")
	}
	if u.Scheme+"://"+u.Host != s {
		return bad("want scheme://host[:port] with nothing else (no path, query, fragment or user)")
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil {
			return bad("port must be a number")
		}
		if p[0] == '0' || n < 1 || n > 65535 {
			return bad("port must be 1 to 65535 without a leading zero")
		}
		if (u.Scheme == "http" && n == 80) || (u.Scheme == "https" && n == 443) {
			return bad("a browser omits the default port, so write the origin without it")
		}
	}
	return nil
}

// corsHeaders applies the cors-origin policy to a response. Under "*" every response
// allows any origin. Under a single origin the response allows it only when the request
// came from it, and always says the answer depends on Origin.
func corsHeaders(h http.Header, origin string, r *http.Request) {
	if origin == "" || origin == corsAny {
		h.Set("Access-Control-Allow-Origin", corsAny)
		return
	}
	h.Add("Vary", "Origin")
	if r.Header.Get("Origin") == origin {
		h.Set("Access-Control-Allow-Origin", origin)
	}
}
