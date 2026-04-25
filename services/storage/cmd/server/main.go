// Package main boots the VaultDMS storage service.
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
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/vaultdms/vaultdms/pkg/config"
	pkgcrypto "github.com/vaultdms/vaultdms/pkg/crypto"
	"github.com/vaultdms/vaultdms/pkg/database"
	"github.com/vaultdms/vaultdms/pkg/events"
	"github.com/vaultdms/vaultdms/pkg/health"
	"github.com/vaultdms/vaultdms/pkg/logger"
	"github.com/vaultdms/vaultdms/pkg/middleware"
	"github.com/vaultdms/vaultdms/pkg/storage"

	vaultdmsv1 "github.com/vaultdms/vaultdms/proto/gen/go/vaultdms/v1"
	"github.com/vaultdms/vaultdms/services/storage/internal/handler"
	"github.com/vaultdms/vaultdms/services/storage/internal/repository"
	"github.com/vaultdms/vaultdms/services/storage/internal/scanner"
	"github.com/vaultdms/vaultdms/services/storage/internal/service"
)

const serviceName = "storage"

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

	s3c, err := storage.NewS3Client(cfg.S3Endpoint, cfg.S3AccessKey, cfg.S3SecretKey, cfg.S3UseSSL)
	if err != nil {
		log.Fatal(ctx).Err(err).Msg("s3 connect")
	}

	scannerClient := scanner.New(cfg.ClamAVAddr, 2*time.Minute)

	// ---- Policy gRPC client ------------------------------------------------
	// Dev dials insecure; prod swaps for mTLS. Failure at startup logs a
	// warning and the service runs with the deny-all fallback — every
	// InitiateUpload permission check then returns Forbidden until Policy
	// is reachable.
	var policyClient vaultdmsv1.PolicyServiceClient
	policyConn, perr := grpc.DialContext(ctx, cfg.PolicyServiceAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
		grpc.WithTimeout(5*time.Second),
	)
	if perr != nil {
		log.Warn(ctx).Err(perr).Str("addr", cfg.PolicyServiceAddr).
			Msg("policy service unreachable at startup; uploads will be denied until it comes up")
	} else {
		policyClient = vaultdmsv1.NewPolicyServiceClient(policyConn)
		defer func() { _ = policyConn.Close() }()
	}

	// ---- Envelope encryption KeyManager -----------------------------------
	// Dev path: LocalKeyManager reads VAULTDMS_LOCAL_KEK (base64 32-byte AES
	// key). Prod should swap for pkgcrypto.VaultKeyManager or AWSKMS. If
	// LocalKEK is unset, encryption is disabled — bucket-level SSE-AES256
	// still provides at-rest encryption but there's no per-tenant KEK
	// isolation.
	var km pkgcrypto.KeyManager
	if cfg.LocalKEK != "" {
		lkm, kerr := pkgcrypto.NewLocalKeyManager(cfg.LocalKEK, func(msg string) {
			log.Warn(ctx).Msg(msg)
		})
		if kerr != nil {
			log.Warn(ctx).Err(kerr).Msg("local kek invalid; encryption at rest disabled")
		} else {
			km = lkm
		}
	} else {
		log.Warn(ctx).Msg("VAULTDMS_LOCAL_KEK not set; envelope encryption disabled")
	}

	// ---- Service ----------------------------------------------------------
	repos := repository.New(pool)
	plans := service.NewPlanLookup(pool, rdb, 5*1024*1024*1024)
	svc := service.New(service.Config{
		Pool:             pool,
		Repos:            repos,
		S3:               s3c,
		Scanner:          scannerClient,
		Outbox:           database.NewOutboxRepository(),
		Policy:           policyClient,
		Plans:            plans,
		KMS:              km,
		Logger:           *log.Z(),
		DefaultRegion:    cfg.Region,
		QuarantineBucket: "dms-quarantine",
		PublicUploadBase: cfg.S3PublicBase,
		// TODO(per-tenant-kek): Single KEK across all tenants. Finding k in
		// docs/audit/04-antipatterns.md; target design + migration plan in
		// docs/tech-debt/per-tenant-kek.md.
		TenantKEKID:      "vaultdms-storage-default",
	})

	// ---- Health ------------------------------------------------------------
	hs := health.NewServer(pool, rdb, nc, s3c)
	go func() {
		if err := hs.Start(fmt.Sprintf(":%d", cfg.HealthPort)); err != nil {
			log.Error(ctx).Err(err).Msg("health server")
		}
	}()

	// ---- gRPC --------------------------------------------------------------
	grpcSrv := grpc.NewServer(grpc.ChainUnaryInterceptor(
		middleware.RecoveryInterceptor(log),
		middleware.CorrelationInterceptor(),
		middleware.TenantInterceptor(pool),
		middleware.UserIdentityInterceptor(),
		middleware.RequestLogInterceptor(log),
	))
	vaultdmsv1.RegisterStorageServiceServer(grpcSrv, handler.New(svc))

	grpcLis, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.GRPCPort))
	if err != nil {
		log.Fatal(ctx).Err(err).Msg("grpc listen")
	}
	go func() {
		log.Info(ctx).Int("port", cfg.GRPCPort).Msg("storage grpc listening")
		if err := grpcSrv.Serve(grpcLis); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			log.Error(ctx).Err(err).Msg("grpc serve")
		}
	}()

	// ---- Outbox publisher --------------------------------------------------
	outbox := database.NewOutboxPublisher(pool, js, serviceName, *log.Z())
	go outbox.Start(ctx)

	// ---- Blob reaper -------------------------------------------------------
	// Sweeps zero-reference content_blobs older than 24h, deletes the S3
	// object, then removes the row. Runs hourly. Requires the DB role to
	// have BYPASSRLS (or RLS policies configured to permit row_security=off)
	// since the reaper scans across tenants.
	reaper := service.NewBlobReaper(pool, repos, s3c, *log.Z())
	go reaper.Start(ctx)

	// ---- Scan reconciler ---------------------------------------------------
	// Sweeps scan_results rows stuck in 'pending' > 1h so an upload never
	// hangs silently when finalize crashes mid-way. See reconcile.go.
	reconciler := service.NewScanReconciler(pool, repos, *log.Z())
	go reconciler.Start(ctx)

	log.Info(ctx).Str("version", version).Msg(serviceName + " started")
	<-ctx.Done()
	log.Info(context.Background()).Msg(serviceName + " shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	grpcSrv.GracefulStop()
	_ = hs.Shutdown(shutdownCtx)
	outbox.Stop()
	reaper.Stop()
	reconciler.Stop()
	_ = http.ListenAndServe // retained for future /metrics wiring
}
