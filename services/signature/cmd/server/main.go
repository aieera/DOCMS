// Package main boots the VaultDMS signature service.
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

	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"

	"github.com/vaultdms/vaultdms/pkg/config"
	"github.com/vaultdms/vaultdms/pkg/crypto"
	"github.com/vaultdms/vaultdms/pkg/database"
	"github.com/vaultdms/vaultdms/pkg/events"
	"github.com/vaultdms/vaultdms/pkg/health"
	"github.com/vaultdms/vaultdms/pkg/logger"
	"github.com/vaultdms/vaultdms/pkg/middleware"
	"github.com/vaultdms/vaultdms/pkg/storage"
	"github.com/vaultdms/vaultdms/services/signature/internal/handler"
	"github.com/vaultdms/vaultdms/services/signature/internal/repository"
	"github.com/vaultdms/vaultdms/services/signature/internal/service"
)

const serviceName = "signature"

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

	var s3c *storage.S3Client
	if cfg.S3Endpoint != "" {
		s3c, err = storage.NewS3Client(cfg.S3Endpoint, cfg.S3AccessKey, cfg.S3SecretKey, cfg.S3UseSSL)
		if err != nil {
			log.Fatal(ctx).Err(err).Msg("s3 connect")
		}
	}

	repo := repository.New(pool)
	svc := service.New(service.Config{
		Pool:   pool,
		Repo:   repo,
		Outbox: database.NewOutboxRepository(),
		S3:     s3c,
		Logger: *log.Z(),
	})

	hs := health.NewServer(pool, rdb, nc, s3c)
	go func() {
		if err := hs.Start(fmt.Sprintf(":%d", cfg.HealthPort)); err != nil {
			log.Error(ctx).Err(err).Msg("health server")
		}
	}()

	grpcSrv := grpc.NewServer(grpc.ChainUnaryInterceptor(
		middleware.RecoveryInterceptor(log),
		middleware.CorrelationInterceptor(),
		middleware.TenantInterceptor(pool),
		middleware.UserIdentityInterceptor(),
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

	mux := http.NewServeMux()
	h := handler.New(svc, *log.Z())
	h.Register(mux)

	// Wave 15.4 saved-signature profiles — separate chi router under
	// SessionAuth + TenantHTTP so handlers can pull auth.User(ctx).
	// KMS wired from the LocalKEK for dev; swap Vault/AWS/PKCS#11 in
	// prod via config. S3 client from above.
	var profileRouter http.Handler
	if s3c != nil && cfg.LocalKEK != "" {
		km, kerr := crypto.NewLocalKeyManager(cfg.LocalKEK, nil)
		if kerr != nil {
			log.Fatal(ctx).Err(kerr).Msg("kms init")
		}
		profileSvc := service.NewProfileService(service.ProfileServiceConfig{
			Pool:  pool,
			Repo:  repository.NewProfileRepo(),
			KMS:   km,
			Store: service.NewProfileS3Adapter(s3c),
		})
		pr := chi.NewRouter()
		pr.Use(middleware.TenantHTTP(pool))
		pr.Use(middleware.SessionAuth(middleware.SessionAuthConfig{Pool: pool}))
		profileMux := http.NewServeMux()
		handler.NewProfileHandler(profileSvc).Register(profileMux)
		pr.Mount("/", profileMux)
		profileRouter = pr
	} else {
		log.Warn(ctx).Msg("signature profiles disabled: S3 or LocalKEK missing")
	}

	// Root mux: signature-profile routes go through the chi sub-router
	// with session auth; everything else keeps the legacy path.
	rootMux := http.NewServeMux()
	if profileRouter != nil {
		rootMux.Handle("/api/v1/signatures/profiles", profileRouter)
		rootMux.Handle("/api/v1/signatures/profiles/", profileRouter)
	}
	rootMux.Handle("/", mux)

	httpSrv := &http.Server{Addr: fmt.Sprintf(":%d", cfg.HTTPPort), Handler: middleware.RequireGatewaySignature()(rootMux), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		log.Info(ctx).Int("port", cfg.HTTPPort).Msg("http listening")
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error(ctx).Err(err).Msg("http serve")
		}
	}()

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
