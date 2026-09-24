package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	userapp "github.com/antimage/antimage/internal/app/user"
	vpnuimigration "github.com/antimage/antimage/internal/app/vpnuimigration"
)

func (s *Server) handleVPNUIBackupImport(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/settings/backup/import/vpn-ui" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	principal, ok := r.Context().Value(adminContextKey).(adminPrincipal)
	if !ok {
		writeError(w, http.StatusUnauthorized, "missing admin context")
		return
	}
	uploadPath, cleanup, err := saveBackupUpload(w, r)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) || errors.Is(err, errBackupUploadTooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "vpn-ui backup upload is too large")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	defer cleanup()

	serviceID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("service_id")), 10, 64)
	if err != nil || serviceID <= 0 {
		writeError(w, http.StatusBadRequest, "service_id is required")
		return
	}
	if err := s.ensureServiceVisible(r.Context(), serviceID, principal.Context.Admin); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	policy := strings.ToLower(strings.TrimSpace(r.FormValue("duplicate_policy")))
	if policy == "" {
		policy = "skip"
	}
	if policy != "skip" && policy != "rename" {
		writeError(w, http.StatusBadRequest, "duplicate_policy must be skip or rename")
		return
	}

	analysis, err := vpnuimigration.Analyze(r.Context(), uploadPath)
	if err != nil {
		writeVPNUIImportError(w, err)
		return
	}
	result := vpnuimigration.Result{
		Detected: len(analysis.Accounts),
		Warnings: append([]string(nil), analysis.Warnings...),
	}
	for _, account := range analysis.Accounts {
		username := account.Username
		exists, err := s.userExistsForVPNUIImport(r.Context(), username)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if exists {
			if policy == "skip" {
				result.Skipped++
				continue
			}
			username = vpnUIRenamedUsername(username, account.SourceID)
			exists, err = s.userExistsForVPNUIImport(r.Context(), username)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			if exists {
				result.Skipped++
				continue
			}
			result.Renamed++
		}

		payload := userapp.UserServiceCreate{
			Username:    username,
			ServiceID:   serviceID,
			Status:      userapp.UserStatusCreateActive,
			Expire:      account.Expire,
			DataLimit:   account.DataLimit,
			IPLimit:     int64Pointer(account.IPLimit),
			DeviceLimit: int64Pointer(account.DeviceLimit),
		}
		if note := strings.TrimSpace(account.Note); note != "" {
			payload.Note = &note
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		created, err := s.userService.CreateUser(r.Context(), principal.Context.Admin, raw)
		if err != nil {
			result.Skipped++
			result.Warnings = append(result.Warnings, fmt.Sprintf("Skipped %s: %s", account.Username, err.Error()))
			continue
		}
		status := vpnUIImportedStatus(account, time.Now().Unix())
		now := time.Now().UTC()
		if _, err := s.db.ExecContext(r.Context(), `UPDATE users SET used_traffic = ?, status = ?, last_status_change = ? WHERE id = ?`, account.UsedBytes, status, now, created.UserID); err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("Imported %s but could not restore usage: %s", username, err.Error()))
		}
		if account.UsedBytes > 0 {
			_, _ = s.db.ExecContext(r.Context(), `UPDATE services SET used_traffic = used_traffic + ?, lifetime_used_traffic = lifetime_used_traffic + ?, updated_at = ? WHERE id = ?`, account.UsedBytes, account.UsedBytes, now, serviceID)
		}
		s.kickUserNodeOperationsSoon(created.UserID)
		result.Imported++
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) userExistsForVPNUIImport(ctx context.Context, username string) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE LOWER(username) = LOWER(?) AND status != 'deleted'`, username).Scan(&count)
	return count > 0, err
}

func vpnUIRenamedUsername(username string, sourceID int64) string {
	suffix := "-vpn" + strconv.FormatInt(sourceID, 10)
	maxBase := 34 - len(suffix)
	if maxBase < 1 {
		maxBase = 1
	}
	if len(username) > maxBase {
		username = username[:maxBase]
	}
	return username + suffix
}

func vpnUIImportedStatus(account vpnuimigration.Account, now int64) string {
	if !account.Enabled {
		return string(userapp.UserStatusDisabled)
	}
	if account.Expire != nil && *account.Expire > 0 && *account.Expire <= now {
		return string(userapp.UserStatusExpired)
	}
	if account.DataLimit != nil && *account.DataLimit > 0 && account.UsedBytes >= *account.DataLimit {
		return string(userapp.UserStatusLimited)
	}
	return string(userapp.UserStatusActive)
}

func int64Pointer(value int64) *int64 {
	if value <= 0 {
		return nil
	}
	return &value
}

func writeVPNUIImportError(w http.ResponseWriter, err error) {
	var migrationErr vpnuimigration.Error
	if errors.As(err, &migrationErr) {
		writeError(w, http.StatusBadRequest, migrationErr.Message)
		return
	}
	writeError(w, http.StatusInternalServerError, err.Error())
}
