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
		language := b.state.language(ctx, chatID, msg.From.LanguageCode)
		ctx = context.WithValue(ctx, languageContextKey{}, language)
		if command == "/lang" || command == "/language" {
			if strings.TrimSpace(arg) == "" {
				b.reply(ctx, settings, chatID, "🇮🇷 /lang fa · 🇬🇧 /lang en · 🇷🇺 /lang ru", menuForLanguage(b.adminAuthorized(ctx, settings, chatID), language, settings.MiniAppURL))
				return
			}
			if arg != "fa" && arg != "en" && arg != "ru" {
				b.reply(ctx, settings, chatID, localText(ctx, "Use /lang fa, /lang en, or /lang ru.", "برای انتخاب زبان /lang fa یا /lang en یا /lang ru را بزنید.", "Выберите язык командой /lang fa, /lang en или /lang ru."), nil)
				return
			}
			if err := b.state.setLanguage(ctx, chatID, arg); err != nil {
				b.reply(ctx, settings, chatID, localText(ctx, "Could not save language. Please try again.", "زبان ذخیره نشد. دوباره تلاش کنید.", "Не удалось сохранить язык. Попробуйте ещё раз."), nil)
				return
			}
			b.reply(ctx, settings, chatID, languageChangedText(arg), menuForLanguage(b.adminAuthorized(ctx, settings, chatID), arg, settings.MiniAppURL))
			return
		}
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
				b.reply(ctx, settings, chatID, welcomeForLanguage(language, false), menuForLanguage(false, language, settings.MiniAppURL))
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
		language := b.state.language(ctx, chatID, msg.From.LanguageCode)
		b.reply(ctx, settings, chatID, welcomeForLanguage(language, true), menuForLanguage(true, language, settings.MiniAppURL))
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
			b.reply(ctx, settings, chatID, localText(ctx, "Unknown command. Send /help.", "دستور شناخته نشد. /help را بزنید.", "Неизвестная команда. Отправьте /help."), nil)
		}
	}
}

func (b *Bot) handleCustomerAccounts(ctx context.Context, settings Settings, chatID, telegramID int64) {
	if b.customers == nil {
		b.reply(ctx, settings, chatID, localText(ctx, "Customer services are unavailable.", "سرویس‌های کاربران در دسترس نیستند.", "Услуги пользователей недоступны."), nil)
		return
	}
	accounts, err := b.customers.ListByTelegramID(ctx, telegramID)
	if err != nil {
		b.reply(ctx, settings, chatID, localText(ctx, "Could not load your services. Please try again later.", "سرویس‌ها بارگذاری نشدند. کمی بعد دوباره تلاش کنید.", "Не удалось загрузить ваши услуги. Попробуйте позже."), nil)
		return
	}
	if len(accounts) == 0 {
		b.reply(ctx, settings, chatID, localText(ctx, "No services are linked to your Telegram account. Ask your provider to set your Telegram ID on your AntiMage user.", "سرویسی به حساب تلگرام شما وصل نیست. از مدیر بخواهید شناسهٔ تلگرام شما را برای کاربر AntiMage ثبت کند.", "К вашему аккаунту Telegram не привязаны услуги. Попросите администратора указать ваш Telegram ID в AntiMage."), nil)
		return
	}
	for _, account := range accounts {
		lines := []string{localText(ctx, "👤 <b>Your service</b>", "👤 <b>سرویس شما</b>", "👤 <b>Ваша услуга</b>"), line(localText(ctx, "Username", "نام کاربری", "Пользователь"), account.Username), "<b>" + localText(ctx, "Status", "وضعیت", "Статус") + ":</b> " + localStatusLine(ctx, account.Status), line(localText(ctx, "Used", "مصرف", "Использовано"), formatBytes(account.UsedTraffic)), line(localText(ctx, "Limit", "سقف حجم", "Лимит"), localOptionalBytes(ctx, account.DataLimit)), line(localText(ctx, "Expires", "انقضا", "Истекает"), localExpire(ctx, account.Expire))}
		if strings.TrimSpace(account.SubscriptionURL) != "" {
			lines = append(lines, line(localText(ctx, "Subscription", "لینک اشتراک", "Подписка"), account.SubscriptionURL))
		}
		b.reply(ctx, settings, chatID, strings.Join(lines, "\n"), nil)
	}
}

