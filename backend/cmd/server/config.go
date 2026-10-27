package main

import (
	"fmt"
	"net"
	"refundworkbench/internal/app"
	"strconv"
	"strings"
)

type serverConfig struct {
	HTTPAddr         string
	DSN              string
	AllowNonLoopback bool
	Origins          []string
	CookieSecure     bool
	EnableLegacy     bool
	EnableDataReset  bool
	AllowDockerReset bool
}

type envLookup func(string) (string, bool)

func readBool(lookup envLookup, name string) (bool, error) {
	value, exists := lookup(name)
	if !exists {
		return false, nil
	}
	switch value {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("%s must be exactly true or false", name)
	}
}

func loadServerConfig(lookup envLookup) (serverConfig, error) {
	var cfg serverConfig
	cfg.HTTPAddr = "127.0.0.1:18080"
	if value, exists := lookup("HTTP_ADDR"); exists {
		if value == "" {
			return cfg, fmt.Errorf("HTTP_ADDR must not be empty")
		}
		cfg.HTTPAddr = value
	}
	flags := []struct {
		name string
		dst  *bool
	}{
		{"ALLOW_NON_LOOPBACK_HTTP", &cfg.AllowNonLoopback}, {"SESSION_COOKIE_SECURE", &cfg.CookieSecure},
		{"ENABLE_DEMO_BEARER", &cfg.EnableLegacy}, {"ENABLE_DATA_RESET", &cfg.EnableDataReset}, {"ALLOW_DOCKER_DEMO_RESET", &cfg.AllowDockerReset},
	}
	for _, flag := range flags {
		value, err := readBool(lookup, flag.name)
		if err != nil {
			return cfg, err
		}
		*flag.dst = value
	}
	if value, exists := lookup("AUTH_ALLOWED_ORIGINS"); exists {
		cfg.Origins = strings.Split(value, ",")
		for i := range cfg.Origins {
			cfg.Origins[i] = strings.TrimSpace(cfg.Origins[i])
		}
	}
	if err := app.ValidateAuthHTTPConfig(cfg.Origins, cfg.CookieSecure); err != nil {
		return cfg, err
	}
	if err := validateHTTPBinding(cfg.HTTPAddr, cfg.AllowNonLoopback, cfg.Origins != nil); err != nil {
		return cfg, err
	}
	dsn, err := app.DatabaseDSNFromEnv(lookup)
	if err != nil {
		return cfg, err
	}
	cfg.DSN = dsn
	return cfg, nil
}

func validateHTTPBinding(addr string, allowNonLoopback, explicitOrigins bool) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("HTTP_ADDR must be host:port")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return fmt.Errorf("HTTP_ADDR requires a numeric port between 1 and 65535")
	}
	if host == "127.0.0.1" || host == "localhost" || host == "::1" {
		return nil
	}
	if !allowNonLoopback {
		return fmt.Errorf("non-loopback HTTP_ADDR requires explicit ALLOW_NON_LOOPBACK_HTTP=true")
	}
	if !explicitOrigins {
		return fmt.Errorf("non-loopback HTTP_ADDR requires explicit AUTH_ALLOWED_ORIGINS")
	}
	// Allow wildcard/IP listeners for private container networking, not a
	// hostname whose later DNS changes could silently alter the bound surface.
	if host != "" && net.ParseIP(host) == nil {
		return fmt.Errorf("non-loopback HTTP_ADDR must use an IP listener")
	}
	return nil
}
