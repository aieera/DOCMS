package main

import (
	"bytes"
	"strings"
	"testing"
)

// The replay command validates its flags before touching the database.
func TestOutboxReplayDLQ_FlagValidation(t *testing.T) {
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"no subcommand":  {[]string{"outbox"}, "Subcommands: replay-dlq"},
		"unknown":        {[]string{"outbox", "nope"}, "unknown outbox subcommand: nope"},
		"bad tenant":     {[]string{"outbox", "replay-dlq", "--tenant", "nope"}, `invalid --tenant "nope"`},
		"zero tenant":    {[]string{"outbox", "replay-dlq", "--tenant", "00000000-0000-0000-0000-000000000000"}, "invalid --tenant"},
		"non-positive":   {[]string{"outbox", "replay-dlq", "--limit", "0"}, "--limit must be positive"},
		"undefined flag": {[]string{"outbox", "replay-dlq", "--force"}, "flag provided but not defined"},
	} {
		t.Run(name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if code := run(tc.args, &out, &errOut); code != 2 {
				t.Fatalf("exit=%d, want 2 (stdout=%q stderr=%q)", code, out.String(), errOut.String())
			}
			if !strings.Contains(errOut.String(), tc.want) {
				t.Fatalf("stderr=%q, want it to contain %q", errOut.String(), tc.want)
			}
		})
	}
}