func (b *Bot) handleUsageCommand(ctx context.Context, settings Settings, chatID int64, username string) {
	username = strings.TrimSpace(username)
	if username == "" {
		b.reply(ctx, settings, chatID, localText(ctx, "Usage: <code>/usage &lt;username&gt;</code>", "روش استفاده: <code>/usage &lt;نام کاربری&gt;</code>", "Использование: <code>/usage &lt;имя пользователя&gt;</code>"), nil)
		return
	}
	user, err := b.users.Get(ctx, username)
	if err != nil {
		b.reply(ctx, settings, chatID, notFoundText(ctx, username), nil)
		return
	}
	b.reply(ctx, settings, chatID, userUsageText(ctx, user), nil)
}

func (b *Bot) handleUserCommand(ctx context.Context, settings Settings, chatID int64, username string) {
	username = strings.TrimSpace(username)
	if username == "" {
		b.reply(ctx, settings, chatID, localText(ctx, "Usage: <code>/user &lt;username&gt;</code>", "روش استفاده: <code>/user &lt;نام کاربری&gt;</code>", "Использование: <code>/user &lt;имя пользователя&gt;</code>"), nil)
		return
	}
	user, err := b.users.Get(ctx, username)
	if err != nil {
		b.reply(ctx, settings, chatID, notFoundText(ctx, username), nil)
		return
	}
	b.reply(ctx, settings, chatID, userDetailText(ctx, user), localizeKeyboard(languageFromContext(ctx), userMenuKeyboard(user.Username, user.Status)))
}

func (b *Bot) handleConversation(ctx context.Context, settings Settings, chatID int64, conv conversation, text string) {
	switch conv.State {
	case stateAwaitNote:
		username := conv.Payload
		_ = b.state.clear(ctx, chatID)
		actor, ok := b.authorizer.Actor(ctx, chatID)
		if !ok {
			b.reply(ctx, settings, chatID, noActorText(ctx), nil)
			return
		}
		if err := b.users.SetNote(ctx, actor, username, text); err != nil {
			b.reply(ctx, settings, chatID, actionErrorText(ctx, "update note", err), nil)
			return
		}
		b.sendRefreshedUser(ctx, settings, chatID, 0, username, localText(ctx, "Note updated", "یادداشت به‌روزرسانی شد", "Заметка обновлена"))
	default:
		_ = b.state.clear(ctx, chatID)
	}
}

func (b *Bot) handleCallback(ctx context.Context, settings Settings, query *CallbackQuery) {
	if query.From != nil && query.From.ID > 0 && query.Message != nil && query.Message.Chat.Type == "private" && query.Message.Chat.ID == query.From.ID {
		ctx = context.WithValue(ctx, languageContextKey{}, b.state.language(ctx, query.From.ID, query.From.LanguageCode))
	}
	if strings.HasPrefix(query.Data, "lang:") && query.From != nil && query.From.ID > 0 && query.Message != nil && query.Message.Chat.Type == "private" && query.Message.Chat.ID == query.From.ID {
		language := strings.TrimPrefix(query.Data, "lang:")
		if language == "fa" || language == "en" || language == "ru" {
			if err := b.state.setLanguage(ctx, query.From.ID, language); err == nil {
				_ = b.client.answerCallbackQuery(ctx, settings, query.ID, "")
				b.reply(ctx, settings, query.From.ID, languageChangedText(language), menuForLanguage(b.adminAuthorized(ctx, settings, query.From.ID), language, settings.MiniAppURL))
				return
			}
		}
		_ = b.client.answerCallbackQuery(ctx, settings, query.ID, localText(ctx, "Invalid language", "زبان نامعتبر است", "Неверный язык"))
		return
	}
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
		_ = b.client.answerCallbackQuery(ctx, settings, query.ID, localText(ctx, "Not authorized", "دسترسی ندارید", "Нет доступа"))
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
		_ = b.client.editMessageText(ctx, settings, chatID, messageID, b.systemText(ctx), localizeKeyboard(languageFromContext(ctx), mainMenuKeyboard()))
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
		b.reply(ctx, settings, chatID, fmt.Sprintf(localText(ctx, "Send the new note for <code>%s</code>:", "یادداشت جدید برای <code>%s</code> را بفرستید:", "Отправьте новую заметку для <code>%s</code>:"), escape(value)), nil)
	case cbDelete:
		_ = b.client.answerCallbackQuery(ctx, settings, query.ID, "")
		_ = b.client.editMessageText(ctx, settings, chatID, messageID,
			fmt.Sprintf(localText(ctx, "⚠️ Delete user <code>%s</code>? This cannot be undone.", "⚠️ کاربر <code>%s</code> حذف شود؟ این کار قابل بازگشت نیست.", "⚠️ Удалить пользователя <code>%s</code>? Это нельзя отменить."), escape(value)),
			localizeKeyboard(languageFromContext(ctx), confirmDeleteKeyboard(value)))
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
		_ = b.client.answerCallbackQuery(ctx, settings, query.ID, localText(ctx, "User not found", "کاربر پیدا نشد", "Пользователь не найден"))
		return
	}
	_ = b.client.answerCallbackQuery(ctx, settings, query.ID, "")
	b.reply(ctx, settings, chatID, linksText(ctx, user), nil)
}

