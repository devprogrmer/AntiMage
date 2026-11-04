package bot

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strconv"
	"strings"
)

func (b *Bot) handleChangeCallback(ctx context.Context, settings Settings, query *CallbackQuery) {
	shop, ok := b.shop.(ShopChangesService)
	if !ok || b.customers == nil {
		return
	}
	parts := strings.Split(query.Data, ":")
	if len(parts) != 3 && len(parts) != 4 {
		return
	}
	username := parts[len(parts)-1]
	accounts, err := b.customers.ListByTelegramID(ctx, query.From.ID)
	if err != nil {
		return
	}
	var account *UserView
	for i := range accounts {
		if accounts[i].Username == username {
			account = &accounts[i]
			break
		}
	}
	if account == nil || account.ServiceID == nil || account.DataLimit == nil || *account.DataLimit <= 0 {
		return
	}
	if account.Status != "active" && account.Status != "limited" && account.Status != "on_hold" && account.Status != "expired" {
		return
	}
	products, err := b.shop.Products(ctx)
	if err != nil {
		return
	}
	if parts[1] == "list" && len(parts) == 3 {
		_ = b.client.answerCallbackQuery(ctx, settings, query.ID, "")
		for _, p := range products {
			if p.ServiceID != *account.ServiceID {
				continue
			}
			text := fmt.Sprintf(localText(ctx, "%s · %d toman · %.1f GB\nRenewal adds %d days and the listed data. Data-only keeps the current expiry; both cost the listed price.", "%s · %d تومان · %.1f گیگابایت\nتمدید، %d روز و حجم بالا را اضافه می‌کند. خرید فقط حجم، زمان را عوض نمی‌کند؛ قیمت هر دو همان مبلغ بالاست.", "%s · %d томан · %.1f ГБ\nПродление добавляет %d дней и указанный трафик. Покупка только трафика сохраняет срок; обе операции стоят указанную сумму."), p.Title, p.PriceToman, float64(p.DataLimitBytes)/(1<<30), p.DurationDays)
			buttons := []InlineButton{{Text: localText(ctx, "Renew", "تمدید", "Продлить"), CallbackData: fmt.Sprintf("sx:r:%d:%s", p.ID, username)}}
			if account.Status != "expired" {
				buttons = append(buttons, InlineButton{Text: localText(ctx, "Add data only", "فقط افزایش حجم", "Только трафик"), CallbackData: fmt.Sprintf("sx:v:%d:%s", p.ID, username)})
			}
			b.reply(ctx, settings, query.From.ID, escape(text), &InlineKeyboard{InlineKeyboard: [][]InlineButton{buttons}})
		}
		return
	}
	if len(parts) != 4 || (parts[1] != "r" && parts[1] != "v") {
		return
	}
	productID, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || productID <= 0 {
		return
	}
	valid := false
	for _, p := range products {
		if p.ID == productID && p.ServiceID == *account.ServiceID {
			valid = true
			break
		}
	}
	if !valid {
		return
	}
	kind := "renew"
	if parts[1] == "v" {
		kind = "volume"
	}
	key := sha256.Sum256([]byte(query.ID))
	order, err := shop.PlaceChange(ctx, query.From.ID, productID, username, kind, fmt.Sprintf("tg:%x", key[:]))
	if err != nil {
		_ = b.client.answerCallbackQuery(ctx, settings, query.ID, localText(ctx, "Order unavailable", "سفارش در دسترس نیست", "Заказ недоступен"))
		return
	}
	_ = b.client.answerCallbackQuery(ctx, settings, query.ID, "")
	label := localText(ctx, "Renewal", "تمدید", "Продление")
	if kind == "volume" {
		label = localText(ctx, "Data add-on", "افزایش حجم", "Дополнительный трафик")
	}
	b.reply(ctx, settings, query.From.ID, escape(fmt.Sprintf("%s · %s · %d", label, username, order.PriceToman))+localText(ctx, " toman. Confirm wallet payment below.", " تومان. پرداخت از کیف پول را در پایین تأیید کنید.", " томан. Подтвердите оплату из кошелька ниже."), &InlineKeyboard{InlineKeyboard: [][]InlineButton{{{Text: localText(ctx, "Pay / retry delivery", "پرداخت / تلاش دوباره برای تحویل", "Оплатить / повторить выдачу"), CallbackData: "shop:pay:" + order.ID}}}})
}
