// Package service holds the task service's business logic. Task 1 only
// scaffolds wiring; task-assignment business logic lands in a later task.
package service

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/database"
)

// TaskService is the placeholder service layer for the task service.
type TaskService struct {
	Pool   *pgxpool.Pool
	Outbox *database.OutboxRepository
	Log    zerolog.Logger
}

// New constructs a TaskService.
func New(pool *pgxpool.Pool, log zerolog.Logger) *TaskService {
	return &TaskService{
		Pool:   pool,
		Outbox: database.NewOutboxRepository(),
		Log:    log,
	}
}
