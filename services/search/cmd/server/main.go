// Package main boots the VaultDMS search service.
//
// Responsibilities:
//   - Lexical search via OpenSearch (semantic via Qdrant stubbed for Phase 11)
//   - NATS consumers for document/permission/OCR/classification/entity events
//   - REST API: search, autocomplete, saved searches
//   - Index template bootstrap on startup
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"google.golang.org/grpc"

	"github.com/vaultdms/vaultdms/pkg/config"
	"github.com/vaultdms/vaultdms/pkg/database"
	"github.com/vaultdms/vaultdms/pkg/events"
	"github.com/vaultdms/vaultdms/pkg/health"
	"github.com/vaultdms/vaultdms/pkg/logger"
	"github.com/vaultdms/vaultdms/pkg/middleware"
	"github.com/vaultdms/vaultdms/services/search/internal/handler"
	"github.com/vaultdms/vaultdms/services/search/internal/opensearch"
	"github.com/vaultdms/vaultdms/services/search/internal/repository"
	"github.com/vaultdms/vaultdms/services/search/internal/service"
	"github.com/vaultdms/vaultdms/services/search/internal/vector"
)

const serviceName = "search"

var version = "dev"

func main() {
	cfg, err := config.Load(serviceName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config load: %v\n", err)
		os.Exit(1)
	}
	cfg.ServiceVersion = version

	log := logger.New(serviceName, cfg.ServiceVersion, cfg.LogLevel)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// ---- Dependencies ------------------------------------------------------
	pool, err := database.NewPool(ctx, cfg.DatabaseURL, database.DefaultPoolConfig())
	if err != nil {
		log.Fatal(ctx).Err(err).Msg("postgres connect")
	}
	defer pool.Close()

	rdb := redis.NewClient(&redis.Options{Addr: cfg.RedisURL, Password: cfg.RedisPassword, DB: cfg.RedisDB})
	defer func() { _ = rdb.Close() }()

	nc, js, err := events.ConnectNATS(cfg.NATSURL)
	if err != nil {
		log.Fatal(ctx).Err(err).Msg("nats connect")
	}
	defer nc.Close()

	// ---- OpenSearch --------------------------------------------------------
	osCli, err := opensearch.NewReal(ctx, opensearch.Config{
		URL:      cfg.OpenSearchURL,
		Username: cfg.OpenSearchUsername,
		Password: cfg.OpenSearchPassword,
		Insecure: cfg.Environment != "prod",
		Logger:   *log.Z(),
	})
	if err != nil {
		log.Fatal(ctx).Err(err).Msg("opensearch connect")
	}

	// ---- Service layer -----------------------------------------------------
	repo := repository.New(pool)

	// §7.1 / D6 part 2 — optional dense-vector path. Wires only when
	// both env vars are set (dev without intelligence running leaves
	// them empty, and hybrid mode degrades to lexical).
	var vecClient *vector.Client
	if embedURL, qdrantURL := os.Getenv("VAULTDMS_INTELLIGENCE_EMBED_URL"),
		os.Getenv("VAULTDMS_QDRANT_URL"); embedURL != "" && qdrantURL != "" {
		coll := os.Getenv("VAULTDMS_QDRANT_COLLECTION")
		if coll == "" {
			coll = "vaultdms_chunks"
		}
		vecClient, err = vector.New(vector.Config{
			IntelligenceEmbedURL: embedURL,
			QdrantBaseURL:        qdrantURL,
			Collection:           coll,
		})
		if err != nil {
			log.Warn(ctx).Err(err).Msg("vector client init failed; hybrid search disabled")
			vecClient = nil
		}
	}

	svc := service.New(service.Config{
		OS:     osCli,
		Repo:   repo,
		Redis:  rdb,
		Vector: vecClient,
		Logger: *log.Z(),
	})

	// ---- NATS indexer ------------------------------------------------------
	indexer := service.NewIndexer(svc, js, *log.Z())
	if err := indexer.Start(ctx); err != nil {
		log.Fatal(ctx).Err(err).Msg("indexer start")
	}
	defer indexer.Stop()

	// ---- Health ------------------------------------------------------------
	hs := health.NewServer(pool, rdb, nc, nil)
	go func() {
		if err := hs.Start(fmt.Sprintf(":%d", cfg.HealthPort)); err != nil {
			log.Error(ctx).Err(err).Msg("health server")
		}
	}()

	// ---- gRPC (still available for inter-service calls) --------------------
	grpcSrv := grpc.NewServer(grpc.ChainUnaryInterceptor(
		middleware.RecoveryInterceptor(log),
		middleware.CorrelationInterceptor(),
		middleware.TenantInterceptor(pool),
		middleware.RequestLogInterceptor(log),
	))
	grpcLis, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.GRPCPort))
	if err != nil {
		log.Fatal(ctx).Err(err).Msg("grpc listen")
	}
	go func() {
		log.Info(ctx).Int("port", cfg.GRPCPort).Msg("grpc listening")
		if err := grpcSrv.Serve(grpcLis); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			log.Error(ctx).Err(err).Msg("grpc serve")
		}
	}()

	// ---- HTTP REST ---------------------------------------------------------
	mux := http.NewServeMux()
	h := handler.New(svc, *log.Z())
	h.Register(mux)

	httpSrv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.HTTPPort),
		Handler:           middleware.RequireGatewaySignature()(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		log.Info(ctx).Int("port", cfg.HTTPPort).Msg("http listening")
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error(ctx).Err(err).Msg("http serve")
		}
	}()

	// ---- Outbox publisher --------------------------------------------------
	outbox := database.NewOutboxPublisher(pool, js, serviceName, *log.Z())
	go outbox.Start(ctx)

	log.Info(ctx).Str("version", version).Msg(serviceName + " started")
	<-ctx.Done()
	log.Info(context.Background()).Msg(serviceName + " shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	grpcSrv.GracefulStop()
	_ = httpSrv.Shutdown(shutdownCtx)
	_ = hs.Shutdown(shutdownCtx)
	outbox.Stop()
}

// silence unused
var _ zerolog.Logger
