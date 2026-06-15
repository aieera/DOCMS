// Command backfill onboards EXISTING ERP data into SeDoc by enumerating
// customers + documents from a source and feeding synthesized canonical events
// through the same idempotent sync handlers the live worker uses. Re-runnable:
// a second pass over the same data creates nothing new.
//
//	backfill                              # full backfill from the ERP listing API
//	backfill -customers CUST-1,CUST-2     # scope to specific customers (ERP source)
//	backfill -source ndjson -file inv.ndjson   # backfill from an NDJSON inventory file
//
// Reuses the worker's env (SEDOC_INTEGRATION_DB_URL, SEDOC_BASE_URL,
// SEDOC_API_KEY, SEDOC_WORKSPACE_ID, ERP_BASE_URL, rate-limit knobs). The ERP
// render endpoint (ERP_BASE_URL) still resolves document bytes for both sources.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/integration/internal/backfill"
	"github.com/aieera/sedoc/integration/internal/erp"
	"github.com/aieera/sedoc/integration/internal/sedoc"
	"github.com/aieera/sedoc/integration/internal/store"
	syncpkg "github.com/aieera/sedoc/integration/internal/sync"
	"github.com/aieera/sedoc/pkg/database"
)

func main() {
	log := zerolog.New(os.Stderr).With().Timestamp().Str("svc", "integration-backfill").Logger()
	ctx, stop := context.WithCancel(context.Background())
	defer stop()

	fs := flag.NewFlagSet("backfill", flag.ExitOnError)
	source := fs.String("source", "erp", "inventory source: erp | ndjson")
	file := fs.String("file", "", "NDJSON inventory path (required for -source ndjson)")
	customers := fs.String("customers", "", "comma-separated customer_refs to scope (erp source; empty = all)")
	concurrency := fs.Int("concurrency", envInt("SEDOC_INTEGRATION_BACKFILL_CONCURRENCY", 4), "customers processed in parallel")
	_ = fs.Parse(os.Args[1:])

	dsn := env("SEDOC_INTEGRATION_DB_URL", "")
	baseURL := env("SEDOC_BASE_URL", "http://localhost:8081/api/v1")
	apiKey := env("SEDOC_API_KEY", "")
	workspaceID := env("SEDOC_WORKSPACE_ID", "")
	erpBase := env("ERP_BASE_URL", "http://localhost:8095")
	if dsn == "" || apiKey == "" || workspaceID == "" {
		log.Fatal().Msg("SEDOC_INTEGRATION_DB_URL, SEDOC_API_KEY, SEDOC_WORKSPACE_ID are required")
	}

	if err := database.RunMigrations(dsn, "migrations"); err != nil {
		log.Fatal().Err(err).Msg("migrate")
	}
	poolCfg := database.DefaultPoolConfig()
	poolCfg.SkipRLSPostureCheck = true
	pool, err := database.NewPool(ctx, dsn, poolCfg)
	if err != nil {
		log.Fatal().Err(err).Msg("db pool")
	}
	defer pool.Close()

	st := store.New(pool)
	ratePerMin := envInt("SEDOC_INTEGRATION_RATE_PER_MIN", 600)
	rateBurst := envInt("SEDOC_INTEGRATION_RATE_BURST", 20)
	limiter := sedoc.NewLimiter(float64(ratePerMin)/60.0, rateBurst)
	doc := sedoc.New(baseURL, apiKey).WithLimiter(limiter)
	erpClient := erp.NewHTTPClient(erpBase).WithToken(env("ERP_API_TOKEN", ""))
	syncer := syncpkg.New(st, doc, erpClient, syncpkg.Config{
		WorkspaceID: workspaceID, RegionPin: env("SEDOC_REGION_PIN", "us-east-1"),
		Buckets: envInt("SEDOC_INTEGRATION_BUCKETS", 256),
	})

	// Resolve the inventory source. The ERP render endpoint still serves bytes.
	var lister erp.Lister
	sourceArg := ""
	switch *source {
	case "erp":
		lister = erpClient
		sourceArg = strings.TrimSpace(*customers)
	case "ndjson":
		if *file == "" {
			log.Fatal().Msg("-file is required for -source ndjson")
		}
		f, ferr := os.Open(*file)
		if ferr != nil {
			log.Fatal().Err(ferr).Msg("open ndjson")
		}
		defer f.Close()
		nd, nerr := backfill.NewNDJSONSource(f)
		if nerr != nil {
			log.Fatal().Err(nerr).Msg("parse ndjson")
		}
		lister = nd
		sourceArg = *file
	default:
		log.Fatal().Str("source", *source).Msg("unknown source (want erp | ndjson)")
	}

	id, err := st.CreateBackfillRun(ctx, *source, sourceArg, true /* start now */)
	if err != nil {
		log.Fatal().Err(err).Msg("create backfill run")
	}
	run, err := st.GetBackfillRun(ctx, id)
	if err != nil {
		log.Fatal().Err(err).Msg("load backfill run")
	}
	log.Info().Str("run", id.String()).Str("source", *source).Msg("backfill started")

	backfill.NewRunner(st, syncer, lister, *concurrency, log).Process(ctx, run)

	final, err := st.GetBackfillRun(ctx, id)
	if err != nil {
		log.Fatal().Err(err).Msg("reload backfill run")
	}
	fmt.Printf("run_id=%s status=%s total=%d processed=%d failed=%d\n",
		final.ID, final.Status, final.Total, final.Processed, final.Failed)
	if final.Failed > 0 {
		fails, _ := st.ListBackfillFailures(ctx, id, 50)
		for _, f := range fails {
			fmt.Printf("  FAIL %s %s: %s\n", f.CustomerRef, f.Item, f.Error)
		}
		os.Exit(1)
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
