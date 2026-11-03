package bot

import (
	"context"
	"fmt"
	"strings"
)

const helpText = `🤖 <b>AntiMage admin bot</b>

<b>Commands</b>
/user <code>&lt;username&gt;</code> — show a user and manage it
/usage <code>&lt;username&gt;</code> — show a user's usage
/system — show system status
/nodes — list nodes and their status
/node &lt;id&gt; — inspect one node with live metrics
/sales — show the shop's order and revenue summary
/shop — browse plans
/trial — claim your one-time trial, if enabled
/balance — show your wallet
/transactions — show your latest wallet transactions
/wallet &lt;telegram_id&gt; — admin wallet audit
/orders — show your orders
/my — show services linked to your Telegram ID
/credit &lt;telegram_id&gt; &lt;toman&gt; &lt;reference&gt; — admin wallet credit
/plan-add &lt;service_id&gt; &lt;toman&gt; &lt;GB&gt; &lt;days&gt; &lt;title&gt; — admin product
/plan-disable &lt;product_id&gt; — admin product disable
/trial-config &lt;service_id&gt; &lt;GB&gt; &lt;days&gt; — admin trial offer
/trial-off — disable new trials
/support &lt;message&gt; — open a support ticket
/tickets — list tickets
/ticket &lt;id&gt; — read a ticket
/reply &lt;id&gt; &lt;message&gt; — admin reply
/topup &lt;toman&gt; — request wallet charge
/receipt &lt;topup_id&gt; — send receipt photo with this caption
/topups — view your charges
/pending-topups — admin review queue
/topup-review &lt;id&gt; — admin receipt view
/payment-details &lt;text&gt; — admin payment instructions
/help — show this help`

func (b *Bot) handleMessage(ctx context.Context, settings Settings, msg *Message) {
	chatID := msg.Chat.ID
	input := msg.Text
	if input == "" {
		input = msg.Caption
	}
	command, arg := splitCommand(input)
	if msg.From != nil && msg.From.ID > 0 && msg.Chat.Type == "private" && chatID == msg.From.ID {
		switch command {
		case "/my":
			b.handleCustomerAccounts(ctx, settings, chatID, msg.From.ID)
			return
		case "/shop", "/balance", "/orders", "/transactions":
			b.handleShopCommand(ctx, settings, chatID, msg.From.ID, command)
			return
		case "/trial":
			b.handleTrialClaim(ctx, settings, chatID, msg.From.ID)
			return
		case "/support", "/tickets", "/ticket", "/reply":
			b.handleSupportCommand(ctx, settings, msg, command, arg)
			return
		case "/topup", "/receipt", "/topups", "/pending-topups", "/topup-review", "/payment-details":
			b.handleTopupCommand(ctx, settings, msg, command, arg)
			return
		case "/start", "/help":
			if !b.adminAuthorized(ctx, settings, msg.From.ID) {
				b.reply(ctx, settings, chatID, "Welcome to AntiMage. Use /shop to view plans, /trial for a one-time trial, /topup to fund your wallet, /balance and /transactions for your wallet, /orders for purchases, /my for services, or /support for help.", customerMenuKeyboard())
				return
			}
		}
	}
	// Admin notification destinations may include groups. Membership in a
	// group must never grant its members command access to the panel.
	if msg.From == nil || msg.Chat.Type != "private" || chatID != msg.From.ID || !b.adminAuthorized(ctx, settings, msg.From.ID) {
		return
	}
	text := strings.TrimSpace(input)

	// Multi-step flows take priority over command parsing for non-command input.
	if !strings.HasPrefix(text, "/") {
		if conv, ok := b.state.get(ctx, chatID); ok {
			b.handleConversation(ctx, settings, chatID, conv, text)
			return
		}
	}

	command, arg = splitCommand(text)
	switch command {
	case "/start", "/help":
		b.reply(ctx, settings, chatID, helpText, mainMenuKeyboard())
	case "/system":
		b.reply(ctx, settings, chatID, b.systemText(ctx), nil)
	case "/nodes", "/node":
		b.handleAdminNodes(ctx, settings, chatID, command, arg)
	case "/sales":
		b.handleAdminSales(ctx, settings, chatID)
	case "/wallet":
		b.handleAdminWallet(ctx, settings, chatID, arg)
	case "/usage":
		b.handleUsageCommand(ctx, settings, chatID, arg)
	case "/user":
		b.handleUserCommand(ctx, settings, chatID, arg)
	case "/credit", "/plan-add", "/plan-disable":
		b.handleAdminShopCommand(ctx, settings, msg, command, arg)
	case "/trial-config", "/trial-off":
		b.handleAdminTrial(ctx, settings, msg, command, arg)
	default:
		if command != "" {
			b.reply(ctx, settings, chatID, "Unknown command. Send /help.", nil)
		}
	}
}

