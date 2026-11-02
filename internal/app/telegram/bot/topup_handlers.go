package bot

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

func (b *Bot) handleTopupCommand(ctx context.Context, settings Settings, msg *Message, command, arg string) {
	if b.topups == nil || msg.From == nil || msg.From.ID <= 0 || msg.From.ID != msg.Chat.ID || msg.Chat.Type != "private" {
		return
	}
	chatID := msg.Chat.ID
	isAdmin := authorized(settings, chatID)
	switch command {
	case "/topup":
		amount, err := strconv.ParseInt(strings.TrimSpace(arg), 10, 64)
		if err != nil || amount < 1000 || amount > 100000000 || msg.MessageID <= 0 {
			b.reply(ctx, settings, chatID, "Usage: /topup &lt;toman&gt; (1,000 to 100,000,000)", nil)
			return
		}
		instructions, err := b.topups.Instructions(ctx)
		if err != nil || strings.TrimSpace(instructions) == "" {
			b.reply(ctx, settings, chatID, "Manual wallet topups are not configured yet.", nil)
			return
		}
		request, err := b.topups.Create(ctx, chatID, amount, fmt.Sprintf("tg-topup:%d:%d", chatID, msg.MessageID))
		if err != nil {
			b.reply(ctx, settings, chatID, "Could not create a topup request. Please try again.", nil)
			return
		}
		b.reply(ctx, settings, chatID, fmt.Sprintf("💳 Topup <code>%s</code> for <b>%d toman</b>.\n%s\nAfter transferring, send the receipt photo with caption <code>/receipt %s</code>. Your wallet is credited only after admin approval.", escape(request.ID), request.AmountToman, escape(instructions), escape(request.ID)), nil)
	case "/receipt":
		if len(msg.Photo) == 0 {
			b.reply(ctx, settings, chatID, "Send the bank receipt as a photo with caption /receipt &lt;topup_id&gt;.", nil)
			return
		}
		fileID := msg.Photo[len(msg.Photo)-1].FileID
		request, err := b.topups.SubmitReceipt(ctx, chatID, strings.TrimSpace(arg), fileID)
		if err != nil {
			b.reply(ctx, settings, chatID, "This topup request cannot accept a receipt. Check /topups.", nil)
			return
		}
		b.reply(ctx, settings, chatID, "Receipt saved for <code>"+escape(request.ID)+"</code>. Awaiting admin review.", nil)
		keyboard := &InlineKeyboard{InlineKeyboard: [][]InlineButton{{
			{Text: "Review approval", CallbackData: "topup:approve:" + request.ID},
			{Text: "Review rejection", CallbackData: "topup:reject:" + request.ID},
		}}}
		for _, adminID := range settings.AdminChatIDs {
			if adminID <= 0 || adminID == chatID {
				continue
			}
			caption := fmt.Sprintf("Receipt for topup <code>%s</code>\nCustomer: <code>%d</code>\nAmount: <b>%d toman</b>. Verify the transfer before approving.", escape(request.ID), chatID, request.AmountToman)
			if err := b.client.sendPhoto(ctx, settings, adminID, fileID, caption, keyboard); err != nil {
				b.logf("telegram bot: send receipt to admin %d: %v", adminID, err)
			}
		}
	case "/topups", "/pending-topups":
		if command == "/pending-topups" && !isAdmin {
			return
		}
		items, err := b.topups.List(ctx, chatID, command == "/pending-topups")
		if err != nil {
			b.reply(ctx, settings, chatID, "Could not load wallet topups.", nil)
			return
		}
		if len(items) == 0 {
			b.reply(ctx, settings, chatID, "No topup requests found.", nil)
			return
		}
		lines := []string{"<b>Wallet topups</b>"}
		for _, item := range items {
			line := fmt.Sprintf("<code>%s</code> · %d toman · %s", escape(item.ID), item.AmountToman, escape(item.Status))
			if command == "/pending-topups" {
				line += fmt.Sprintf(" · %d", item.TelegramID)
				line += " · /topup-review " + escape(item.ID)
			}
			lines = append(lines, line)
		}
		b.reply(ctx, settings, chatID, strings.Join(lines, "\n"), nil)
	case "/topup-review":
		if !isAdmin {
			return
		}
		request, err := b.topups.Get(ctx, strings.TrimSpace(arg))
		if err != nil || request.ReceiptFileID == nil || request.Status != "submitted" {
			b.reply(ctx, settings, chatID, "No submitted receipt found for that request.", nil)
			return
		}
		keyboard := &InlineKeyboard{InlineKeyboard: [][]InlineButton{{
			{Text: "Review approval", CallbackData: "topup:approve:" + request.ID},
			{Text: "Review rejection", CallbackData: "topup:reject:" + request.ID},
		}}}
		caption := fmt.Sprintf("Receipt for <code>%s</code>\nCustomer: <code>%d</code>\nAmount: <b>%d toman</b>. Verify the transfer before approving.", escape(request.ID), request.TelegramID, request.AmountToman)
		if err := b.client.sendPhoto(ctx, settings, chatID, *request.ReceiptFileID, caption, keyboard); err != nil {
			b.logf("telegram bot: review receipt %s: %v", request.ID, err)
			b.reply(ctx, settings, chatID, "Could not load the receipt photo.", nil)
		}
	case "/payment-details":
		if !isAdmin {
			return
		}
		if strings.TrimSpace(arg) == "" {
			b.reply(ctx, settings, chatID, "Usage: /payment-details &lt;bank transfer instructions&gt;", nil)
			return
		}
		if err := b.topups.SetInstructions(ctx, arg); err != nil {
			b.reply(ctx, settings, chatID, "Could not save payment instructions.", nil)
			return
		}
		b.reply(ctx, settings, chatID, "Manual payment instructions saved.", nil)
	}
}

