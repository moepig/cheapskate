package main

import (
	"strings"
	"testing"
)

// C-6: flag misuse must return an error from run() (ContinueOnError) rather than calling
// os.Exit via flag.ExitOnError, which would kill the test binary (and any embedding process).
func TestFlagMisuseReturnsErrorInsteadOfExiting(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"unknown global flag", []string{"-bogus"}, "flag provided but not defined"},
		{"unknown schedule flag", []string{"-table", "t", "schedule", "rds-instance#db", "-bogus"}, "flag provided but not defined"},
		{"unknown override flag", []string{"-table", "t", "override", "rds-instance#db", "running", "-for", "2h", "-bogus"}, "flag provided but not defined"},
		{"malformed duration", []string{"-table", "t", "override", "rds-instance#db", "running", "-for", "not-a-duration"}, "invalid value"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := run(c.args)
			if err == nil {
				t.Fatalf("run(%v) = nil, want error", c.args)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("run(%v) error = %q, want substring %q", c.args, err.Error(), c.want)
			}
		})
	}
}