func (b *Bot) handleCustomerAccounts(ctx context.Context, settings Settings, chatID, telegramID int64) {
	if b.customers == nil {
		b.reply(ctx, settings, chatID, "Customer services are unavailable.", nil)
		return
	}
	accounts, err := b.customers.ListByTelegramID(ctx, telegramID)
	if err != nil {
		b.reply(ctx, settings, chatID, "Could not load your services. Please try again later.", nil)
		return
	}
	if len(accounts) == 0 {
		b.reply(ctx, settings, chatID, "No services are linked to your Telegram account. Ask your provider to set your Telegram ID on your AntiMage user.", nil)
		return
	}
	for _, account := range accounts {
		lines := []string{"👤 <b>Your service</b>", line("Username", account.Username), "<b>Status:</b> " + statusLine(account.Status), line("Used", formatBytes(account.UsedTraffic)), line("Limit", formatOptionalBytes(account.DataLimit)), line("Expires", formatExpire(account.Expire))}
		if strings.TrimSpace(account.SubscriptionURL) != "" {
			lines = append(lines, line("Subscription", account.SubscriptionURL))
		}
		b.reply(ctx, settings, chatID, strings.Join(lines, "\n"), nil)
	}
}

func (b *Bot) handleUsageCommand(ctx context.Context, settings Settings, chatID int64, username string) {
	username = strings.TrimSpace(username)
	if username == "" {
		b.reply(ctx, settings, chatID, "Usage: <code>/usage &lt;username&gt;</code>", nil)
		return
	}
	user, err := b.users.Get(ctx, username)
	if err != nil {
		b.reply(ctx, settings, chatID, notFoundText(username), nil)
		return
	}
	b.reply(ctx, settings, chatID, userUsageText(user), nil)
}

func (b *Bot) handleUserCommand(ctx context.Context, settings Settings, chatID int64, username string) {
	username = strings.TrimSpace(username)
	if username == "" {
		b.reply(ctx, settings, chatID, "Usage: <code>/user &lt;username&gt;</code>", nil)
		return
	}
	user, err := b.users.Get(ctx, username)
	if err != nil {
		b.reply(ctx, settings, chatID, notFoundText(username), nil)
		return
	}
	b.reply(ctx, settings, chatID, userDetailText(user), userMenuKeyboard(user.Username, user.Status))
}

func (b *Bot) handleConversation(ctx context.Context, settings Settings, chatID int64, conv conversation, text string) {
	switch conv.State {
	case stateAwaitNote:
		username := conv.Payload
		_ = b.state.clear(ctx, chatID)
		actor, ok := b.authorizer.Actor(ctx, chatID)
		if !ok {
			b.reply(ctx, settings, chatID, noActorText(), nil)
			return
		}
		if err := b.users.SetNote(ctx, actor, username, text); err != nil {
			b.reply(ctx, settings, chatID, actionErrorText("update note", err), nil)
			return
		}
		b.sendRefreshedUser(ctx, settings, chatID, 0, username, "Note updated")
	default:
		_ = b.state.clear(ctx, chatID)
	}
}

