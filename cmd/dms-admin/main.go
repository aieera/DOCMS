// dms-admin is a CLI tool for SeDoc operations:
//
//	dms-admin nats ...   — JetStream topology and replay utilities
//	dms-admin kms ...    — per-tenant KEK operations (list, create, rotate, rewrap)
//	dms-admin seed       — seed test data (not yet implemented)
//
// Tenant KEK rotation is `dms-admin kms rotate --tenant <uuid>`.
package main

import (
	"fmt"
	"io"
	"os"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run dispatches one command and returns the process exit code.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		usage(stdout)
		return 1
	}
	switch args[0] {
	case "seed":
		fmt.Fprintln(stdout, "seed: not yet implemented")
	case "nats":
		natsMain(args[1:])
	case "kms":
		kmsMain(args[1:])
	default:
		fmt.Fprintf(stderr, "unknown command: %s\n", args[0])
		usage(stderr)
		return 1
	}
	return 0
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "Usage: dms-admin <command>")
	fmt.Fprintln(w, "Commands: seed, nats, kms")
	fmt.Fprintln(w, "To rotate a tenant KEK: dms-admin kms rotate --tenant <uuid>")
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
