package bot

import (
	"context"
	"fmt"
	"html"
	"strings"
	"time"
)

var statusEmoji = map[string]string{
	"active":   "✅",
	"expired":  "🕰",
	"limited":  "🪫",
	"disabled": "❌",
	"on_hold":  "🔌",
}

func escape(value string) string {
	return html.EscapeString(value)
}

func line(label, value string) string {
	return fmt.Sprintf("<b>%s:</b> <code>%s</code>", escape(label), escape(value))
}

func formatBytes(value int64) string {
	if value <= 0 {
		return "0 B"
	}
	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	size := float64(value)
	unit := 0
	for size >= 1024 && unit < len(units)-1 {
		size /= 1024
		unit++
	}
	if unit == 0 {
		return fmt.Sprintf("%d B", value)
	}
	return fmt.Sprintf("%.2f %s", size, units[unit])
}

func formatOptionalBytes(value *int64) string {
	if value == nil || *value <= 0 {
		return "Unlimited"
	}
	return formatBytes(*value)
}

func formatExpire(value *int64) string {
	if value == nil || *value <= 0 {
		return "Never"
	}
	return time.Unix(*value, 0).UTC().Format("2006-01-02")
}

// relativeFromServer renders a naive/zoned server timestamp as a coarse relative
// string, mirroring the legacy bot's "about N ... ago" formatting.
func relativeFromServer(value *string) string {
	if value == nil || strings.TrimSpace(*value) == "" {
		return "-"
	}
	parsed, ok := parseServerTime(*value)
	if !ok {
		return "-"
	}
	now := time.Now().UTC()
	if parsed.After(now) {
		return "in " + coarseDuration(parsed.Sub(now))
	}
	return coarseDuration(now.Sub(parsed)) + " ago"
}

func coarseDuration(d time.Duration) string {
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("about %d days", int(d.Hours()/24))
	case d >= time.Hour:
		return fmt.Sprintf("about %d hours", int(d.Hours()))
	case d >= time.Minute:
		return fmt.Sprintf("about %d minutes", int(d.Minutes()))
	default:
		return "just now"
	}
}

func parseServerTime(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC(), true
		}
	}
	return time.Time{}, false
}

func statusLine(status string) string {
	emoji := statusEmoji[strings.ToLower(strings.TrimSpace(status))]
	if emoji == "" {
		emoji = "•"
	}
	return fmt.Sprintf("%s <code>%s</code>", emoji, escape(status))
}

func localStatusLine(ctx context.Context, status string) string {
	label := status
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "active":
		label = localText(ctx, status, "فعال", "Активен")
	case "expired":
		label = localText(ctx, status, "منقضی", "Истёк")
	case "limited":
		label = localText(ctx, status, "حجم تمام‌شده", "Лимит исчерпан")
	case "disabled":
		label = localText(ctx, status, "غیرفعال", "Отключён")
	case "on_hold":
		label = localText(ctx, status, "در انتظار", "Приостановлен")
	}
	emoji := statusEmoji[strings.ToLower(strings.TrimSpace(status))]
	if emoji == "" {
		emoji = "•"
	}
	return fmt.Sprintf("%s <code>%s</code>", emoji, escape(label))
}

// userDetailText renders the admin-facing user detail message.
func userDetailText(ctx context.Context, user UserView) string {
	dataLeft := "-"
	if user.DataLimit != nil && *user.DataLimit > 0 {
		left := *user.DataLimit - user.UsedTraffic
		if left < 0 {
			left = 0
		}
		dataLeft = formatBytes(left)
	}
	lines := []string{
		"👤 " + localStatusLine(ctx, user.Status),
		separator(),
		line(localText(ctx, "Username", "نام کاربری", "Пользователь"), user.Username),
		line(localText(ctx, "Data limit", "سقف حجم", "Лимит трафика"), localOptionalBytes(ctx, user.DataLimit)),
		line(localText(ctx, "Data used", "حجم مصرف‌شده", "Использовано"), formatBytes(user.UsedTraffic)),
		line(localText(ctx, "Data left", "حجم باقیمانده", "Остаток"), dataLeft),
		line(localText(ctx, "Expires", "انقضا", "Истекает"), localExpire(ctx, user.Expire)),
		line(localText(ctx, "Online", "آخرین حضور", "Был в сети"), localRelative(ctx, user.OnlineAt)),
		line(localText(ctx, "Subscription updated", "آخرین به‌روزرسانی اشتراک", "Подписка обновлена"), localRelative(ctx, user.SubUpdatedAt)),
	}
	if strings.TrimSpace(user.Note) != "" {
		lines = append(lines, line(localText(ctx, "Note", "یادداشت", "Заметка"), user.Note))
	}
	if strings.TrimSpace(user.OwnerAdmin) != "" {
		lines = append(lines, line(localText(ctx, "Belongs to", "مدیر مالک", "Администратор"), user.OwnerAdmin))
	}
	if strings.TrimSpace(user.SubscriptionURL) != "" {
		lines = append(lines, separator(), line(localText(ctx, "Subscription", "اشتراک", "Подписка"), user.SubscriptionURL))
	}
	return strings.Join(lines, "\n")
}