// runUserAction performs a lifecycle action then refreshes the detail message.
func (b *Bot) runUserAction(ctx context.Context, settings Settings, query *CallbackQuery, chatID, messageID int64, username, action string) {
	actor, ok := b.authorizer.Actor(ctx, chatID)
	if !ok {
		_ = b.client.answerCallbackQuery(ctx, settings, query.ID, localText(ctx, "No admin configured", "مدیری تنظیم نشده است", "Администратор не настроен"))
		return
	}
	var err error
	var note string
	switch action {
	case "delete":
		err = b.users.Delete(ctx, actor, username)
		note = localText(ctx, "Deleted", "حذف شد", "Удалено")
	case "activate":
		err = b.users.SetStatus(ctx, actor, username, "active")
		note = localText(ctx, "Activated", "فعال شد", "Активировано")
	case "suspend":
		err = b.users.SetStatus(ctx, actor, username, "disabled")
		note = localText(ctx, "Disabled", "غیرفعال شد", "Отключено")
	case "reset":
		err = b.users.Reset(ctx, actor, username)
		note = localText(ctx, "Usage reset", "مصرف صفر شد", "Использование сброшено")
	case "revoke":
		err = b.users.RevokeSubscription(ctx, actor, username)
		note = localText(ctx, "Subscription revoked", "اشتراک لغو شد", "Подписка отозвана")
	}
	if err != nil {
		_ = b.client.answerCallbackQuery(ctx, settings, query.ID, localText(ctx, "Failed", "ناموفق", "Ошибка"))
		_ = b.client.editMessageText(ctx, settings, chatID, messageID, actionErrorText(ctx, action, err), nil)
		return
	}
	_ = b.client.answerCallbackQuery(ctx, settings, query.ID, note)
	if action == "delete" {
		_ = b.client.editMessageText(ctx, settings, chatID, messageID,
			fmt.Sprintf(localText(ctx, "🗑 User <code>%s</code> deleted.", "🗑 کاربر <code>%s</code> حذف شد.", "🗑 Пользователь <code>%s</code> удалён."), escape(username)), nil)
		return
	}
	b.editRefreshedUser(ctx, settings, chatID, messageID, username, note)
}

func (b *Bot) sendRefreshedUser(ctx context.Context, settings Settings, chatID, messageID int64, username, note string) {
	user, err := b.users.Get(ctx, username)
	if err != nil {
		b.reply(ctx, settings, chatID, notFoundText(ctx, username), nil)
		return
	}
	text := userDetailText(ctx, user)
	if strings.TrimSpace(note) != "" {
		text = "✅ " + escape(note) + "\n" + text
	}
	if messageID > 0 {
		_ = b.client.editMessageText(ctx, settings, chatID, messageID, text, localizeKeyboard(languageFromContext(ctx), userMenuKeyboard(user.Username, user.Status)))
		return
	}
	b.reply(ctx, settings, chatID, text, localizeKeyboard(languageFromContext(ctx), userMenuKeyboard(user.Username, user.Status)))
}

