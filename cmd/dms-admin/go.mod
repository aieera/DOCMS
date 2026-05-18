module github.com/vaultdms/vaultdms/cmd/dms-admin

go 1.25.0

require (
	github.com/jackc/pgx/v5 v5.9.2
	github.com/nats-io/nats.go v1.49.0
	github.com/redis/go-redis/v9 v9.19.0
	github.com/vaultdms/vaultdms/pkg v0.0.0
)

replace github.com/vaultdms/vaultdms/pkg => ../../pkg

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/klauspost/compress v1.18.5 // indirect
	github.com/nats-io/nkeys v0.4.15 // indirect
	github.com/nats-io/nuid v1.0.1 // indirect
	go.uber.org/atomic v1.11.0 // indirect
	golang.org/x/crypto v0.51.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/sys v0.44.0 // indirect
	golang.org/x/text v0.37.0 // indirect
)

replace github.com/docker/docker => github.com/moby/moby v26.1.5+incompatible
