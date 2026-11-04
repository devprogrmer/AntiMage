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
	isAdmin := b.adminAuthorized(ctx, settings, chatID)
	switch command {
	case "/topup":
		amount, err := strconv.ParseInt(strings.TrimSpace(arg), 10, 64)
		if err != nil || amount < 1000 || amount > 100000000 || msg.MessageID <= 0 {
			b.reply(ctx, settings, chatID, localText(ctx, "Usage: /topup &lt;toman&gt; (1,000 to 100,000,000)", "روش استفاده: /topup &lt;تومان&gt; (۱٬۰۰۰ تا ۱۰۰٬۰۰۰٬۰۰۰)", "Использование: /topup &lt;томан&gt; (от 1 000 до 100 000 000)"), nil)
			return
		}
		instructions, err := b.topups.Instructions(ctx)
		if err != nil || strings.TrimSpace(instructions) == "" {
			b.reply(ctx, settings, chatID, localText(ctx, "Manual wallet topups are not configured yet.", "روش شارژ دستی کیف پول هنوز تنظیم نشده است.", "Ручное пополнение кошелька пока не настроено."), nil)
			return
		}
		request, err := b.topups.Create(ctx, chatID, amount, fmt.Sprintf("tg-topup:%d:%d", chatID, msg.MessageID))
		if err != nil {
			b.reply(ctx, settings, chatID, localText(ctx, "Could not create a topup request. Please try again.", "درخواست شارژ ثبت نشد. دوباره تلاش کنید.", "Не удалось создать запрос на пополнение. Попробуйте снова."), nil)
			return
		}
		b.reply(ctx, settings, chatID, fmt.Sprintf(localText(ctx, "💳 Topup <code>%s</code> for <b>%d toman</b>.\n%s\nAfter transferring, send the receipt photo with caption <code>/receipt %s</code>. Your wallet is credited only after admin approval.", "💳 درخواست شارژ <code>%s</code> به مبلغ <b>%d تومان</b>.\n%s\nپس از انتقال، تصویر رسید را با عنوان <code>/receipt %s</code> بفرستید. کیف پول فقط پس از تأیید مدیر شارژ می‌شود.", "💳 Пополнение <code>%s</code> на <b>%d томан</b>.\n%s\nПосле перевода отправьте фото чека с подписью <code>/receipt %s</code>. Кошелёк пополнится только после подтверждения администратора."), escape(request.ID), request.AmountToman, escape(instructions), escape(request.ID)), nil)
	case "/receipt":
		if len(msg.Photo) == 0 {
			b.reply(ctx, settings, chatID, localText(ctx, "Send the bank receipt as a photo with caption /receipt &lt;topup_id&gt;.", "تصویر رسید بانکی را با عنوان /receipt &lt;شناسهٔ شارژ&gt; بفرستید.", "Отправьте фото чека с подписью /receipt &lt;ID пополнения&gt;."), nil)
			return
		}
		fileID := msg.Photo[len(msg.Photo)-1].FileID
		request, err := b.topups.SubmitReceipt(ctx, chatID, strings.TrimSpace(arg), fileID)
		if err != nil {
			b.reply(ctx, settings, chatID, localText(ctx, "This topup request cannot accept a receipt. Check /topups.", "این درخواست شارژ رسید نمی‌پذیرد. /topups را بررسی کنید.", "Этот запрос не принимает чек. Проверьте /topups."), nil)
			return
		}
		b.reply(ctx, settings, chatID, localText(ctx, "Receipt saved for <code>", "رسید برای <code>", "Чек для <code>")+escape(request.ID)+localText(ctx, "</code>. Awaiting admin review.", "</code> ثبت شد و منتظر بررسی مدیر است.", "</code> сохранён и ожидает проверки администратора."), nil)
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
			b.reply(ctx, settings, chatID, localText(ctx, "Could not load wallet topups.", "درخواست‌های شارژ بارگذاری نشدند.", "Не удалось загрузить пополнения."), nil)
			return
		}
		if len(items) == 0 {
			b.reply(ctx, settings, chatID, localText(ctx, "No topup requests found.", "درخواست شارژی پیدا نشد.", "Запросы на пополнение не найдены."), nil)
			return
		}
		lines := []string{localText(ctx, "<b>Wallet topups</b>", "<b>درخواست‌های شارژ کیف پول</b>", "<b>Пополнения кошелька</b>")}
		for _, item := range items {
			line := fmt.Sprintf(localText(ctx, "<code>%s</code> · %d toman · %s", "<code>%s</code> · %d تومان · %s", "<code>%s</code> · %d томан · %s"), escape(item.ID), item.AmountToman, escape(item.Status))
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
			b.reply(ctx, settings, chatID, localText(ctx, "No submitted receipt found for that request.", "برای این درخواست رسیدی ثبت نشده است.", "Для этого запроса нет отправленного чека."), nil)
			return
		}
		keyboard := &InlineKeyboard{InlineKeyboard: [][]InlineButton{{
			{Text: localText(ctx, "Review approval", "بررسی تأیید", "Проверить одобрение"), CallbackData: "topup:approve:" + request.ID},
			{Text: localText(ctx, "Review rejection", "بررسی رد", "Проверить отклонение"), CallbackData: "topup:reject:" + request.ID},
		}}}
		caption := fmt.Sprintf(localText(ctx, "Receipt for <code>%s</code>\nCustomer: <code>%d</code>\nAmount: <b>%d toman</b>. Verify the transfer before approving.", "رسید درخواست <code>%s</code>\nمشتری: <code>%d</code>\nمبلغ: <b>%d تومان</b>. پیش از تأیید، انتقال وجه را بررسی کنید.", "Чек запроса <code>%s</code>\nКлиент: <code>%d</code>\nСумма: <b>%d томан</b>. Проверьте перевод до подтверждения."), escape(request.ID), request.TelegramID, request.AmountToman)
		if err := b.client.sendPhoto(ctx, settings, chatID, *request.ReceiptFileID, caption, keyboard); err != nil {
			b.logf("telegram bot: review receipt %s: %v", request.ID, err)
			b.reply(ctx, settings, chatID, localText(ctx, "Could not load the receipt photo.", "تصویر رسید بارگذاری نشد.", "Не удалось загрузить фото чека."), nil)
		}
	case "/payment-details":
		if !isAdmin {
			return
		}
		if strings.TrimSpace(arg) == "" {
			b.reply(ctx, settings, chatID, localText(ctx, "Usage: /payment-details &lt;bank transfer instructions&gt;", "روش استفاده: /payment-details &lt;راهنمای واریز بانکی&gt;", "Использование: /payment-details &lt;инструкция для перевода&gt;"), nil)
			return
		}
		if err := b.topups.SetInstructions(ctx, arg); err != nil {
			b.reply(ctx, settings, chatID, localText(ctx, "Could not save payment instructions.", "راهنمای پرداخت ذخیره نشد.", "Не удалось сохранить инструкции по оплате."), nil)
			return
		}
		b.reply(ctx, settings, chatID, localText(ctx, "Manual payment instructions saved.", "راهنمای پرداخت دستی ذخیره شد.", "Инструкции по ручной оплате сохранены."), nil)
	}
}

