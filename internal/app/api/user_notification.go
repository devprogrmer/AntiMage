package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	telegramapp "github.com/antimage/antimage/internal/app/telegram"
	userapp "github.com/antimage/antimage/internal/app/user"
)

type userNotificationRequest struct {
	Template string `json:"template"`
}

func (s *Server) handleUserNotification(w http.ResponseWriter, r *http.Request, username string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	principal, ok := r.Context().Value(adminContextKey).(adminPrincipal)
	if !ok {
		writeError(w, http.StatusUnauthorized, "missing admin context")
		return
	}
	var payload userNotificationRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&payload); err != nil {
		writeError(w, http.StatusBadRequest, "invalid notification request")
		return
	}
	user, err := s.userService.UserGet(r.Context(), userapp.UserGetRequest{
		Username:      username,
		RequestOrigin: requestOrigin(r),
		Admin:         s.userAdminContext(principal, nil),
	})
	if err != nil {
		writeUserReadError(w, err)
		return
	}
	if user.TelegramID == nil || strings.TrimSpace(*user.TelegramID) == "" {
		writeError(w, http.StatusBadRequest, "user has no Telegram chat ID")
		return
	}
	chatID, err := strconv.ParseInt(strings.TrimSpace(*user.TelegramID), 10, 64)
	if err != nil || chatID <= 0 {
		writeError(w, http.StatusBadRequest, "user Telegram chat ID must be a positive number")
		return
	}
	usedTraffic := user.UsedTraffic
	dataLimit := user.DataLimit
	trafficVisible := canViewUserTraffic(principal.Context.Admin, user.ServiceID)
	if !trafficVisible {
		usedTraffic = 0
		dataLimit = nil
	}
	used := "Hidden"
	limit := "Hidden"
	remaining := "Hidden"
	if trafficVisible {
		used = telegramapp.FormatOptionalBytes(&usedTraffic)
		limit = telegramapp.FormatOptionalBytes(dataLimit)
		remaining = "Unlimited"
	}
	if trafficVisible && dataLimit != nil && *dataLimit > 0 {
		left := *dataLimit - usedTraffic
		if left < 0 {
			left = 0
		}
		if left == 0 {
			remaining = "0 B"
		} else {
			remaining = telegramapp.FormatOptionalBytes(&left)
		}
	}
	expireAt := "Unlimited"
	if user.Expire != nil && *user.Expire > 0 {
		expireAt = time.Unix(*user.Expire, 0).UTC().Format("2006-01-02 15:04 UTC")
	}
	message, err := telegramapp.RenderUserNotificationTemplate(payload.Template, telegramapp.UserNotificationContext{
		Username:         user.Username,
		Status:           user.Status,
		UsedTraffic:      used,
		DataLimit:        limit,
		RemainingTraffic: remaining,
		ExpireAt:         expireAt,
		SubscriptionURL:  user.SubscriptionURL,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.telegramSender.SendDirectMessage(r.Context(), chatID, message); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "username": user.Username})
}
