module github.com/vaultdms/vaultdms/services/mcp-server

go 1.25.0

require (
	github.com/jackc/pgx/v5 v5.5.5
	github.com/nats-io/nats.go v1.49.0
	github.com/rs/zerolog v1.32.0
	github.com/vaultdms/vaultdms/pkg v0.0.0
)

replace (
	github.com/vaultdms/vaultdms/pkg => ../../pkg
	github.com/vaultdms/vaultdms/proto/gen/go => ../../proto/gen/go
)
