// dms-admin nats — JetStream topology + replay utilities.
//
// Subcommands:
//
//	dms-admin nats bootstrap [--dry-run]     create/update streams + DLQs
//	dms-admin nats replay --stream NAME [--count N]   replay recent events to stdout
//	dms-admin nats list                      print streams + DLQ status
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/redis/go-redis/v9"

	"github.com/vaultdms/vaultdms/pkg/events"
)

// bootstrapLockKey + bootstrapLockTTL coordinate concurrent service
// startups so only one process tries to shape the topology at once.
// Per `final.md` § 4.4, the lock is advisory — NATS itself is safe
// under concurrent AddStream/UpdateStream, so this is belt-and-braces
// to reduce log noise during rolling deploys.
const (
	bootstrapLockKey = "nats:bootstrap:lock"
	bootstrapLockTTL = 60 * time.Second
)

func natsMain(args []string) {
	if len(args) == 0 {
		fmt.Println("Usage: dms-admin nats <subcommand>")
		fmt.Println("Subcommands: bootstrap, replay, list, check")
		os.Exit(2)
	}
	switch args[0] {
	case "bootstrap":
		natsBootstrap(args[1:])
	case "replay":
		natsReplay(args[1:])
	case "list":
		natsList(args[1:])
	case "check":
		natsCheck(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown nats subcommand: %s\n", args[0])
		os.Exit(2)
	}
}

// legacyStreamNames are the pre-Wave-5 stream names that overlap the
// new topology's subjects and must be removed before AddStream on the
// new names succeeds. Deleting a stream drops any unacked messages on
// it — Wave 5's rename is additive for consumers, so production runs
// should drain first. See docs/runbooks/05-event-pipeline.md.
var legacyStreamNames = []string{
	"DOCUMENTS", "AUTH", "WORKFLOWS", "AUDIT",
	"NOTIFICATIONS", "BILLING", "INTELLIGENCE",
}

func natsBootstrap(args []string) {
	fs := flag.NewFlagSet("nats bootstrap", flag.ExitOnError)
	dryRun := fs.Bool("dry-run", false, "print the diff without applying")
	purgeLegacy := fs.Bool("purge-legacy", false, "delete pre-Wave-5 streams (DOCUMENTS/AUTH/...) before creating the new topology")
	_ = fs.Parse(args)

	js, nc := mustConnectJetStream()
	defer nc.Close()

	if *dryRun {
		printDryRun(js)
		return
	}

	if *purgeLegacy {
		for _, name := range legacyStreamNames {
			if _, err := js.StreamInfo(name); err != nil {
				continue
			}
			if err := js.DeleteStream(name); err != nil {
				fmt.Fprintf(os.Stderr, "[warn] delete legacy %s: %v\n", name, err)
			} else {
				fmt.Printf("[legacy] deleted %s\n", name)
			}
		}
	}

	rdb := tryRedis()
	if rdb != nil {
		defer rdb.Close()
		acquired, err := rdb.SetNX(context.Background(), bootstrapLockKey, os.Getpid(), bootstrapLockTTL).Result()
		if err != nil {
			fmt.Fprintf(os.Stderr, "[warn] lock set failed: %v (continuing without lock)\n", err)
		} else if !acquired {
			holder, _ := rdb.Get(context.Background(), bootstrapLockKey).Result()
			fmt.Fprintf(os.Stderr, "[warn] another process holds %s (holder=%s); continuing anyway\n", bootstrapLockKey, holder)
		} else {
			defer func() { _ = rdb.Del(context.Background(), bootstrapLockKey).Err() }()
		}
	}

	report, err := events.EnsureStreams(js, events.DefaultStreams)
	if err != nil {
		fatal("ensure streams: %v", err)
	}
	printReport(report)
}

func natsReplay(args []string) {
	fs := flag.NewFlagSet("nats replay", flag.ExitOnError)
	stream := fs.String("stream", "", "stream name (required)")
	count := fs.Int("count", 50, "max messages to read (most recent)")
	_ = fs.Parse(args)
	if *stream == "" {
		fmt.Fprintln(os.Stderr, "--stream is required")
		os.Exit(2)
	}
	js, nc := mustConnectJetStream()
	defer nc.Close()

	info, err := js.StreamInfo(*stream)
	if err != nil {
		fatal("stream info %s: %v", *stream, err)
	}
	if info.State.Msgs == 0 {
		fmt.Println("(stream is empty)")
		return
	}

	// Compute the starting sequence to replay the last N messages.
	first := info.State.LastSeq - uint64(*count) + 1
	if first < info.State.FirstSeq {
		first = info.State.FirstSeq
	}

	sub, err := js.PullSubscribe("", "",
		nats.BindStream(*stream),
		nats.StartSequence(first),
	)
	if err != nil {
		fatal("subscribe: %v", err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	msgs, err := sub.Fetch(*count, nats.Context(ctx))
	if err != nil && len(msgs) == 0 {
		fatal("fetch: %v", err)
	}
	for i, m := range msgs {
		meta, _ := m.Metadata()
		seq := uint64(0)
		if meta != nil {
			seq = meta.Sequence.Stream
		}
		fmt.Printf("--- %d/%d  seq=%d  subject=%s ---\n", i+1, len(msgs), seq, m.Subject)
		fmt.Println(string(m.Data))
		_ = m.Ack()
	}
}

func natsList(_ []string) {
	js, nc := mustConnectJetStream()
	defer nc.Close()

	fmt.Printf("%-18s %6s %12s %10s  %s\n", "STREAM", "MSGS", "BYTES", "AGE", "SUBJECTS")
	for info := range js.StreamsInfo() {
		fmt.Printf("%-18s %6d %12d %10s  %v\n",
			info.Config.Name,
			info.State.Msgs,
			info.State.Bytes,
			info.Config.MaxAge.Truncate(time.Hour),
			info.Config.Subjects,
		)
	}
}

func printDryRun(js nats.JetStreamContext) {
	fmt.Println("=== nats bootstrap --dry-run ===")
	for _, spec := range events.DefaultStreams {
		info, err := js.StreamInfo(spec.Name)
		if err != nil {
			fmt.Printf("  [ADD]     %s  subjects=%v\n", spec.Name, spec.Subjects)
		} else {
			fmt.Printf("  [KEEP]    %s  existing=%v  desired=%v\n",
				spec.Name, info.Config.Subjects, spec.Subjects)
		}
		if _, err := js.StreamInfo(spec.Name + "_DLQ"); err != nil {
			fmt.Printf("  [ADD-DLQ] %s_DLQ\n", spec.Name)
		}
	}
}

func printReport(r *events.BootstrapReport) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(map[string]any{
		"added":     r.Added,
		"updated":   r.Updated,
		"skipped":   r.Skipped,
		"dlq":       r.DLQ,
	})
}

func mustConnectJetStream() (nats.JetStreamContext, *nats.Conn) {
	url := envOrDefault("VAULTDMS_NATS_URL", envOrDefault("NATS_URL", "nats://localhost:4222"))
	nc, err := nats.Connect(url, nats.Name("dms-admin"), nats.Timeout(5*time.Second))
	if err != nil {
		fatal("nats connect %s: %v", url, err)
	}
	js, err := nc.JetStream()
	if err != nil {
		fatal("jetstream ctx: %v", err)
	}
	return js, nc
}

func tryRedis() *redis.Client {
	addr := envOrDefault("VAULTDMS_REDIS_URL", envOrDefault("REDIS_URL", ""))
	if addr == "" {
		return nil
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		_ = rdb.Close()
		return nil
	}
	return rdb
}
