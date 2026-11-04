package bot

import (
	"context"
	"fmt"
	"github.com/antimage/antimage/internal/app/shop"
	"github.com/google/uuid"
	"strconv"
	"strings"
)

func (b *Bot) handleGatewayCommand(ctx context.Context, settings Settings, msg *Message, command, arg string) {
	g, ok := b.topups.(GatewayTopupService)
	if !ok || msg.From == nil || msg.From.ID <= 0 || msg.Chat.Type != "private" || msg.Chat.ID != msg.From.ID {
		return
	}
	owner := msg.From.ID
	switch command {
	case "/payonline":
		if !g.GatewayEnabled() {
			b.reply(ctx, settings, owner, localText(ctx, "Online payments are not configured. Use /topup for manual payment instructions.", "پرداخت آنلاین هنوز تنظیم نشده است. برای روش دستی /topup را ببینید.", "Онлайн-платежи не настроены. Инструкции для ручной оплаты: /topup."), nil)
			return
		}
		amount, err := strconv.ParseInt(strings.TrimSpace(arg), 10, 64)
		if err != nil || amount < 1000 || amount > 100000000 || msg.MessageID <= 0 {
			b.reply(ctx, settings, owner, localText(ctx, "Usage: /payonline &lt;toman&gt; (1,000 to 100,000,000)", "روش استفاده: /payonline &lt;تومان&gt; (۱٬۰۰۰ تا ۱۰۰٬۰۰۰٬۰۰۰)", "Использование: /payonline &lt;томан&gt; (от 1 000 до 100 000 000)"), nil)
			return
		}
		p, err := g.CreateGateway(ctx, owner, amount, fmt.Sprintf("tg-gateway:%d:%d", owner, msg.MessageID))
		if err != nil {
			b.reply(ctx, settings, owner, localText(ctx, "Checkout unavailable. Retry this payment; do not transfer to an unknown link.", "لینک پرداخت ساخته نشد. همین درخواست را دوباره امتحان کنید.", "Ссылка оплаты недоступна. Повторите этот запрос."), nil)
			return
		}
		b.sendGatewayPayment(ctx, settings, owner, p)
	case "/payment":
		id := strings.TrimSpace(arg)
		if uuid.Validate(id) != nil {
			return
		}
		p, err := g.CheckGateway(ctx, owner, id)
		if err != nil {
			b.reply(ctx, settings, owner, localText(ctx, "Payment is not confirmed. If you paid, check this payment again from /payments.", "پرداخت تأیید نشده است. اگر پرداخت کردید، وضعیت همین پرداخت را از /payments دوباره بررسی کنید.", "Платёж не подтверждён. Если вы оплатили, проверьте этот платёж снова через /payments."), nil)
			return
		}
		b.sendGatewayPayment(ctx, settings, owner, p)
	case "/payments":
		b.reply(ctx, settings, owner, localText(ctx, "Use /payonline &lt;toman&gt; to fund your wallet. After payment, return to /orders to pay for your service.", "برای شارژ کیف پول /payonline &lt;تومان&gt; را بزنید. بعد از پرداخت، از /orders سفارش سرویس را پرداخت کنید.", "Для пополнения кошелька: /payonline &lt;томан&gt;. После пополнения оплатите услугу через /orders."), nil)
		items, err := g.ListGateway(ctx, owner)
		if err != nil {
			return
		}
		for _, p := range items {
			b.sendGatewayPayment(ctx, settings, owner, p)
		}
	}
}

func (b *Bot) sendGatewayPayment(ctx context.Context, settings Settings, owner int64, p shop.GatewayPayment) {
	status := localText(ctx, "Awaiting payment", "در انتظار پرداخت", "Ожидает оплаты")
	if p.Status == "paid" {
		status = localText(ctx, "Payment confirmed; wallet credited", "پرداخت تأیید شد؛ کیف پول شارژ شد", "Платёж подтверждён; кошелёк пополнен")
	}
	if p.Status == "verified" {
		status = localText(ctx, "Verified; credit pending", "تأییدشده؛ در انتظار ثبت شارژ", "Подтверждён; пополнение ожидается")
	}
	if p.Status == "creating" {
		status = localText(ctx, "Creating checkout", "در حال ساخت لینک", "Создание ссылки")
	}
	buttons := [][]InlineButton{}
	if p.CheckoutURL != "" {
		buttons = append(buttons, []InlineButton{{Text: localText(ctx, "Pay online", "پرداخت آنلاین", "Оплатить онлайн"), URL: p.CheckoutURL}})
	}
	if p.Status == "awaiting" || p.Status == "verified" {
		buttons = append(buttons, []InlineButton{{Text: localText(ctx, "Check payment", "بررسی پرداخت", "Проверить оплату"), CallbackData: "gp:" + p.ID}})
	}
	b.reply(ctx, settings, owner, escape(fmt.Sprintf("%s · %d %s\n%s", p.ID, p.AmountToman, localText(ctx, "toman", "تومان", "томан"), status)), &InlineKeyboard{InlineKeyboard: buttons})
}