func (b *Bot) editRefreshedUser(ctx context.Context, settings Settings, chatID, messageID int64, username, note string) {
	user, err := b.users.Get(ctx, username)
	if err != nil {
		_ = b.client.editMessageText(ctx, settings, chatID, messageID, notFoundText(ctx, username), nil)
		return
	}
	text := userDetailText(ctx, user)
	if strings.TrimSpace(note) != "" {
		text = "✅ " + escape(note) + "\n" + text
	}
	_ = b.client.editMessageText(ctx, settings, chatID, messageID, text, localizeKeyboard(languageFromContext(ctx), userMenuKeyboard(user.Username, user.Status)))
}

func (b *Bot) systemText(ctx context.Context) string {
	if b.system == nil {
		return localText(ctx, "System information unavailable.", "اطلاعات سیستم در دسترس نیست.", "Информация о системе недоступна.")
	}
	info, err := b.system.Info(ctx)
	if err != nil {
		return localText(ctx, "System information unavailable.", "اطلاعات سیستم در دسترس نیست.", "Информация о системе недоступна.")
	}
	return systemInfoText(ctx, info)
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

func notFoundText(ctx context.Context, username string) string {
	return fmt.Sprintf(localText(ctx, "User <code>%s</code> not found.", "کاربر <code>%s</code> پیدا نشد.", "Пользователь <code>%s</code> не найден."), escape(username))
}

func noActorText(ctx context.Context) string {
	return localText(ctx, "No admin is configured to perform this action.", "مدیری برای انجام این کار تنظیم نشده است.", "Для этого действия не назначен администратор.")
}

func actionErrorText(ctx context.Context, action string, err error) string {
	switch action {
	case "update note":
		action = localText(ctx, action, "به‌روزرسانی یادداشت", "обновление заметки")
	case "delete":
		action = localText(ctx, action, "حذف کاربر", "удаление пользователя")
	case "activate":
		action = localText(ctx, action, "فعال‌سازی کاربر", "активацию пользователя")
	case "suspend":
		action = localText(ctx, action, "غیرفعال‌کردن کاربر", "отключение пользователя")
	case "reset":
		action = localText(ctx, action, "صفرکردن مصرف", "сброс использования")
	case "revoke":
		action = localText(ctx, action, "لغو اشتراک", "отзыв подписки")
	}
	return fmt.Sprintf(localText(ctx, "Failed to %s: <code>%s</code>", "انجام %s ناموفق بود: <code>%s</code>", "Не удалось выполнить %s: <code>%s</code>"), escape(action), escape(err.Error()))
}

func linksText(ctx context.Context, user UserView) string {
	if len(user.Links) == 0 && strings.TrimSpace(user.SubscriptionURL) == "" {
		return fmt.Sprintf(localText(ctx, "No links available for <code>%s</code>.", "لینکی برای <code>%s</code> موجود نیست.", "Нет ссылок для <code>%s</code>."), escape(user.Username))
	}
	lines := []string{fmt.Sprintf(localText(ctx, "🔗 <b>Links for</b> <code>%s</code>", "🔗 <b>لینک‌های</b> <code>%s</code>", "🔗 <b>Ссылки для</b> <code>%s</code>"), escape(user.Username))}
	if strings.TrimSpace(user.SubscriptionURL) != "" {
		lines = append(lines, "", "<b>"+localText(ctx, "Subscription", "اشتراک", "Подписка")+":</b>", "<code>"+escape(user.SubscriptionURL)+"</code>")
	}
	if len(user.Links) > 0 {
		lines = append(lines, "")
		for _, link := range user.Links {
			lines = append(lines, "<code>"+escape(link)+"</code>")
		}
	}
	return strings.Join(lines, "\n")
}
