// Package api implements the HTTP API layer for the NAS backup system.
// It registers all routes, applies CORS middleware, and delegates request
// handling to the handler methods defined in handlers.go.
package api

import (
	"encoding/json"
	"net/http"

	"github.com/nas-backup/internal/backup"
	"github.com/nas-backup/internal/config"
	"github.com/nas-backup/internal/db"
	"github.com/nas-backup/internal/models"
	"github.com/nas-backup/internal/scheduler"
)

// Router registers HTTP routes and provides handler methods.
type Router struct {
	engine    *backup.Engine
	restorer  *backup.Restorer
	scheduler *scheduler.Scheduler
	db        *db.Database
	config    *config.AppConfig
	mux       *http.ServeMux
}

// NewRouter creates a new Router with all required dependencies.
func NewRouter(engine *backup.Engine, restorer *backup.Restorer,
	sched *scheduler.Scheduler, database *db.Database, cfg *config.AppConfig) *Router {
	return &Router{
		engine:    engine,
		restorer:  restorer,
		scheduler: sched,
		db:        database,
		config:    cfg,
		mux:       http.NewServeMux(),
	}
}

// Setup registers all routes and returns the HTTP handler with CORS middleware.
func (r *Router) Setup() http.Handler {
	// Dashboard
	r.mux.HandleFunc("GET /api/dashboard/stats", r.handleDashboardStats)
	r.mux.HandleFunc("GET /api/dashboard/history", r.handleDashboardHistory)

	// Backup operations
	r.mux.HandleFunc("POST /api/backup/trigger", r.handleBackupTrigger)
	r.mux.HandleFunc("POST /api/backup/cancel", r.handleBackupCancel)
	r.mux.HandleFunc("GET /api/backup/status", r.handleBackupStatus)

	// Content — File System Browse
	r.mux.HandleFunc("GET /api/fs/browse", r.handleFSBrowse)

	// Content — Directories
	r.mux.HandleFunc("GET /api/content/directories", r.handleListDirectories)
	r.mux.HandleFunc("POST /api/content/directories", r.handleAddDirectory)
	r.mux.HandleFunc("PUT /api/content/directories/{id}", r.handleUpdateDirectory)
	r.mux.HandleFunc("DELETE /api/content/directories/{id}", r.handleDeleteDirectory)

	// Content — Exclusions
	r.mux.HandleFunc("GET /api/content/exclusions", r.handleListExclusions)
	r.mux.HandleFunc("POST /api/content/exclusions", r.handleAddExclusion)
	r.mux.HandleFunc("PUT /api/content/exclusions/{id}", r.handleUpdateExclusion)
	r.mux.HandleFunc("DELETE /api/content/exclusions/{id}", r.handleDeleteExclusion)

	// Strategy — Schedule
	r.mux.HandleFunc("GET /api/strategy/schedule", r.handleGetSchedule)
	r.mux.HandleFunc("PUT /api/strategy/schedule", r.handleUpdateSchedule)

	// Strategy — Compression
	r.mux.HandleFunc("GET /api/strategy/compression", r.handleGetCompression)
	r.mux.HandleFunc("PUT /api/strategy/compression", r.handleUpdateCompression)

	// Strategy — Upload
	r.mux.HandleFunc("GET /api/strategy/upload", r.handleGetUpload)
	r.mux.HandleFunc("PUT /api/strategy/upload", r.handleUpdateUpload)

	// Strategy — Retention
	r.mux.HandleFunc("GET /api/strategy/retention", r.handleGetRetention)
	r.mux.HandleFunc("PUT /api/strategy/retention", r.handleUpdateRetention)

	// Strategy — Encryption
	r.mux.HandleFunc("GET /api/strategy/encryption", r.handleGetEncryption)
	r.mux.HandleFunc("PUT /api/strategy/encryption", r.handleUpdateEncryption)

	// Logs
	r.mux.HandleFunc("GET /api/logs", r.handleListLogs)
	r.mux.HandleFunc("GET /api/logs/{id}", r.handleGetLog)

	// Restore & Garbage Collection
	r.mux.HandleFunc("POST /api/restore", r.handleRestore)
	r.mux.HandleFunc("POST /api/gc", r.handleGarbageCollection)

	return r.corsMiddleware(r.mux)
}

// corsMiddleware adds CORS headers to all responses and handles preflight requests.
func (r *Router) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if req.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, req)
	})
}

// jsonResponse writes a success JSON response with the given status code.
func (r *Router) jsonResponse(w http.ResponseWriter, data interface{}, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(models.APIResponse{
		Success: true,
		Data:    data,
	})
}

// jsonPaginatedResponse writes a paginated JSON response.
func (r *Router) jsonPaginatedResponse(w http.ResponseWriter, data interface{}, total int64, page, size int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(models.PaginatedResponse{
		Success: true,
		Data:    data,
		Total:   total,
		Page:    page,
		Size:    size,
	})
}

// jsonError writes an error JSON response with the given status code.
func (r *Router) jsonError(w http.ResponseWriter, message string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(models.APIResponse{
		Success: false,
		Error:   message,
	})
}
