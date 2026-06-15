// Command bff is the Files BFF [C]: the key-holding API the file-explorer /
// review / dashboard UIs call. It authenticates the ERP user (X-ERP-User,
// stamped upstream by the ERP), checks per-customer authorization via the ERP,
// and proxies to SeDoc with the service API key — which never reaches the
// browser.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/integration/internal/bff"
	"github.com/aieera/sedoc/integration/internal/erp"
	"github.com/aieera/sedoc/integration/internal/sedoc"
	"github.com/aieera/sedoc/integration/internal/store"
	"github.com/aieera/sedoc/pkg/database"
)

func main() {
	log := zerolog.New(os.Stderr).With().Timestamp().Str("svc", "files-bff").Logger()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	dsn := env("SEDOC_INTEGRATION_DB_URL", "")
	apiKey := env("SEDOC_API_KEY", "")
	workspaceID := env("SEDOC_WORKSPACE_ID", "")
	if dsn == "" || apiKey == "" || workspaceID == "" {
		log.Fatal().Msg("SEDOC_INTEGRATION_DB_URL, SEDOC_API_KEY, SEDOC_WORKSPACE_ID are required")
	}
	baseURL := env("SEDOC_BASE_URL", "http://localhost:8081/api/v1")
	searchURL := env("SEDOC_SEARCH_URL", "http://localhost:8086/api/v1")
	erpBase := env("ERP_BASE_URL", "http://localhost:8095")
	workerURL := env("SEDOC_INTEGRATION_WORKER_URL", "http://localhost:8090")
	port := envInt("SEDOC_BFF_HTTP_PORT", 8091)

	poolCfg := database.DefaultPoolConfig()
	poolCfg.SkipRLSPostureCheck = true // integration DB is single-tenant
	pool, err := database.NewPool(ctx, dsn, poolCfg)
	if err != nil {
		log.Fatal().Err(err).Msg("db pool")
	}
	defer pool.Close()

	doc := sedoc.New(baseURL, apiKey)
	doc.SetSearchURL(searchURL)
	erpClient := erp.NewHTTPClient(erpBase).WithToken(env("ERP_API_TOKEN", ""))
	b := bff.New(store.New(pool), doc, erpClient, workspaceID, log).WithWorkerURL(workerURL)

	mux := http.NewServeMux()
	b.Register(mux)
	srv := &http.Server{Addr: fmt.Sprintf(":%d", port), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		log.Info().Int("port", port).Msg("files BFF listening")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error().Err(err).Msg("http serve")
		}
	}()
	<-ctx.Done()
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(sctx)
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
