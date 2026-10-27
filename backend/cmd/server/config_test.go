package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"refundworkbench/internal/app"
)

func configLookup(values map[string]string) envLookup {
	return func(name string) (string, bool) { value, ok := values[name]; return value, ok }
}

func TestDeploymentServerConfigDefaultsAndExplicitOptIn(t *testing.T) {
	cfg, err := loadServerConfig(configLookup(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddr != "127.0.0.1:18080" || cfg.DSN != app.DefaultDSN || cfg.Origins != nil || cfg.AllowNonLoopback || cfg.CookieSecure || cfg.EnableLegacy || cfg.EnableDataReset || cfg.AllowDockerReset {
		t.Fatal("safe local defaults changed")
	}
	values := map[string]string{"HTTP_ADDR": "0.0.0.0:18080", "ALLOW_NON_LOOPBACK_HTTP": "true", "AUTH_ALLOWED_ORIGINS": "http://192.0.2.1:8080", "MYSQL_HOST": "mysql", "MYSQL_USER": "refundapp", "MYSQL_PASSWORD": "dummy", "ALLOW_DOCKER_DEMO_RESET": "true"}
	cfg, err = loadServerConfig(configLookup(values))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.AllowNonLoopback || !cfg.AllowDockerReset || cfg.EnableDataReset || cfg.EnableLegacy || cfg.CookieSecure || len(cfg.Origins) != 1 || cfg.Origins[0] != values["AUTH_ALLOWED_ORIGINS"] {
		t.Fatal("Docker configuration changed unrelated security flags")
	}
	values["AUTH_ALLOWED_ORIGINS"] = " https://erp.example.invalid , https://erp2.example.invalid:8443 "
	values["SESSION_COOKIE_SECURE"] = "true"
	cfg, err = loadServerConfig(configLookup(values))
	if err != nil || !cfg.CookieSecure || len(cfg.Origins) != 2 || cfg.Origins[0] != "https://erp.example.invalid" {
		t.Fatal("HTTPS origin list failed")
	}
}

func TestDeploymentServerConfigRejectsUnsafeAndMistypedValues(t *testing.T) {
	for _, values := range []map[string]string{
		{"HTTP_ADDR": "0.0.0.0:18080"},
		{"HTTP_ADDR": "0.0.0.0:18080", "ALLOW_NON_LOOPBACK_HTTP": "true"},
		{"HTTP_ADDR": "0.0.0.0:18080", "AUTH_ALLOWED_ORIGINS": "http://192.0.2.1:8080"},
		{"HTTP_ADDR": "evil.invalid:18080", "ALLOW_NON_LOOPBACK_HTTP": "true", "AUTH_ALLOWED_ORIGINS": "http://192.0.2.1:8080"},
		{"HTTP_ADDR": ""}, {"HTTP_ADDR": "localhost:abc"}, {"HTTP_ADDR": "localhost:0"}, {"HTTP_ADDR": "localhost:65536"},
		{"AUTH_ALLOWED_ORIGINS": ""}, {"AUTH_ALLOWED_ORIGINS": "*"}, {"AUTH_ALLOWED_ORIGINS": "http://192.0.2.1:8080,"},
		{"AUTH_ALLOWED_ORIGINS": "https://user:dummy-secret@example.invalid"},
		{"AUTH_ALLOWED_ORIGINS": "https://erp.example.invalid"},
		{"AUTH_ALLOWED_ORIGINS": "http://192.0.2.1:8080", "SESSION_COOKIE_SECURE": "true"},
	} {
		if _, err := loadServerConfig(configLookup(values)); err == nil {
			t.Fatal("unsafe configuration accepted")
		} else if strings.Contains(err.Error(), "dummy-secret") {
			t.Fatal("secret echoed in error")
		}
	}
	for _, name := range []string{"ALLOW_NON_LOOPBACK_HTTP", "SESSION_COOKIE_SECURE", "ENABLE_DEMO_BEARER", "ENABLE_DATA_RESET", "ALLOW_DOCKER_DEMO_RESET"} {
		for _, value := range []string{"", "TRUE", "1", "yes", " true", "false "} {
			if _, err := loadServerConfig(configLookup(map[string]string{name: value})); err == nil {
				t.Fatalf("mistyped bool accepted %s", name)
			}
		}
		for _, value := range []string{"true", "false"} {
			if _, err := loadServerConfig(configLookup(map[string]string{name: value})); err != nil {
				t.Fatalf("valid bool rejected %s: %v", name, err)
			}
		}
	}
}

func TestDeploymentListenerAllowsOnlyExplicitIPBinding(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:18080", ":18080", "[::]:18080", "192.0.2.1:18080"} {
		if validateHTTPBinding(addr, true, true) != nil {
			t.Fatalf("explicit IP listener rejected %s", addr)
		}
		if validateHTTPBinding(addr, false, true) == nil || validateHTTPBinding(addr, true, false) == nil {
			t.Fatal("missing explicit opt-in accepted")
		}
	}
}

func TestDeploymentHealthcheckRequiresHealthyResponse(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		valid  bool
	}{
		{"healthy", 200, `{"status":"ok"}`, true}, {"unavailable", 503, `{"status":"ok"}`, false},
		{"wrongstatus", 200, `{"status":"unhealthy"}`, false}, {"invalidjson", 200, `{`, false}, {"redirect", 302, `{"status":"ok"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/health" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
					t.Error("healthcheck used unexpected request")
				}
				if tc.status == 302 {
					w.Header().Set("Location", "https://external.invalid")
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			address := strings.TrimPrefix(server.URL, "http://")
			if err := healthcheck(address); (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
			if tc.valid {
				wildcard := strings.Replace(address, "127.0.0.1:", "0.0.0.0:", 1)
				if err := healthcheck(wildcard); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
	if healthcheck("invalid") == nil {
		t.Fatal("invalid health address accepted")
	}
}
