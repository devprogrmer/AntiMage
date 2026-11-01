package telegram

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

const maxUserNotificationRunes = 4096

type UserNotificationContext struct {
	Username         string
	Status           string
	UsedTraffic      string
	DataLimit        string
	RemainingTraffic string
	ExpireAt         string
	SubscriptionURL  string
}

var userNotificationPlaceholder = regexp.MustCompile(`\{\{\s*([a-z][a-z0-9_]*)\s*\}\}`)

// RenderUserNotificationTemplate expands the documented double-brace tokens.
// Unknown tokens are rejected so a typo can never be delivered literally.
func RenderUserNotificationTemplate(template string, user UserNotificationContext) (string, error) {
	values := map[string]string{
		"username":          user.Username,
		"status":            user.Status,
		"used_traffic":      user.UsedTraffic,
		"data_limit":        user.DataLimit,
		"remaining_traffic": user.RemainingTraffic,
		"expire_at":         user.ExpireAt,
		"subscription_url":  user.SubscriptionURL,
	}
	unknown := ""
	message := userNotificationPlaceholder.ReplaceAllStringFunc(template, func(token string) string {
		match := userNotificationPlaceholder.FindStringSubmatch(token)
		value, ok := values[match[1]]
		if !ok {
			unknown = match[1]
			return ""
		}
		return value
	})
	if unknown != "" {
		return "", fmt.Errorf("unknown user notification placeholder %q", unknown)
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return "", fmt.Errorf("user notification message is empty")
	}
	if utf8.RuneCountInString(message) > maxUserNotificationRunes {
		return "", fmt.Errorf("user notification message exceeds %d characters", maxUserNotificationRunes)
	}
	return message, nil
}