func (b *Bot) handleTopupCallback(ctx context.Context, settings Settings, query *CallbackQuery) {
	if query.From == nil || query.Message == nil || query.From.ID <= 0 || query.Message.Chat.Type != "private" || query.Message.Chat.ID != query.From.ID || !b.adminAuthorized(ctx, settings, query.From.ID) || b.topups == nil {
		_ = b.client.answerCallbackQuery(ctx, settings, query.ID, localText(ctx, "Not authorized", "دسترسی ندارید", "Нет доступа"))
		return
	}
	parts := strings.SplitN(query.Data, ":", 3)
	if len(parts) != 3 || len(parts[2]) != 36 {
		_ = b.client.answerCallbackQuery(ctx, settings, query.ID, localText(ctx, "Invalid topup", "درخواست شارژ نامعتبر است", "Неверное пополнение"))
		return
	}
	if parts[1] == "approve" || parts[1] == "reject" {
		topup, err := b.topups.Get(ctx, parts[2])
		if err != nil || topup.Status != "submitted" {
			_ = b.client.answerCallbackQuery(ctx, settings, query.ID, localText(ctx, "Topup is not awaiting review", "درخواست شارژ در انتظار بررسی نیست", "Пополнение не ожидает проверки"))
			return
		}
		_ = b.client.answerCallbackQuery(ctx, settings, query.ID, localText(ctx, "Confirm this review action", "این اقدام را تأیید کنید", "Подтвердите действие"))
		action := localText(ctx, "approve", "تأیید", "одобрение")
		if parts[1] == "reject" {
			action = localText(ctx, "reject", "رد", "отклонение")
		}
		keyboard := &InlineKeyboard{InlineKeyboard: [][]InlineButton{{{Text: localText(ctx, "Confirm ", "تأیید ", "Подтвердить ") + action, CallbackData: "topup:" + parts[1] + "-confirm:" + parts[2]}}}}
		b.reply(ctx, settings, query.From.ID, fmt.Sprintf(localText(ctx, "Confirm %s for topup <code>%s</code>: <b>%d toman</b> to Telegram ID <code>%d</code>.", "%s درخواست شارژ <code>%s</code> به مبلغ <b>%d تومان</b> برای شناسهٔ تلگرام <code>%d</code> را تأیید کنید.", "Подтвердите %s пополнения <code>%s</code>: <b>%d томан</b> для Telegram ID <code>%d</code>."), escape(action), escape(topup.ID), topup.AmountToman, topup.TelegramID), keyboard)
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
		_ = b.client.answerCallbackQuery(ctx, settings, query.ID, localText(ctx, "Invalid action", "اقدام نامعتبر است", "Неверное действие"))
		return
	}
	if topupErr != nil {
		_ = b.client.answerCallbackQuery(ctx, settings, query.ID, localText(ctx, "Review failed", "بررسی ناموفق بود", "Ошибка проверки"))
		b.reply(ctx, settings, query.From.ID, localText(ctx, "Topup review failed: ", "بررسی درخواست شارژ ناموفق بود: ", "Не удалось проверить пополнение: ")+escape(topupErr.Error()), nil)
		return
	}
	_ = b.client.answerCallbackQuery(ctx, settings, query.ID, localText(ctx, "Review saved", "نتیجهٔ بررسی ذخیره شد", "Результат проверки сохранён"))
	if parts[1] == "approve-confirm" {
		b.reply(ctx, settings, query.From.ID, localText(ctx, "Topup approved and wallet credited once.", "شارژ تأیید شد و موجودی کیف پول یک‌بار افزایش یافت.", "Пополнение подтверждено, средства зачислены один раз."), nil)
		ownerCtx := context.WithValue(ctx, languageContextKey{}, b.state.language(ctx, owner, "en"))
		b.reply(ownerCtx, settings, owner, localText(ownerCtx, "✅ Your wallet topup was approved. Use /balance to see the current balance.", "✅ شارژ کیف پول شما تأیید شد. موجودی را با /balance ببینید.", "✅ Пополнение кошелька подтверждено. Проверьте баланс командой /balance."), nil)
	} else {
		b.reply(ctx, settings, query.From.ID, localText(ctx, "Topup rejected.", "درخواست شارژ رد شد.", "Пополнение отклонено."), nil)
		ownerCtx := context.WithValue(ctx, languageContextKey{}, b.state.language(ctx, owner, "en"))
		b.reply(ownerCtx, settings, owner, localText(ownerCtx, "Your wallet topup receipt was rejected. Contact support with /support if you need help.", "رسید شارژ کیف پول شما رد شد. برای پیگیری /support را بزنید.", "Чек пополнения отклонён. Для помощи отправьте /support."), nil)
	}
}
