// Package handler holds the task service's HTTP surface. Task 1 only
// scaffolds wiring; routes land in a later task.
package handler

import (
	"net/http"

	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/services/task/internal/service"
)

// Handler is the placeholder HTTP surface for the task service.
type Handler struct {
	log zerolog.Logger
}

// New constructs a Handler.
func New(svc *service.TaskService, log zerolog.Logger) *Handler {
	return &Handler{log: log}
}

// Register wires HTTP routes onto mux. No routes yet — placeholder.
func (h *Handler) Register(mux *http.ServeMux) {}
