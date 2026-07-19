package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
		{"unknown schedule flag", []string{"-table", "t", "schedule", "--tag", "dev", "-bogus"}, "flag provided but not defined"},
		{"unknown override flag", []string{"-table", "t", "override", "--tag", "dev", "running", "-for", "2h", "-bogus"}, "flag provided but not defined"},
		{"malformed duration", []string{"-table", "t", "override", "--tag", "dev", "running", "-for", "not-a-duration"}, "invalid value"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := run(c.args)
			require.Error(t, err)
			assert.Contains(t, err.Error(), c.want)
		})
	}
}
