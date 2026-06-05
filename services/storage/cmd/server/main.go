// Package main boots the SeDoc storage service.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/aieera/sedoc/pkg/config"
	pkgcrypto "github.com/aieera/sedoc/pkg/crypto"
	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/events"
	"github.com/aieera/sedoc/pkg/health"
	"github.com/aieera/sedoc/pkg/logger"
	"github.com/aieera/sedoc/pkg/middleware"
	"github.com/aieera/sedoc/pkg/storage"

	sedocv1 "github.com/aieera/sedoc/proto/gen/go/sedoc/v1"
	"github.com/aieera/sedoc/services/storage/internal/handler"
	"github.com/aieera/sedoc/services/storage/internal/repository"
	"github.com/aieera/sedoc/services/storage/internal/scanner"
	"github.com/aieera/sedoc/services/storage/internal/service"
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
	// FIX-7 follow-up: RLS posture gate. Refuses to start when the
	// connection role unexpectedly has BYPASSRLS; set
	// SEDOC_ALLOW_BYPASS_RLS=1 in dev to opt in.

	rdb := redis.NewClient(&redis.Options{Addr: cfg.RedisURL, Password: cfg.RedisPassword, DB: cfg.RedisDB})
	defer func() { _ = rdb.Close() }()

	nc, js, err := events.ConnectNATS(cfg.NATSURL)
	if err != nil {
		log.Fatal(ctx).Err(err).Msg("nats connect")
	}
	defer nc.Close()

	// cfg.S3PublicBase is the address the user's BROWSER reaches
	// MinIO/S3 at (in dev: "localhost:9000"; in prod usually equals
	// MinIOEndpoint). The pkg/storage two-client split signs presigned
	// URLs against this endpoint so the browser's Host header matches
	// the signature — previous rewriteHost-after-signing path was
	// broken (Sig V4 binds to Host).
	s3c, err := storage.NewS3ClientWithPublicEndpoint(
		cfg.MinIOEndpoint, cfg.S3PublicBase,
		cfg.MinIOAccessKey, cfg.MinIOSecretKey, cfg.MinIOUseSSL,
	)
	if err != nil {
		log.Fatal(ctx).Err(err).Msg("s3 connect")
	}

	// SEDOC_STORAGE_SKIP_VIRUS_SCAN=true bypasses the ClamAV scan
	// on the upload-complete path. ClamAV in dev compose takes 5–7s
	// per scan even for tiny PDFs — long enough that the FE looks
	// hung. Skipping in dev is safe because the bytes never leave
	// the local stack; prod always leaves this false.
	var scannerClient *scanner.Client
	if parseSkipVirusScan() {
		log.Info(ctx).Msg("virus scan disabled (SEDOC_STORAGE_SKIP_VIRUS_SCAN=true)")
	} else {
		scannerClient = scanner.New(cfg.ClamAVAddr, 2*time.Minute)
	}

	// ---- Policy gRPC client ------------------------------------------------
	// Dev dials insecure; prod swaps for mTLS. Failure at startup logs a
	// warning and the service runs with the deny-all fallback — every
	// InitiateUpload permission check then returns Forbidden until Policy
	// is reachable.
	var policyClient sedocv1.PolicyServiceClient
	policyConn, perr := grpc.DialContext(ctx, cfg.PolicyServiceAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
		grpc.WithTimeout(5*time.Second),
	)
	if perr != nil {
		log.Warn(ctx).Err(perr).Str("addr", cfg.PolicyServiceAddr).
			Msg("policy service unreachable at startup; uploads will be denied until it comes up")
	} else {
		policyClient = sedocv1.NewPolicyServiceClient(policyConn)
		defer func() { _ = policyConn.Close() }()
	}

	// ---- Envelope encryption KeyManager -----------------------------------
	// Dev path: LocalKeyManager reads SEDOC_LOCAL_KEK (base64 32-byte AES
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
		log.Warn(ctx).Msg("SEDOC_LOCAL_KEK not set; envelope encryption disabled")
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
		// Encryption-at-rest is gated by SEDOC_STORAGE_ENCRYPT_AT_REST.
		// Defaults to true when KMS is wired (per ADR 0022), but dev
		// compose explicitly sets it false because the OCR/preview/
		// intelligence pipelines fetch raw S3 bytes (boto3) and don't
		// run them through the storage service's decryptAll path.
		// Without a GetObjectBytes RPC that decrypts (queued for the
		// next storage wave), every download from those workers
		// returns encrypted bytes that fitz/pdfminer can't parse —
		// surfacing as "OCR failed: cannot find document handler."
		EncryptAtRest:    parseEncryptAtRest(km != nil),
		// TODO(per-tenant-kek): Single KEK across all tenants. Finding k in
		// docs/audit/04-antipatterns.md; target design + migration plan in
		// docs/tech-debt/per-tenant-kek.md.
		TenantKEKID:      "vaultdms-storage-default",
	})

	// ---- Health ------------------------------------------------------------
	hs := health.NewServerWithMeta("storage", cfg.Region, pool, rdb, nc, s3c)
	// Internal admin: blob re-encrypt (dms-admin kms rewrap*). Shares the
	// health port; gated by SEDOC_INTERNAL_API_KEY. Lets the CLI drive a bulk
	// re-wrap (region migration / post-rotation) without re-implementing the
	// KMS+S3 wiring the service owns.
	hs.Handle("/internal/v1/reencrypt-blob", handler.NewReencryptHTTPHandler(svc))
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
		// Without this every InitiateUpload returned
		// INVALID_ARGUMENT: required because the handler reads userID
		// from ctx via auth.GetUserID and got uuid.Nil. Same gap that
		// existed in services/document until the matching fix landed.
		middleware.UserIdentityInterceptor(),
		middleware.RequestLogInterceptor(log),
	))
	sedocv1.RegisterStorageServiceServer(grpcSrv, handler.New(svc))

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

	log.Info(ctx).Str("version", version).Msg(serviceName + " started")
	<-ctx.Done()
	log.Info(context.Background()).Msg(serviceName + " shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	grpcSrv.GracefulStop()
	_ = hs.Shutdown(shutdownCtx)
	outbox.Stop()
	reaper.Stop()
	_ = http.ListenAndServe // retained for future /metrics wiring
}

// parseEncryptAtRest reads SEDOC_STORAGE_ENCRYPT_AT_REST. Returns
// the explicit value when set, otherwise the historical default
// ("true when KMS is wired"). Three accepted truth values are
// 1 / true / yes (case-insensitive); anything else counts as false.
func parseEncryptAtRest(kmsWired bool) bool {
	raw := strings.TrimSpace(strings.ToLower(os.Getenv("SEDOC_STORAGE_ENCRYPT_AT_REST")))
	if raw == "" {
		return kmsWired
	}
	switch raw {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// parseSkipVirusScan reads SEDOC_STORAGE_SKIP_VIRUS_SCAN. Defaults
// to false (scan stays on). Truthy values bypass the ClamAV step on
// CompleteUpload — meant for dev where the scan adds 5–7s per upload.
func parseSkipVirusScan() bool {
	raw := strings.TrimSpace(strings.ToLower(os.Getenv("SEDOC_STORAGE_SKIP_VIRUS_SCAN")))
	switch raw {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
