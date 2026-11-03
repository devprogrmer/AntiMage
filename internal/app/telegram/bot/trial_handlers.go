package bot

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/antimage/antimage/internal/app/shop"
)

func (b *Bot) handleTrialClaim(ctx context.Context, settings Settings, chatID, telegramID int64) {
	if b.trials == nil {
		b.reply(ctx, settings, chatID, "Trial accounts are unavailable.", nil)
		return
	}
	username, err := b.trials.ClaimAndProvision(ctx, telegramID)
	if errors.Is(err, shop.ErrTrialUnavailable) {
		b.reply(ctx, settings, chatID, "Trial accounts are not currently offered.", nil)
		return
	}
	if err != nil {
		b.logf("telegram trial delivery for %d: %v", telegramID, err)
		b.reply(ctx, settings, chatID, "Your trial request is saved. Delivery is pending; send /trial again later or use /my to check your service.", nil)
		return
	}
	b.reply(ctx, settings, chatID, "✅ Trial service <code>"+escape(username)+"</code> is ready. Use /my for the subscription link.", nil)
}

func (b *Bot) handleAdminTrial(ctx context.Context, settings Settings, msg *Message, command, arg string) {
	if b.trials == nil || msg.From == nil || msg.Chat.Type != "private" || msg.Chat.ID != msg.From.ID || !b.adminAuthorized(ctx, settings, msg.From.ID) {
		return
	}
	if command == "/trial-off" {
		if err := b.trials.Disable(ctx); err != nil {
			b.reply(ctx, settings, msg.Chat.ID, "Could not disable trials: "+escape(err.Error()), nil)
			return
		}
		b.reply(ctx, settings, msg.Chat.ID, "New trial claims are disabled. Existing claims remain recoverable.", nil)
		return
	}
	parts := strings.Fields(arg)
	if len(parts) != 3 {
		b.reply(ctx, settings, msg.Chat.ID, "Usage: /trial-config &lt;service_id&gt; &lt;GB&gt; &lt;days&gt;", nil)
		return
	}
	serviceID, e1 := strconv.ParseInt(parts[0], 10, 64)
	gb, e2 := strconv.ParseInt(parts[1], 10, 64)
	days, e3 := strconv.ParseInt(parts[2], 10, 64)
	if e1 != nil || e2 != nil || e3 != nil || serviceID <= 0 || gb <= 0 || gb > math.MaxInt64/(1<<30) || days <= 0 || days > math.MaxInt64/86400 {
		b.reply(ctx, settings, msg.Chat.ID, "Invalid service, GB, or duration.", nil)
		return
	}
	if err := b.trials.Configure(ctx, shop.TrialConfig{ServiceID: serviceID, DataLimitBytes: gb * (1 << 30), DurationDays: days}); err != nil {
		b.reply(ctx, settings, msg.Chat.ID, "Could not configure trials: "+escape(err.Error()), nil)
		return
	}
	b.reply(ctx, settings, msg.Chat.ID, fmt.Sprintf("One trial per Telegram account enabled: service #%d, %d GB, %d days.", serviceID, gb, days), nil)
}
