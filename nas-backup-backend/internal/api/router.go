// Package api implements the HTTP API layer for the NAS backup system.
// It registers all routes, applies CORS middleware, and delegates request
// handling to the handler methods defined in domain-specific handler files.
package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nas-backup/internal/backup"
	"github.com/nas-backup/internal/config"
	"github.com/nas-backup/internal/db"
	"github.com/nas-backup/internal/logger"
	"github.com/nas-backup/internal/models"
	"github.com/nas-backup/internal/scheduler"
)

// Router registers HTTP routes and provides handler methods.
type Router struct {
	engine        *backup.Engine
	restorer      *backup.Restorer
	restoreJobMgr *backup.RestoreJobManager
	scheduler     *scheduler.Scheduler
	db            *db.Database
	config        *config.AppConfig
	mux           *http.ServeMux
}

// NewRouter creates a new Router with all required dependencies.
func NewRouter(engine *backup.Engine, restorer *backup.Restorer,
	restoreJobMgr *backup.RestoreJobManager,
	sched *scheduler.Scheduler, database *db.Database, cfg *config.AppConfig) *Router {
	return &Router{
		engine:        engine,
		restorer:      restorer,
		restoreJobMgr: restoreJobMgr,
		scheduler:     sched,
		db:            database,
		config:        cfg,
		mux:           http.NewServeMux(),
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
	r.mux.HandleFunc("GET /api/backup/progress/stream", r.handleBackupProgressStream)

	// Content — File System Browse
	r.mux.HandleFunc("GET /api/fs/browse", r.handleFSBrowse)

	// Content — Directories
	r.mux.HandleFunc("GET /api/content/directories", r.handleListDirectories)
	r.mux.HandleFunc("POST /api/content/directories", r.handleAddDirectory)
	r.mux.HandleFunc("PATCH /api/content/directories/{id}", r.handleUpdateDirectory)

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

	// Restore operations (async job-based)
	r.mux.HandleFunc("POST /api/restore", r.handleRestoreCreate)
	r.mux.HandleFunc("GET /api/restore/files", r.handleRestoreListFiles)
	r.mux.HandleFunc("GET /api/restore/progress/stream", r.handleRestoreProgressStream)
	r.mux.HandleFunc("GET /api/restore/jobs", r.handleRestoreListJobs)
	r.mux.HandleFunc("GET /api/restore/jobs/{id}", r.handleRestoreGetJob)
	r.mux.HandleFunc("POST /api/restore/jobs/{id}/cancel", r.handleRestoreCancelJob)

	// Backups list (for restore version selection)
	r.mux.HandleFunc("GET /api/backups", r.handleListBackups)

	r.mux.HandleFunc("POST /api/gc", r.handleGarbageCollection)
	// Reconcile (system sync): keep OSS / hash_index / backup_files consistent.
	r.mux.HandleFunc("POST /api/reconcile", r.handleReconcile)

	// Storage health — verify OSS connectivity / credentials.
	r.mux.HandleFunc("GET /api/storage/health", r.handleStorageHealth)

	return r.loggingMiddleware(r.corsMiddleware(r.mux))
}

// loggingMiddleware logs every HTTP request: method, path, status, and duration.
func (r *Router) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		start := time.Now()

		// Wrap the ResponseWriter to capture the status code.
		rw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, req)

		duration := time.Since(start)
		if rw.status >= 500 {
			logger.Error("HTTP %d %s %s (%v)", rw.status, req.Method, req.URL.Path, duration)
		} else if rw.status >= 400 {
			logger.Warn("HTTP %d %s %s (%v)", rw.status, req.Method, req.URL.Path, duration)
		} else {
			logger.Info("HTTP %d %s %s (%v)", rw.status, req.Method, req.URL.Path, duration)
		}
	})
}

// statusWriter wraps http.ResponseWriter to capture the response status code.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// corsMiddleware adds CORS headers to all responses and handles preflight requests.
func (r *Router) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Cache-Control")
		w.Header().Set("Access-Control-Expose-Headers", "Content-Type")

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

// jsonError writes an error JSON response with the given status code and logs it.
// 4xx client errors are logged as warnings; 5xx server errors as errors.
func (r *Router) jsonError(w http.ResponseWriter, message string, status int) {
	if status >= 500 {
		logger.Error("API error %d: %s", status, message)
	} else {
		logger.Warn("API client error %d: %s", status, message)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(models.APIResponse{
		Success: false,
		Error:   message,
	})
}

// parsePagination extracts page and size from query parameters with defaults.
func parsePagination(req *http.Request) (page, size int) {
	page, _ = strconv.Atoi(req.URL.Query().Get("page"))
	size, _ = strconv.Atoi(req.URL.Query().Get("size"))
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 20
	}
	if size > 200 {
		size = 200
	}
	return page, size
}

// parseStringSlice parses a comma-separated string into a slice.
func parseStringSlice(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

// formatStringSlice formats a string slice as a comma-separated string.
func formatStringSlice(parts []string) string {
	return strings.Join(parts, ",")
}