func (b *Bot) handleTopupCallback(ctx context.Context, settings Settings, query *CallbackQuery) {
	if query.From == nil || query.Message == nil || query.From.ID <= 0 || query.Message.Chat.Type != "private" || query.Message.Chat.ID != query.From.ID || !authorized(settings, query.From.ID) || b.topups == nil {
		_ = b.client.answerCallbackQuery(ctx, settings, query.ID, "Not authorized")
		return
	}
	parts := strings.SplitN(query.Data, ":", 3)
	if len(parts) != 3 || len(parts[2]) != 36 {
		_ = b.client.answerCallbackQuery(ctx, settings, query.ID, "Invalid topup")
		return
	}
	if parts[1] == "approve" || parts[1] == "reject" {
		topup, err := b.topups.Get(ctx, parts[2])
		if err != nil || topup.Status != "submitted" {
			_ = b.client.answerCallbackQuery(ctx, settings, query.ID, "Topup is not awaiting review")
			return
		}
		_ = b.client.answerCallbackQuery(ctx, settings, query.ID, "Confirm this review action")
		keyboard := &InlineKeyboard{InlineKeyboard: [][]InlineButton{{{Text: "Confirm " + parts[1], CallbackData: "topup:" + parts[1] + "-confirm:" + parts[2]}}}}
		b.reply(ctx, settings, query.From.ID, fmt.Sprintf("Confirm %s for topup <code>%s</code>: <b>%d toman</b> to Telegram ID <code>%d</code>.", escape(parts[1]), escape(topup.ID), topup.AmountToman, topup.TelegramID), keyboard)
		return
	}
	var topupErr error
	var owner int64
	switch parts[1] {
	case "approve-confirm":
		topup, err := b.topups.Approve(ctx, parts[2], query.From.ID)
		topupErr = err
		owner = topup.TelegramID
	case "reject-confirm":
		topup, err := b.topups.Reject(ctx, parts[2], query.From.ID)
		topupErr = err
		owner = topup.TelegramID
	default:
		_ = b.client.answerCallbackQuery(ctx, settings, query.ID, "Invalid action")
		return
	}
	if topupErr != nil {
		_ = b.client.answerCallbackQuery(ctx, settings, query.ID, "Review failed")
		b.reply(ctx, settings, query.From.ID, "Topup review failed: "+escape(topupErr.Error()), nil)
		return
	}
	_ = b.client.answerCallbackQuery(ctx, settings, query.ID, "Review saved")
	if parts[1] == "approve-confirm" {
		b.reply(ctx, settings, query.From.ID, "Topup approved and wallet credited once.", nil)
		b.reply(ctx, settings, owner, "✅ Your wallet topup was approved. Use /balance to see the current balance.", nil)
	} else {
		b.reply(ctx, settings, query.From.ID, "Topup rejected.", nil)
		b.reply(ctx, settings, owner, "Your wallet topup receipt was rejected. Contact support with /support if you need help.", nil)
	}
}
