module github.com/vaultdms/vaultdms/services/acknowledgement

go 1.25.0

require (
	github.com/google/uuid v1.6.0
	github.com/jackc/pgx/v5 v5.5.5
	github.com/prometheus/client_golang v1.19.0
	github.com/redis/go-redis/v9 v9.5.1
	github.com/rs/zerolog v1.32.0
	github.com/stretchr/testify v1.9.0
	github.com/vaultdms/vaultdms/pkg v0.0.0
)

replace github.com/vaultdms/vaultdms/pkg => ../../pkg
