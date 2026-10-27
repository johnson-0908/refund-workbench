package main

import "testing"

func TestPasswordlessServerIsLoopbackOnly(t *testing.T) {
	for _, tc := range []struct {
		address string
		valid   bool
	}{
		{"127.0.0.1:18080", true}, {"localhost:18082", true}, {"[::1]:18080", true},
		{"0.0.0.0:18080", false}, {":18080", false}, {"[::]:18080", false}, {"192.168.1.2:18080", false}, {"evil.invalid:18080", false}, {"invalid", false},
	} {
		if err := validateHTTPAddr(tc.address); (err == nil) != tc.valid {
			t.Fatalf("address=%s valid=%v err=%v", tc.address, tc.valid, err)
		}
	}
}