// userUsageText renders the public /usage response.
func userUsageText(ctx context.Context, user UserView) string {
	daysLeft := "-"
	if user.Expire != nil && *user.Expire > 0 {
		remaining := time.Until(time.Unix(*user.Expire, 0))
		if remaining < 0 {
			daysLeft = "0"
		} else {
			daysLeft = fmt.Sprintf("%d", int(remaining.Hours()/24))
		}
	}
	return strings.Join([]string{
		"📊 " + localStatusLine(ctx, user.Status),
		separator(),
		line(localText(ctx, "Username", "نام کاربری", "Пользователь"), user.Username),
		line(localText(ctx, "Data limit", "سقف حجم", "Лимит трафика"), localOptionalBytes(ctx, user.DataLimit)),
		line(localText(ctx, "Data used", "حجم مصرف‌شده", "Использовано"), formatBytes(user.UsedTraffic)),
		line(localText(ctx, "Expires", "انقضا", "Истекает"), localExpire(ctx, user.Expire)),
		line(localText(ctx, "Days left", "روزهای باقیمانده", "Осталось дней"), daysLeft),
	}, "\n")
}

func systemInfoText(ctx context.Context, info SystemInfo) string {
	return strings.Join([]string{
		localText(ctx, "🖥 <b>System</b>", "🖥 <b>سیستم</b>", "🖥 <b>Система</b>"),
		separator(),
		line(localText(ctx, "Version", "نسخه", "Версия"), firstNonEmpty(info.Version, "-")),
		line("CPU", fmt.Sprintf("%.1f%%", info.CPUPercent)),
		line(localText(ctx, "Memory", "حافظه", "Память"), fmt.Sprintf("%s / %s", formatBytes(info.MemUsed), formatBytes(info.MemTotal))),
		separator(),
		line(localText(ctx, "Total users", "کل کاربران", "Всего пользователей"), fmt.Sprintf("%d", info.TotalUsers)),
		line(localText(ctx, "Active users", "کاربران فعال", "Активные пользователи"), fmt.Sprintf("%d", info.ActiveUsers)),
		line(localText(ctx, "Online users", "کاربران آنلاین", "Пользователи онлайн"), fmt.Sprintf("%d", info.OnlineUsers)),
	}, "\n")
}

func localOptionalBytes(ctx context.Context, value *int64) string {
	if value == nil || *value <= 0 {
		return localText(ctx, "Unlimited", "نامحدود", "Без ограничений")
	}
	return formatBytes(*value)
}

func localExpire(ctx context.Context, value *int64) string {
	if value == nil || *value <= 0 {
		return localText(ctx, "Never", "بدون انقضا", "Без срока")
	}
	return formatExpire(value)
}

func localRelative(ctx context.Context, value *string) string {
	if languageFromContext(ctx) == "en" {
		return relativeFromServer(value)
	}
	if value == nil {
		return "-"
	}
	parsed, ok := parseServerTime(*value)
	if !ok {
		return "-"
	}
	ago := time.Since(parsed)
	if ago < 0 {
		ago = -ago
	}
	var fa, ru string
	switch {
	case ago >= 24*time.Hour:
		fa, ru = fmt.Sprintf("حدود %d روز", int(ago.Hours()/24)), fmt.Sprintf("около %d дн.", int(ago.Hours()/24))
	case ago >= time.Hour:
		fa, ru = fmt.Sprintf("حدود %d ساعت", int(ago.Hours())), fmt.Sprintf("около %d ч.", int(ago.Hours()))
	case ago >= time.Minute:
		fa, ru = fmt.Sprintf("حدود %d دقیقه", int(ago.Minutes())), fmt.Sprintf("около %d мин.", int(ago.Minutes()))
	default:
		fa, ru = "همین حالا", "только что"
	}
	if ago < time.Second {
		return localText(ctx, "just now", fa, ru)
	}
	if parsed.After(time.Now()) {
		return localText(ctx, "in ", "تا ", "через ") + localText(ctx, "", fa, ru)
	}
	return localText(ctx, "", fa+" پیش", ru+" назад")
}

func separator() string {
	return "━━━━━━━━━━━━"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