func (b *Bot) handleCallback(ctx context.Context, settings Settings, query *CallbackQuery) {
	if strings.HasPrefix(query.Data, "menu:") && query.From != nil && query.From.ID > 0 && query.Message != nil && query.Message.Chat.Type == "private" && query.Message.Chat.ID == query.From.ID {
		chatID := query.From.ID
		_ = b.client.answerCallbackQuery(ctx, settings, query.ID, "")
		switch query.Data {
		case "menu:nodes":
			if b.adminAuthorized(ctx, settings, chatID) {
				b.handleAdminNodes(ctx, settings, chatID, "/nodes", "")
			}
		case "menu:sales":
			if b.adminAuthorized(ctx, settings, chatID) {
				b.handleAdminSales(ctx, settings, chatID)
			}
		case "menu:trial":
			b.handleTrialClaim(ctx, settings, chatID, chatID)
		case "menu:shop", "menu:balance", "menu:transactions", "menu:orders":
			b.handleShopCommand(ctx, settings, chatID, chatID, "/"+strings.TrimPrefix(query.Data, "menu:"))
		case "menu:my":
			b.handleCustomerAccounts(ctx, settings, chatID, chatID)
		case "menu:tickets":
			b.handleSupportCommand(ctx, settings, &Message{From: query.From, Chat: query.Message.Chat}, "/tickets", "")
		case "menu:topups":
			b.handleTopupCommand(ctx, settings, &Message{From: query.From, Chat: query.Message.Chat}, "/topups", "")
		}
		return
	}
	if strings.HasPrefix(query.Data, "shop:") && query.From != nil && query.From.ID > 0 && query.Message != nil && query.Message.Chat.Type == "private" && query.Message.Chat.ID == query.From.ID {
		b.handleShopCallback(ctx, settings, query)
		return
	}
	if strings.HasPrefix(query.Data, "topup:") {
		b.handleTopupCallback(ctx, settings, query)
		return
	}
	userID := int64(0)
	if query.From != nil {
		userID = query.From.ID
	}
	if query.Message == nil || query.Message.Chat.Type != "private" || query.Message.Chat.ID != userID || !b.adminAuthorized(ctx, settings, userID) {
		_ = b.client.answerCallbackQuery(ctx, settings, query.ID, "Not authorized")
		return
	}
	var chatID, messageID int64
	if query.Message != nil {
		chatID = query.Message.Chat.ID
		messageID = query.Message.MessageID
	}

	prefix, value := parseCallback(query.Data)
	switch query.Data {
	case "system":
		_ = b.client.answerCallbackQuery(ctx, settings, query.ID, "")
		_ = b.client.editMessageText(ctx, settings, chatID, messageID, b.systemText(ctx), mainMenuKeyboard())
		return
	}

	switch prefix {
	case cbRefresh:
		_ = b.client.answerCallbackQuery(ctx, settings, query.ID, "")
		b.sendRefreshedUser(ctx, settings, chatID, messageID, value, "")
	case cbLinks:
		b.handleLinks(ctx, settings, query, chatID, value)
	case cbEditNote:
		_ = b.state.set(ctx, chatID, stateAwaitNote, value)
		_ = b.client.answerCallbackQuery(ctx, settings, query.ID, "")
		b.reply(ctx, settings, chatID, fmt.Sprintf("Send the new note for <code>%s</code>:", escape(value)), nil)
	case cbDelete:
		_ = b.client.answerCallbackQuery(ctx, settings, query.ID, "")
		_ = b.client.editMessageText(ctx, settings, chatID, messageID,
			fmt.Sprintf("⚠️ Delete user <code>%s</code>? This cannot be undone.", escape(value)),
			confirmDeleteKeyboard(value))
	case cbDeleteYes:
		b.runUserAction(ctx, settings, query, chatID, messageID, value, "delete")
	case cbActivate:
		b.runUserAction(ctx, settings, query, chatID, messageID, value, "activate")
	case cbSuspend:
		b.runUserAction(ctx, settings, query, chatID, messageID, value, "suspend")
	case cbResetUsage:
		b.runUserAction(ctx, settings, query, chatID, messageID, value, "reset")
	case cbRevokeSub:
		b.runUserAction(ctx, settings, query, chatID, messageID, value, "revoke")
	default:
		_ = b.client.answerCallbackQuery(ctx, settings, query.ID, "")
	}
}

func (b *Bot) handleLinks(ctx context.Context, settings Settings, query *CallbackQuery, chatID int64, username string) {
	user, err := b.users.Get(ctx, username)
	if err != nil {
		_ = b.client.answerCallbackQuery(ctx, settings, query.ID, "User not found")
		return
	}
	_ = b.client.answerCallbackQuery(ctx, settings, query.ID, "")
	b.reply(ctx, settings, chatID, linksText(user), nil)
}

