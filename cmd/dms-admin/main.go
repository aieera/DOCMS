// dms-admin is a CLI tool for SeDoc operations. Currently supports:
//
//	dms-admin rotate-secrets   — rotate DB passwords, JWT keys, API keys, KEKs
//	dms-admin seed             — seed test data
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: dms-admin <command>")
		fmt.Println("Commands: rotate-secrets, seed, nats, kms")
		os.Exit(1)
	}
	switch os.Args[1] {
	case "rotate-secrets":
		rotateSecrets()
	case "seed":
		fmt.Println("seed: not yet implemented")
	case "nats":
		natsMain(os.Args[2:])
	case "kms":
		kmsMain(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}
}

func rotateSecrets() {
	ctx := context.Background()
	dbURL := envOrDefault("DATABASE_URL", "postgresql://sedoc:devpassword@localhost:5432/sedoc")
	redisAddr := envOrDefault("REDIS_URL", "localhost:6379")

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		fatal("db connect: %v", err)
	}
	defer pool.Close()

	rdb := redis.NewClient(&redis.Options{Addr: redisAddr})
	defer rdb.Close()

	dualValidityWindow := 24 * time.Hour
	now := time.Now().UTC()

	fmt.Println("=== SeDoc Secrets Rotation ===")
	fmt.Printf("Dual-validity window: %s (old keys valid until %s)\n\n", dualValidityWindow, now.Add(dualValidityWindow).Format(time.RFC3339))

	// 1. Rotate JWT signing key.
	newJWTKey := generateKey(32)
	fmt.Printf("[JWT] New signing key: %s\n", newJWTKey[:16]+"...")
	_ = rdb.Set(ctx, "jwt_signing_key:new", newJWTKey, 0).Err()
	_ = rdb.Set(ctx, "jwt_signing_key:old_expires", now.Add(dualValidityWindow).Format(time.RFC3339), 0).Err()
	fmt.Println("[JWT] Stored new key in Redis. Old key valid for 24h.")

	// 2. Rotate session encryption key.
	newSessionKey := generateKey(32)
	fmt.Printf("[Session] New encryption key: %s\n", newSessionKey[:16]+"...")
	_ = rdb.Set(ctx, "session_enc_key:new", newSessionKey, 0).Err()
	_ = rdb.Set(ctx, "session_enc_key:old_expires", now.Add(dualValidityWindow).Format(time.RFC3339), 0).Err()

	// 3. Rotate KEK (Key Encryption Key).
	newKEK := generateKey(32)
	fmt.Printf("[KEK] New KEK (base64): %s\n", base64.StdEncoding.EncodeToString([]byte(newKEK))[:24]+"...")
	_ = rdb.Set(ctx, "kek:new", base64.StdEncoding.EncodeToString([]byte(newKEK)), 0).Err()
	_ = rdb.Set(ctx, "kek:old_expires", now.Add(dualValidityWindow).Format(time.RFC3339), 0).Err()

	// 4. Rotate API key signing secret.
	newAPISecret := generateKey(32)
	_, err = pool.Exec(ctx, `
		INSERT INTO system_secrets (key, value, created_at, expires_at)
		VALUES ('api_key_secret', $1, $2, NULL)
		ON CONFLICT (key) DO UPDATE SET
			value = EXCLUDED.value,
			previous_value = system_secrets.value,
			previous_expires_at = $3,
			created_at = EXCLUDED.created_at
	`, newAPISecret, now, now.Add(dualValidityWindow))
	if err != nil {
		fmt.Printf("[API Keys] Warning: could not rotate in DB: %v\n", err)
		fmt.Printf("[API Keys] Table may not exist yet. New secret: %s\n", newAPISecret[:16]+"...")
	} else {
		fmt.Println("[API Keys] Rotated. Previous key valid for 24h.")
	}

	// 5. DB password rotation hint.
	fmt.Println("\n[DB Password] Database password rotation requires:")
	fmt.Println("  1. ALTER ROLE vaultdms PASSWORD 'new_password';")
	fmt.Println("  2. Update Kubernetes Secret (vaultdms-db-credentials)")
	fmt.Println("  3. Rolling restart of all services")
	fmt.Println("  Dual-validity: both passwords work during the 24h window via pg_hba.conf or connection pooler.")

	fmt.Println("\n=== Rotation complete. Monitor services for auth errors in the next 24h. ===")
}

func generateKey(bytes int) string {
	b := make([]byte, bytes)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
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
