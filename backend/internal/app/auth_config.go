package app

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

func DefaultAuthOrigins() []string {
	return []string{"http://127.0.0.1:5173", "http://localhost:5173", "http://127.0.0.1:18080"}
}

// ValidateAuthHTTPConfig prevents a TLS-facing deployment from accidentally
// issuing transport-insecure cookies, and rejects unusable Secure cookies on
// a non-loopback plain-HTTP origin. Local defaults remain plain HTTP.
func ValidateAuthHTTPConfig(origins []string, secure bool) error {
	if err := ValidateAuthOrigins(origins); err != nil {
		return err
	}
	if origins == nil {
		origins = DefaultAuthOrigins()
	}
	for _, origin := range origins {
		parsed, _ := url.Parse(origin)
		if parsed.Scheme == "https" && !secure {
			return fmt.Errorf("HTTPS origins require SESSION_COOKIE_SECURE=true")
		}
		ip := net.ParseIP(parsed.Hostname())
		local := parsed.Hostname() == "localhost" || (ip != nil && ip.IsLoopback())
		if parsed.Scheme == "http" && secure && !local {
			return fmt.Errorf("SESSION_COOKIE_SECURE=true requires HTTPS for non-loopback origins")
		}
	}
	return nil
}

// ValidateAuthOrigins accepts complete, exact browser origins, never URL
// prefixes, wildcard hosts, paths, credentials, or proxy-derived values.
func ValidateAuthOrigins(origins []string) error {
	if origins == nil {
		return nil
	}
	if len(origins) == 0 {
		return fmt.Errorf("AUTH_ALLOWED_ORIGINS must contain at least one exact origin")
	}
	for _, origin := range origins {
		if origin == "" || strings.ContainsAny(origin, "* \\\t\r\n") {
			return fmt.Errorf("AUTH_ALLOWED_ORIGINS contains an invalid origin")
		}
		parsed, err := url.Parse(origin)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.Hostname() == "" || parsed.User != nil || parsed.Opaque != "" || parsed.Path != "" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawFragment != "" || strings.Contains(origin, "#") {
			return fmt.Errorf("AUTH_ALLOWED_ORIGINS requires http(s)://host[:port] without path, credentials, query, or fragment")
		}
		if strings.HasSuffix(parsed.Host, ":") {
			return fmt.Errorf("AUTH_ALLOWED_ORIGINS contains an empty port")
		}
		if port := parsed.Port(); port != "" {
			number, err := strconv.Atoi(port)
			if err != nil || number < 1 || number > 65535 {
				return fmt.Errorf("AUTH_ALLOWED_ORIGINS contains an invalid port")
			}
		}
		for _, r := range parsed.Host {
			if r < 33 || r > 126 {
				return fmt.Errorf("AUTH_ALLOWED_ORIGINS host must use ASCII/punycode")
			}
		}
	}
	return nil
}

func originAllowed(r *http.Request, origins map[string]struct{}) bool {
	values := r.Header.Values("Origin")
	if len(values) != 1 {
		return false
	}
	_, ok := origins[values[0]]
	return ok
}

func (a *authService) configureHTTP(origins []string, secure bool) error {
	if err := ValidateAuthHTTPConfig(origins, secure); err != nil {
		return err
	}
	if origins == nil {
		origins = DefaultAuthOrigins()
	}
	a.origins = make(map[string]struct{}, len(origins))
	for _, origin := range origins {
		a.origins[origin] = struct{}{}
	}
	a.secureCookie = secure
	return nil
}
