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
			b.reply(ctx, settings, chatID, "Usage: /support &lt;describe your problem&gt;", nil)
			return
		}
		ticket, err := b.support.Create(ctx, chatID, arg, fmt.Sprintf("tg-support:%d:%d", chatID, msg.MessageID))
		if err != nil {
			b.reply(ctx, settings, chatID, "Could not create a support ticket. Please shorten your message and try again.", nil)
			return
		}
		b.reply(ctx, settings, chatID, "Support ticket <code>"+escape(ticket.ID)+"</code> saved. Use /ticket "+escape(ticket.ID)+" to read replies.", nil)
		for _, adminID := range settings.AdminChatIDs {
			if adminID <= 0 || adminID == chatID {
				continue
			}
			b.reply(ctx, settings, adminID, fmt.Sprintf("📩 New support ticket <code>%s</code> from Telegram ID <code>%d</code>. Use /ticket %s to read it.", escape(ticket.ID), chatID, escape(ticket.ID)), nil)
		}
	case "/tickets":
		tickets, err := b.support.List(ctx, chatID, isAdmin)
		if err != nil {
			b.reply(ctx, settings, chatID, "Could not load tickets. Please try again.", nil)
			return
		}
		if len(tickets) == 0 {
			b.reply(ctx, settings, chatID, "No tickets found.", nil)
			return
		}
		lines := []string{"<b>Support tickets</b>"}
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
			b.reply(ctx, settings, chatID, "Ticket not found.", nil)
			return
		}
		b.reply(ctx, settings, chatID, ticketText(ticket), nil)
	case "/reply":
		if !isAdmin {
			return
		}
		parts := strings.SplitN(strings.TrimSpace(arg), " ", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[1]) == "" || msg.MessageID <= 0 {
			b.reply(ctx, settings, chatID, "Usage: /reply &lt;ticket_id&gt; &lt;message&gt;", nil)
			return
		}
		ticket, err := b.support.Reply(ctx, parts[0], parts[1], fmt.Sprintf("tg-reply:%d:%d", chatID, msg.MessageID))
		if err != nil {
			b.reply(ctx, settings, chatID, "Reply failed: "+escape(err.Error()), nil)
			return
		}
		b.reply(ctx, settings, chatID, "Reply saved to ticket <code>"+escape(ticket.ID)+"</code>.", nil)
		b.reply(ctx, settings, ticket.TelegramID, "💬 Support replied to ticket <code>"+escape(ticket.ID)+"</code>. Use /ticket "+escape(ticket.ID)+" to read it.", nil)
	}
}

func ticketText(ticket support.Ticket) string {
	lines := []string{"<b>Ticket " + escape(ticket.ID) + "</b> · " + escape(ticket.Status)}
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