// runUserAction performs a lifecycle action then refreshes the detail message.
func (b *Bot) runUserAction(ctx context.Context, settings Settings, query *CallbackQuery, chatID, messageID int64, username, action string) {
	actor, ok := b.authorizer.Actor(ctx, chatID)
	if !ok {
		_ = b.client.answerCallbackQuery(ctx, settings, query.ID, "No admin configured")
		return
	}
	var err error
	var note string
	switch action {
	case "delete":
		err = b.users.Delete(ctx, actor, username)
		note = "Deleted"
	case "activate":
		err = b.users.SetStatus(ctx, actor, username, "active")
		note = "Activated"
	case "suspend":
		err = b.users.SetStatus(ctx, actor, username, "disabled")
		note = "Disabled"
	case "reset":
		err = b.users.Reset(ctx, actor, username)
		note = "Usage reset"
	case "revoke":
		err = b.users.RevokeSubscription(ctx, actor, username)
		note = "Subscription revoked"
	}
	if err != nil {
		_ = b.client.answerCallbackQuery(ctx, settings, query.ID, "Failed")
		_ = b.client.editMessageText(ctx, settings, chatID, messageID, actionErrorText(action, err), nil)
		return
	}
	_ = b.client.answerCallbackQuery(ctx, settings, query.ID, note)
	if action == "delete" {
		_ = b.client.editMessageText(ctx, settings, chatID, messageID,
			fmt.Sprintf("🗑 User <code>%s</code> deleted.", escape(username)), nil)
		return
	}
	b.editRefreshedUser(ctx, settings, chatID, messageID, username, note)
}

func (b *Bot) sendRefreshedUser(ctx context.Context, settings Settings, chatID, messageID int64, username, note string) {
	user, err := b.users.Get(ctx, username)
	if err != nil {
		b.reply(ctx, settings, chatID, notFoundText(username), nil)
		return
	}
	text := userDetailText(user)
	if strings.TrimSpace(note) != "" {
		text = "✅ " + escape(note) + "\n" + text
	}
	if messageID > 0 {
		_ = b.client.editMessageText(ctx, settings, chatID, messageID, text, userMenuKeyboard(user.Username, user.Status))
		return
	}
	b.reply(ctx, settings, chatID, text, userMenuKeyboard(user.Username, user.Status))
}

func (b *Bot) editRefreshedUser(ctx context.Context, settings Settings, chatID, messageID int64, username, note string) {
	user, err := b.users.Get(ctx, username)
	if err != nil {
		_ = b.client.editMessageText(ctx, settings, chatID, messageID, notFoundText(username), nil)
		return
	}
	text := userDetailText(user)
	if strings.TrimSpace(note) != "" {
		text = "✅ " + escape(note) + "\n" + text
	}
	_ = b.client.editMessageText(ctx, settings, chatID, messageID, text, userMenuKeyboard(user.Username, user.Status))
}

func (b *Bot) systemText(ctx context.Context) string {
	if b.system == nil {
		return "System information unavailable."
	}
	info, err := b.system.Info(ctx)
	if err != nil {
		return "System information unavailable."
	}
	return systemInfoText(info)
}

func splitCommand(text string) (command string, arg string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", ""
	}
	parts := strings.SplitN(text, " ", 2)
	command = strings.ToLower(parts[0])
	// Strip a @botname suffix from the command.
	if at := strings.Index(command, "@"); at >= 0 {
		command = command[:at]
	}
	if len(parts) > 1 {
		arg = strings.TrimSpace(parts[1])
	}
	return command, arg
}

func notFoundText(username string) string {
	return fmt.Sprintf("User <code>%s</code> not found.", escape(username))
}

func noActorText() string {
	return "No admin is configured to perform this action."
}

func actionErrorText(action string, err error) string {
	return fmt.Sprintf("Failed to %s: <code>%s</code>", escape(action), escape(err.Error()))
}

func linksText(user UserView) string {
	if len(user.Links) == 0 && strings.TrimSpace(user.SubscriptionURL) == "" {
		return fmt.Sprintf("No links available for <code>%s</code>.", escape(user.Username))
	}
	lines := []string{fmt.Sprintf("🔗 <b>Links for</b> <code>%s</code>", escape(user.Username))}
	if strings.TrimSpace(user.SubscriptionURL) != "" {
		lines = append(lines, "", "<b>Subscription:</b>", "<code>"+escape(user.SubscriptionURL)+"</code>")
	}
	if len(user.Links) > 0 {
		lines = append(lines, "")
		for _, link := range user.Links {
			lines = append(lines, "<code>"+escape(link)+"</code>")
		}
	}
	return strings.Join(lines, "\n")
}
