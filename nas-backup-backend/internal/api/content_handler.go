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
// Supports partial updates (PATCH semantics): only fields provided in the
// request body are updated; missing fields retain their existing values.
func (r *Router) handleUpdateDirectory(w http.ResponseWriter, req *http.Request) {
	id, err := strconv.ParseInt(req.PathValue("id"), 10, 64)
	if err != nil {
		r.jsonError(w, "invalid directory ID", http.StatusBadRequest)
		return
	}

	var patch struct {
		Path        *string `json:"path"`
		Recursive   *bool   `json:"recursive"`
		Enabled     *bool   `json:"enabled"`
		Description *string `json:"description"`
	}
	if err := json.NewDecoder(req.Body).Decode(&patch); err != nil {
		r.jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	existing, err := r.db.ConfigRepo.GetDirectoryByID(id)
	if err != nil {
		r.jsonError(w, fmt.Sprintf("lookup directory: %v", err), http.StatusInternalServerError)
		return
	}
	if existing == nil {
		r.jsonError(w, "directory not found", http.StatusNotFound)
		return
	}

	path := existing.Path
	recursive := existing.Recursive
	enabled := existing.Enabled
	description := existing.Description
	if patch.Path != nil {
		path = *patch.Path
	}
	if patch.Recursive != nil {
		recursive = *patch.Recursive
	}
	if patch.Enabled != nil {
		enabled = *patch.Enabled
	}
	if patch.Description != nil {
		description = *patch.Description
	}
	if path == "" {
		r.jsonError(w, "path must not be empty", http.StatusBadRequest)
		return
	}

	if err := r.db.ConfigRepo.UpdateDirectory(id, path, recursive, enabled, description); err != nil {
		r.jsonError(w, fmt.Sprintf("update directory: %v", err), http.StatusInternalServerError)
		return
	}
	r.jsonResponse(w, &models.BackupDirectory{
		ID:          id,
		Path:        path,
		Recursive:   recursive,
		Enabled:     enabled,
		Description: description,
	}, http.StatusOK)
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
// Supports partial updates (PATCH semantics): only fields provided in the
// request body are updated; missing fields retain their existing values.
func (r *Router) handleUpdateExclusion(w http.ResponseWriter, req *http.Request) {
	id, err := strconv.ParseInt(req.PathValue("id"), 10, 64)
	if err != nil {
		r.jsonError(w, "invalid exclusion ID", http.StatusBadRequest)
		return
	}

	var patch struct {
		Pattern  *string `json:"pattern"`
		RuleType *string `json:"rule_type"`
		Enabled  *bool   `json:"enabled"`
	}
	if err := json.NewDecoder(req.Body).Decode(&patch); err != nil {
		r.jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	existing, err := r.db.ConfigRepo.GetExclusionRuleByID(id)
	if err != nil {
		r.jsonError(w, fmt.Sprintf("lookup exclusion rule: %v", err), http.StatusInternalServerError)
		return
	}
	if existing == nil {
		r.jsonError(w, "exclusion rule not found", http.StatusNotFound)
		return
	}

	pattern := existing.Pattern
	ruleType := existing.RuleType
	enabled := existing.Enabled
	if patch.Pattern != nil {
		pattern = *patch.Pattern
	}
	if patch.RuleType != nil {
		ruleType = *patch.RuleType
	}
	if patch.Enabled != nil {
		enabled = *patch.Enabled
	}
	if pattern == "" {
		r.jsonError(w, "pattern must not be empty", http.StatusBadRequest)
		return
	}

	if err := r.db.ConfigRepo.UpdateExclusionRule(id, pattern, ruleType, enabled); err != nil {
		r.jsonError(w, fmt.Sprintf("update exclusion rule: %v", err), http.StatusInternalServerError)
		return
	}
	r.jsonResponse(w, &models.ExclusionRule{
		ID:       id,
		Pattern:  pattern,
		RuleType: ruleType,
		Enabled:  enabled,
	}, http.StatusOK)
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
