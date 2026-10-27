package main

import "testing"

func TestResetDSNGuard(t *testing.T) {
	for _, tc := range []struct {
		dsn     string
		allowed bool
	}{
		{"demo:password@tcp(127.0.0.1:3307)/refund_demo", true},
		{"demo:password@tcp(localhost:3307)/refund_test", true},
		{"demo:password@tcp([::1]:3307)/refund_acceptance", true},
		{"demo:password@tcp(127.0.0.1:3307)/business", false},
		{"demo:password@tcp(10.0.0.1:3307)/refund_demo", false},
		{"demo:password@unix(/tmp/mysql.sock)/refund_demo", false},
		{"demo:password@tcp(127.0.0.1:3307)/", false},
	} {
		if err := validateResetDSN(tc.dsn); (err == nil) != tc.allowed {
			t.Fatalf("allowed=%v err=%v", tc.allowed, err)
		}
	}
}
