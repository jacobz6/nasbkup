package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/nas-backup/internal/models"
)

// ──────────────────────────────────────────────────────────────────────────────
// Directory handlers
// ──────────────────────────────────────────────────────────────────────────────

// handleListDirectories returns all backup directories.
func (r *Router) handleListDirectories(w http.ResponseWriter, req *http.Request) {
	dirs, err := r.db.ConfigRepo.ListDirectories()
	if err != nil {
		r.jsonError(w, fmt.Sprintf("list directories: %v", err), http.StatusInternalServerError)
		return
	}
	r.jsonResponse(w, dirs, http.StatusOK)
}

// handleAddDirectory adds a new backup directory.
func (r *Router) handleAddDirectory(w http.ResponseWriter, req *http.Request) {
	var dir models.BackupDirectory
	if err := json.NewDecoder(req.Body).Decode(&dir); err != nil {
		r.jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if dir.Path == "" {
		r.jsonError(w, "path is required", http.StatusBadRequest)
		return
	}

	id, err := r.db.ConfigRepo.AddDirectory(dir.Path, dir.Recursive, dir.Enabled, dir.Description)
	if err != nil {
		r.jsonError(w, fmt.Sprintf("add directory: %v", err), http.StatusInternalServerError)
		return
	}
	dir.ID = id
	r.jsonResponse(w, dir, http.StatusCreated)
}

// handleUpdateDirectory updates an existing backup directory.
func (r *Router) handleUpdateDirectory(w http.ResponseWriter, req *http.Request) {
	id, err := strconv.ParseInt(req.PathValue("id"), 10, 64)
	if err != nil {
		r.jsonError(w, "invalid directory ID", http.StatusBadRequest)
		return
	}

	var dir models.BackupDirectory
	if err := json.NewDecoder(req.Body).Decode(&dir); err != nil {
		r.jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if dir.Path == "" {
		r.jsonError(w, "path is required", http.StatusBadRequest)
		return
	}

	if err := r.db.ConfigRepo.UpdateDirectory(id, dir.Path, dir.Recursive, dir.Enabled, dir.Description); err != nil {
		r.jsonError(w, fmt.Sprintf("update directory: %v", err), http.StatusInternalServerError)
		return
	}
	dir.ID = id
	r.jsonResponse(w, dir, http.StatusOK)
}

// handleDeleteDirectory deletes a backup directory.
func (r *Router) handleDeleteDirectory(w http.ResponseWriter, req *http.Request) {
	id, err := strconv.ParseInt(req.PathValue("id"), 10, 64)
	if err != nil {
		r.jsonError(w, "invalid directory ID", http.StatusBadRequest)
		return
	}

	if err := r.db.ConfigRepo.DeleteDirectory(id); err != nil {
		r.jsonError(w, fmt.Sprintf("delete directory: %v", err), http.StatusNotFound)
		return
	}
	r.jsonResponse(w, map[string]string{"status": "deleted"}, http.StatusOK)
}

// ──────────────────────────────────────────────────────────────────────────────
// Exclusion handlers
// ──────────────────────────────────────────────────────────────────────────────

// handleListExclusions returns all exclusion rules.
func (r *Router) handleListExclusions(w http.ResponseWriter, req *http.Request) {
	rules, err := r.db.ConfigRepo.ListExclusionRules()
	if err != nil {
		r.jsonError(w, fmt.Sprintf("list exclusions: %v", err), http.StatusInternalServerError)
		return
	}
	r.jsonResponse(w, rules, http.StatusOK)
}

// handleAddExclusion adds a new exclusion rule.
func (r *Router) handleAddExclusion(w http.ResponseWriter, req *http.Request) {
	var rule models.ExclusionRule
	if err := json.NewDecoder(req.Body).Decode(&rule); err != nil {
		r.jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if rule.Pattern == "" {
		r.jsonError(w, "pattern is required", http.StatusBadRequest)
		return
	}
	if rule.RuleType == "" {
		rule.RuleType = "pattern"
	}

	id, err := r.db.ConfigRepo.AddExclusionRule(rule.Pattern, rule.RuleType, rule.Enabled)
	if err != nil {
		r.jsonError(w, fmt.Sprintf("add exclusion rule: %v", err), http.StatusInternalServerError)
		return
	}
	rule.ID = id
	r.jsonResponse(w, rule, http.StatusCreated)
}

// handleUpdateExclusion updates an existing exclusion rule.
func (r *Router) handleUpdateExclusion(w http.ResponseWriter, req *http.Request) {
	id, err := strconv.ParseInt(req.PathValue("id"), 10, 64)
	if err != nil {
		r.jsonError(w, "invalid exclusion ID", http.StatusBadRequest)
		return
	}

	var rule models.ExclusionRule
	if err := json.NewDecoder(req.Body).Decode(&rule); err != nil {
		r.jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if rule.Pattern == "" {
		r.jsonError(w, "pattern is required", http.StatusBadRequest)
		return
	}

	if err := r.db.ConfigRepo.UpdateExclusionRule(id, rule.Pattern, rule.RuleType, rule.Enabled); err != nil {
		r.jsonError(w, fmt.Sprintf("update exclusion rule: %v", err), http.StatusInternalServerError)
		return
	}
	rule.ID = id
	r.jsonResponse(w, rule, http.StatusOK)
}

// handleDeleteExclusion deletes an exclusion rule.
func (r *Router) handleDeleteExclusion(w http.ResponseWriter, req *http.Request) {
	id, err := strconv.ParseInt(req.PathValue("id"), 10, 64)
	if err != nil {
		r.jsonError(w, "invalid exclusion ID", http.StatusBadRequest)
		return
	}

	if err := r.db.ConfigRepo.DeleteExclusionRule(id); err != nil {
		r.jsonError(w, fmt.Sprintf("delete exclusion rule: %v", err), http.StatusNotFound)
		return
	}
	r.jsonResponse(w, map[string]string{"status": "deleted"}, http.StatusOK)
}
