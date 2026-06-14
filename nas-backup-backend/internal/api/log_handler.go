package api

import (
	"database/sql"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/nas-backup/internal/models"
)

// handleListLogs returns log records with filtering and pagination.
func (r *Router) handleListLogs(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query()

	filter := &models.LogFilter{
		Page:     1,
		PageSize: 50,
	}

	if v := q.Get("backup_id"); v != "" {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			filter.BackupID = &id
		}
	}
	if v := q.Get("level"); v != "" {
		level := models.LogLevel(v)
		filter.Level = &level
	}
	if v := q.Get("search"); v != "" {
		filter.Search = v
	}
	if v := q.Get("start_time"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			filter.StartTime = &t
		}
	}
	if v := q.Get("end_time"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			filter.EndTime = &t
		}
	}
	if v := q.Get("page"); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p > 0 {
			filter.Page = p
		}
	}
	if v := q.Get("page_size"); v != "" {
		if s, err := strconv.Atoi(v); err == nil && s > 0 {
			filter.PageSize = s
		}
	}

	records, total, err := r.db.LogRepo.List(filter)
	if err != nil {
		r.jsonError(w, fmt.Sprintf("list logs: %v", err), http.StatusInternalServerError)
		return
	}

	r.jsonPaginatedResponse(w, records, total, filter.Page, filter.PageSize)
}

// handleGetLog returns a single log entry by ID.
func (r *Router) handleGetLog(w http.ResponseWriter, req *http.Request) {
	id, err := strconv.ParseInt(req.PathValue("id"), 10, 64)
	if err != nil {
		r.jsonError(w, "invalid log ID", http.StatusBadRequest)
		return
	}

	// Fetch the log entry by ID using a direct query.
	// A proper GetByID method should be added to LogRepository;
	// this implementation queries the database directly as a workaround.
	var rec models.LogRecord
	var backupID sql.NullInt64
	var createdAt string

	row := r.db.DB().QueryRow(`
		SELECT id, backup_id, level, message, detail, created_at
		FROM backup_logs WHERE id = ?`, id)
	if err := row.Scan(&rec.ID, &backupID, &rec.Level, &rec.Message, &rec.Detail, &createdAt); err != nil {
		if err == sql.ErrNoRows {
			r.jsonError(w, "log entry not found", http.StatusNotFound)
		} else {
			r.jsonError(w, fmt.Sprintf("query log: %v", err), http.StatusInternalServerError)
		}
		return
	}

	if backupID.Valid {
		rec.BackupID = &backupID.Int64
	}
	t, parseErr := time.Parse(time.RFC3339, createdAt)
	if parseErr != nil {
		// Try alternative SQLite datetime formats before failing.
		altFormats := []string{
			"2006-01-02 15:04:05",
			"2006-01-02 15:04:05.999999999",
			"2006-01-02 15:04:05Z07:00",
			"2006-01-02T15:04:05",
		}
		parsed := false
		for _, f := range altFormats {
			if alt, altErr := time.Parse(f, createdAt); altErr == nil {
				t = alt
				parsed = true
				break
			}
		}
		if !parsed {
			// Fall back to current time instead of returning an error.
			t = time.Now().UTC()
		}
	}
	rec.CreatedAt = t

	r.jsonResponse(w, rec, http.StatusOK)
}
