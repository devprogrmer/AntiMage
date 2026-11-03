package bot

import (
	"context"
	"fmt"
	"strings"

	"github.com/antimage/antimage/internal/app/support"
)

func (b *Bot) handleSupportCommand(ctx context.Context, settings Settings, msg *Message, command, arg string) {
	if b.support == nil || msg.From == nil || msg.From.ID <= 0 || msg.From.ID != msg.Chat.ID || msg.Chat.Type != "private" {
		return
	}
	chatID := msg.Chat.ID
	isAdmin := b.adminAuthorized(ctx, settings, chatID)
	switch command {
	case "/support":
		if strings.TrimSpace(arg) == "" || msg.MessageID <= 0 {
			b.reply(ctx, settings, chatID, localText(ctx, "Usage: /support &lt;describe your problem&gt;", "روش استفاده: /support &lt;توضیح مشکل&gt;", "Использование: /support &lt;описание проблемы&gt;"), nil)
			return
		}
		ticket, err := b.support.Create(ctx, chatID, arg, fmt.Sprintf("tg-support:%d:%d", chatID, msg.MessageID))
		if err != nil {
			b.reply(ctx, settings, chatID, localText(ctx, "Could not create a support ticket. Please shorten your message and try again.", "درخواست پشتیبانی ثبت نشد. پیام را کوتاه‌تر کنید و دوباره تلاش کنید.", "Не удалось создать запрос. Сократите сообщение и попробуйте снова."), nil)
			return
		}
		b.reply(ctx, settings, chatID, localText(ctx, "Support ticket <code>", "درخواست پشتیبانی <code>", "Запрос в поддержку <code>")+escape(ticket.ID)+localText(ctx, "</code> saved. Use /ticket ", "</code> ثبت شد. پاسخ‌ها را با /ticket ", "</code> создан. Ответы: /ticket ")+escape(ticket.ID)+localText(ctx, " to read replies.", "", ""), nil)
		for _, adminID := range settings.AdminChatIDs {
			if adminID <= 0 || adminID == chatID {
				continue
			}
			b.reply(ctx, settings, adminID, fmt.Sprintf("📩 New support ticket <code>%s</code> from Telegram ID <code>%d</code>. Use /ticket %s to read it.", escape(ticket.ID), chatID, escape(ticket.ID)), nil)
		}
	case "/tickets":
		tickets, err := b.support.List(ctx, chatID, isAdmin)
		if err != nil {
			b.reply(ctx, settings, chatID, localText(ctx, "Could not load tickets. Please try again.", "درخواست‌ها بارگذاری نشدند. دوباره تلاش کنید.", "Не удалось загрузить запросы. Попробуйте снова."), nil)
			return
		}
		if len(tickets) == 0 {
			b.reply(ctx, settings, chatID, localText(ctx, "No tickets found.", "درخواست پشتیبانی پیدا نشد.", "Запросы не найдены."), nil)
			return
		}
		lines := []string{localText(ctx, "<b>Support tickets</b>", "<b>درخواست‌های پشتیبانی</b>", "<b>Запросы в поддержку</b>")}
		for _, ticket := range tickets {
			line := fmt.Sprintf("<code>%s</code> · %s", escape(ticket.ID), escape(ticket.Status))
			if isAdmin {
				line += fmt.Sprintf(" · %d", ticket.TelegramID)
			}
			lines = append(lines, line)
		}
		b.reply(ctx, settings, chatID, strings.Join(lines, "\n"), nil)
	case "/ticket":
		ticket, err := b.support.Get(ctx, strings.TrimSpace(arg), chatID, isAdmin)
		if err != nil {
			b.reply(ctx, settings, chatID, localText(ctx, "Ticket not found.", "درخواست پیدا نشد.", "Запрос не найден."), nil)
			return
		}
		b.reply(ctx, settings, chatID, ticketText(ctx, ticket), nil)
	case "/reply":
		if !isAdmin {
			return
		}
		parts := strings.SplitN(strings.TrimSpace(arg), " ", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[1]) == "" || msg.MessageID <= 0 {
			b.reply(ctx, settings, chatID, localText(ctx, "Usage: /reply &lt;ticket_id&gt; &lt;message&gt;", "روش استفاده: /reply &lt;شناسهٔ درخواست&gt; &lt;پیام&gt;", "Использование: /reply &lt;ID запроса&gt; &lt;сообщение&gt;"), nil)
			return
		}
		ticket, err := b.support.Reply(ctx, parts[0], parts[1], fmt.Sprintf("tg-reply:%d:%d", chatID, msg.MessageID))
		if err != nil {
			b.reply(ctx, settings, chatID, localText(ctx, "Reply failed: ", "پاسخ ثبت نشد: ", "Не удалось ответить: ")+escape(err.Error()), nil)
			return
		}
		b.reply(ctx, settings, chatID, localText(ctx, "Reply saved to ticket <code>", "پاسخ برای درخواست <code>", "Ответ на запрос <code>")+escape(ticket.ID)+localText(ctx, "</code>.", "</code> ثبت شد.", "</code> сохранён."), nil)
		customerCtx := context.WithValue(ctx, languageContextKey{}, b.state.language(ctx, ticket.TelegramID, "en"))
		b.reply(customerCtx, settings, ticket.TelegramID, localText(customerCtx, "💬 Support replied to ticket <code>", "💬 پشتیبانی به درخواست <code>", "💬 Поддержка ответила на запрос <code>")+escape(ticket.ID)+localText(customerCtx, "</code>. Use /ticket ", "</code> پاسخ داد. برای خواندن /ticket ", "</code>. Прочитайте ответ: /ticket ")+escape(ticket.ID)+localText(customerCtx, " to read it.", "", ""), nil)
	}
}

func ticketText(ctx context.Context, ticket support.Ticket) string {
	lines := []string{"<b>" + localText(ctx, "Ticket ", "درخواست ", "Запрос ") + escape(ticket.ID) + "</b> · " + escape(ticket.Status)}
	start := len(ticket.Messages) - 5
	if start < 0 {
		start = 0
	}
	for _, message := range ticket.Messages[start:] {
		content := message.Content
		if len([]rune(content)) > 500 {
			content = string([]rune(content)[:500]) + "…"
		}
		lines = append(lines, "<b>"+escape(message.Author)+":</b> "+escape(content))
	}
	return strings.Join(lines, "\n")
}
