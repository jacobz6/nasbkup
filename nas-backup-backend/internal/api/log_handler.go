package api

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/nas-backup/internal/logger"
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

	logger.Debug("ListLogs: page=%d size=%d total=%d returned=%d", filter.Page, filter.PageSize, total, len(records))
	r.jsonPaginatedResponse(w, records, total, filter.Page, filter.PageSize)
}

// handleGetLog returns a single log entry by ID.
func (r *Router) handleGetLog(w http.ResponseWriter, req *http.Request) {
	id, err := strconv.ParseInt(req.PathValue("id"), 10, 64)
	if err != nil {
		r.jsonError(w, "invalid log ID", http.StatusBadRequest)
		return
	}

	rec, err := r.db.LogRepo.GetByID(id)
	if err != nil {
		r.jsonError(w, fmt.Sprintf("query log: %v", err), http.StatusInternalServerError)
		return
	}
	if rec == nil {
		r.jsonError(w, "log entry not found", http.StatusNotFound)
		return
	}

	r.jsonResponse(w, rec, http.StatusOK)
}
