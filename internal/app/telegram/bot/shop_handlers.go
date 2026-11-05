package bot

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/antimage/antimage/internal/app/shop"
)

func (b *Bot) handleAdminSales(ctx context.Context, settings Settings, chatID int64) {
	if b.shop == nil {
		b.reply(ctx, settings, chatID, localText(ctx, "Shop unavailable.", "فروشگاه در دسترس نیست.", "Магазин недоступен."), nil)
		return
	}
	result, err := b.shop.SalesOverview(ctx)
	if err != nil {
		b.reply(ctx, settings, chatID, localText(ctx, "Could not load sales report. Please try again.", "گزارش فروش بارگذاری نشد. دوباره تلاش کنید.", "Не удалось загрузить отчёт о продажах. Попробуйте снова."), nil)
		return
	}
	b.reply(ctx, settings, chatID, fmt.Sprintf(localText(ctx, "📊 <b>AntiMage sales</b>\nOrders: %d\nAwaiting payment: %d\nPaid, awaiting delivery: %d\nDelivered: %d\nRecorded paid value: %d toman", "📊 <b>فروش AntiMage</b>\nسفارش‌ها: %d\nدر انتظار پرداخت: %d\nپرداخت‌شده، در انتظار تحویل: %d\nتحویل‌شده: %d\nمبلغ پرداختی ثبت‌شده: %d تومان", "📊 <b>Продажи AntiMage</b>\nЗаказы: %d\nОжидают оплаты: %d\nОплачены, ожидают выдачи: %d\nВыданы: %d\nУчтённая сумма оплаты: %d томан"),
		result.TotalOrders, result.PendingOrders, result.UnfulfilledOrders, result.CompletedOrders, result.PaidToman), nil)
}

func (b *Bot) handleAdminWallet(ctx context.Context, settings Settings, chatID int64, arg string) {
	owner, err := strconv.ParseInt(strings.TrimSpace(arg), 10, 64)
	if err != nil || owner <= 0 {
		b.reply(ctx, settings, chatID, localText(ctx, "Usage: /wallet &lt;telegram_id&gt;", "روش استفاده: /wallet &lt;شناسهٔ تلگرام&gt;", "Использование: /wallet &lt;ID Telegram&gt;"), nil)
		return
	}
	b.handleWalletHistory(ctx, settings, chatID, owner)
}

func (b *Bot) handleWalletHistory(ctx context.Context, settings Settings, chatID, telegramID int64) {
	if b.shop == nil {
		b.reply(ctx, settings, chatID, localText(ctx, "Wallet unavailable.", "کیف پول در دسترس نیست.", "Кошелёк недоступен."), nil)
		return
	}
	balance, err := b.shop.Balance(ctx, telegramID)
	if err != nil {
		b.reply(ctx, settings, chatID, localText(ctx, "Could not load wallet balance.", "موجودی کیف پول بارگذاری نشد.", "Не удалось загрузить баланс кошелька."), nil)
		return
	}
	entries, err := b.shop.History(ctx, telegramID)
	if err != nil {
		b.reply(ctx, settings, chatID, localText(ctx, "Could not load wallet history.", "تاریخچهٔ کیف پول بارگذاری نشد.", "Не удалось загрузить историю кошелька."), nil)
		return
	}
	lines := []string{fmt.Sprintf(localText(ctx, "💳 <b>Wallet %d</b> · %d toman", "💳 <b>کیف پول %d</b> · %d تومان", "💳 <b>Кошелёк %d</b> · %d томан"), telegramID, balance)}
	if len(entries) == 0 {
		lines = append(lines, localText(ctx, "No transactions recorded.", "تراکنشی ثبت نشده است.", "Транзакций нет."))
	}
	for _, entry := range entries {
		lines = append(lines, fmt.Sprintf(localText(ctx, "%s · %+d toman · %s · <code>%s</code>", "%s · %+d تومان · %s · <code>%s</code>", "%s · %+d томан · %s · <code>%s</code>"), escape(entry.CreatedAt), entry.Delta, escape(entry.Reason), escape(entry.Reference)))
	}
	b.reply(ctx, settings, chatID, strings.Join(lines, "\n"), nil)
}

func (b *Bot) handleAdminShopCommand(ctx context.Context, settings Settings, msg *Message, command, arg string) {
	if msg.From == nil || msg.From.ID != msg.Chat.ID || msg.Chat.Type != "private" || !b.adminAuthorized(ctx, settings, msg.From.ID) || b.shop == nil {
		return
	}
	chatID := msg.Chat.ID
	fields := strings.Fields(arg)
	switch command {
	case "/credit":
		if len(fields) != 3 {
			b.reply(ctx, settings, chatID, localText(ctx, "Usage: /credit &lt;telegram_id&gt; &lt;toman&gt; &lt;unique_reference&gt;", "روش استفاده: /credit &lt;شناسهٔ تلگرام&gt; &lt;تومان&gt; &lt;شناسهٔ یکتا&gt;", "Использование: /credit &lt;ID Telegram&gt; &lt;томан&gt; &lt;уникальный номер&gt;"), nil)
			return
		}
		owner, ownerErr := strconv.ParseInt(fields[0], 10, 64)
		amount, amountErr := strconv.ParseInt(fields[1], 10, 64)
		if ownerErr != nil || amountErr != nil || owner <= 0 || amount <= 0 {
			b.reply(ctx, settings, chatID, localText(ctx, "Telegram ID and amount must be positive integers.", "شناسهٔ تلگرام و مبلغ باید عدد صحیح مثبت باشند.", "ID Telegram и сумма должны быть положительными целыми числами."), nil)
			return
		}
		balance, err := b.shop.AdminCredit(ctx, owner, amount, "admin:"+fields[2])
		if err != nil {
			b.reply(ctx, settings, chatID, localText(ctx, "Credit failed: ", "شارژ ناموفق بود: ", "Не удалось пополнить: ")+escape(err.Error()), nil)
			return
		}
		b.reply(ctx, settings, chatID, fmt.Sprintf(localText(ctx, "Credit reference processed once. Wallet %d balance: %d toman.", "شناسهٔ شارژ فقط یک‌بار پردازش شد. موجودی کیف پول %d: %d تومان.", "Пополнение учтено один раз. Баланс кошелька %d: %d томан."), owner, balance), nil)
	case "/plan-add":
		if len(fields) < 5 || msg.MessageID <= 0 {
			b.reply(ctx, settings, chatID, localText(ctx, "Usage: /plan-add &lt;service_id&gt; &lt;toman&gt; &lt;GB&gt; &lt;days&gt; &lt;title&gt;", "روش استفاده: /plan-add &lt;شناسهٔ سرویس&gt; &lt;تومان&gt; &lt;گیگابایت&gt; &lt;روز&gt; &lt;عنوان&gt;", "Использование: /plan-add &lt;ID услуги&gt; &lt;томан&gt; &lt;ГБ&gt; &lt;дни&gt; &lt;название&gt;"), nil)
			return
		}
		serviceID, e1 := strconv.ParseInt(fields[0], 10, 64)
		price, e2 := strconv.ParseInt(fields[1], 10, 64)
		gb, e3 := strconv.ParseInt(fields[2], 10, 64)
		days, e4 := strconv.ParseInt(fields[3], 10, 64)
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil || serviceID <= 0 || price <= 0 || gb <= 0 || gb > math.MaxInt64/(1<<30) || days <= 0 {
			b.reply(ctx, settings, chatID, localText(ctx, "Invalid service, price, GB, or duration.", "شناسهٔ سرویس، قیمت، حجم یا مدت نامعتبر است.", "Неверная услуга, цена, объём или срок."), nil)
			return
		}
		product, err := b.shop.AddProduct(ctx, shop.Product{ServiceID: serviceID, Title: strings.Join(fields[4:], " "), PriceToman: price, DataLimitBytes: gb * (1 << 30), DurationDays: days}, fmt.Sprintf("tg-plan:%d:%d", chatID, msg.MessageID))
		if err != nil {
			b.reply(ctx, settings, chatID, localText(ctx, "Plan creation failed: ", "ساخت طرح ناموفق بود: ", "Не удалось создать тариф: ")+escape(err.Error()), nil)
			return
		}
		b.reply(ctx, settings, chatID, fmt.Sprintf(localText(ctx, "Plan #%d created for service #%d. Use /shop to view it.", "طرح شمارهٔ %d برای سرویس شمارهٔ %d ساخته شد. آن را با /shop ببینید.", "Тариф №%d создан для услуги №%d. Просмотр: /shop."), product.ID, product.ServiceID), nil)
	case "/plan-disable":
		if len(fields) != 1 {
			b.reply(ctx, settings, chatID, localText(ctx, "Usage: /plan-disable &lt;product_id&gt;", "روش استفاده: /plan-disable &lt;شناسهٔ طرح&gt;", "Использование: /plan-disable &lt;ID тарифа&gt;"), nil)
			return
		}
		productID, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil || productID <= 0 {
			b.reply(ctx, settings, chatID, localText(ctx, "Invalid product ID.", "شناسهٔ طرح نامعتبر است.", "Неверный ID тарифа."), nil)
			return
		}
		if err := b.shop.DisableProduct(ctx, productID); err != nil {
			b.reply(ctx, settings, chatID, localText(ctx, "Plan disable failed: ", "غیرفعال‌کردن طرح ناموفق بود: ", "Не удалось отключить тариф: ")+escape(err.Error()), nil)
			return
		}
		b.reply(ctx, settings, chatID, fmt.Sprintf(localText(ctx, "Plan #%d disabled.", "طرح شمارهٔ %d غیرفعال شد.", "Тариф №%d отключён."), productID), nil)
	}
}

func (b *Bot) handleShopCommand(ctx context.Context, settings Settings, chatID, telegramID int64, command string) {
	if b.shop == nil {
		b.reply(ctx, settings, chatID, localText(ctx, "The shop is not available yet.", "فروشگاه فعلاً در دسترس نیست.", "Магазин пока недоступен."), nil)
		return
	}
	switch command {
	case "/balance":
		balance, err := b.shop.Balance(ctx, telegramID)
		if err != nil {
			b.reply(ctx, settings, chatID, localText(ctx, "Could not load your wallet. Please try again.", "موجودی کیف پول بارگذاری نشد. دوباره تلاش کنید.", "Не удалось загрузить кошелёк. Попробуйте ещё раз."), nil)
			return
		}
		b.reply(ctx, settings, chatID, fmt.Sprintf(localText(ctx, "💳 Wallet balance: <b>%d toman</b>", "💳 موجودی کیف پول: <b>%d تومان</b>", "💳 Баланс кошелька: <b>%d томан</b>"), balance), nil)
	case "/transactions":
		b.handleWalletHistory(ctx, settings, chatID, telegramID)
	case "/shop":
		products, err := b.shop.Products(ctx)
		if err != nil {
			b.reply(ctx, settings, chatID, localText(ctx, "Could not load plans. Please try again.", "طرح‌ها بارگذاری نشدند. دوباره تلاش کنید.", "Не удалось загрузить тарифы. Попробуйте ещё раз."), nil)
			return
		}
		if len(products) == 0 {
			b.reply(ctx, settings, chatID, localText(ctx, "No plans are currently available.", "فعلاً طرحی برای فروش موجود نیست.", "Пока нет доступных тарифов."), nil)
			return
		}
		for _, product := range products {
			label := fmt.Sprintf(localText(ctx, "%s · %d toman · %d days · %s", "%s · %d تومان · %d روز · %s", "%s · %d томан · %d дней · %s"), product.Title, product.PriceToman, product.DurationDays, formatBytes(product.DataLimitBytes))
			keyboard := &InlineKeyboard{InlineKeyboard: [][]InlineButton{{{Text: localText(ctx, "Create order", "ساخت سفارش", "Создать заказ"), CallbackData: fmt.Sprintf("shop:buy:%d", product.ID)}}}}
			b.reply(ctx, settings, chatID, "🛒 "+escape(label), keyboard)
		}
	case "/orders":
		orders, err := b.shop.Orders(ctx, telegramID)
		if err != nil {
			b.reply(ctx, settings, chatID, localText(ctx, "Could not load orders. Please try again.", "سفارش‌ها بارگذاری نشدند. دوباره تلاش کنید.", "Не удалось загрузить заказы. Попробуйте ещё раз."), nil)
			return
		}
		if len(orders) == 0 {
			b.reply(ctx, settings, chatID, localText(ctx, "You have no orders yet. Use /shop to browse plans.", "هنوز سفارشی ندارید. برای دیدن طرح‌ها /shop را بزنید.", "У вас пока нет заказов. Отправьте /shop для просмотра тарифов."), nil)
			return
		}
		for _, order := range orders {
			label := fmt.Sprintf(localText(ctx, "Order %s\nStatus: %s\nPrice: %d toman", "سفارش %s\nوضعیت: %s\nقیمت: %d تومان", "Заказ %s\nСтатус: %s\nЦена: %d томан"), order.ID, order.Status, order.PriceToman)
			var keyboard *InlineKeyboard
			if order.Status == "pending" || order.Status == "paid" || order.Status == "provisioning" {
				keyboard = &InlineKeyboard{InlineKeyboard: [][]InlineButton{{{Text: localText(ctx, "Pay / retry delivery", "پرداخت / تلاش دوباره برای تحویل", "Оплатить / повторить выдачу"), CallbackData: "shop:pay:" + order.ID}}}}
			}
			if order.Username != nil && order.Status == "completed" {
				label += localText(ctx, "\nService: ", "\nسرویس: ", "\nУслуга: ") + *order.Username
			}
			b.reply(ctx, settings, chatID, escape(label), keyboard)
		}
	}
}

func (b *Bot) handleShopCallback(ctx context.Context, settings Settings, query *CallbackQuery) {
	chatID := query.From.ID
	if b.shop == nil {
		_ = b.client.answerCallbackQuery(ctx, settings, query.ID, localText(ctx, "Shop unavailable", "فروشگاه در دسترس نیست", "Магазин недоступен"))
		return
	}
	parts := strings.SplitN(query.Data, ":", 3)
	if len(parts) != 3 {
		_ = b.client.answerCallbackQuery(ctx, settings, query.ID, "Invalid action")
		return
	}
	switch parts[1] {
	case "buy":
		productID, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil || productID <= 0 {
			_ = b.client.answerCallbackQuery(ctx, settings, query.ID, "Invalid plan")
			return
		}
		// Telegram may redeliver the callback after a process restart. Its ID
		// is stable for that update; hash it to keep the database key bounded.
		keyHash := sha256.Sum256([]byte(query.ID))
		order, err := b.shop.Place(ctx, chatID, productID, fmt.Sprintf("tg:%x", keyHash[:]))
		if err != nil {
			_ = b.client.answerCallbackQuery(ctx, settings, query.ID, "Order failed")
			b.reply(ctx, settings, chatID, localText(ctx, "Could not create the order. Please try again.", "سفارش ساخته نشد. دوباره تلاش کنید.", "Не удалось создать заказ. Попробуйте ещё раз."), nil)
			return
		}
		_ = b.client.answerCallbackQuery(ctx, settings, query.ID, "Order created")
		keyboard := &InlineKeyboard{InlineKeyboard: [][]InlineButton{{{Text: localText(ctx, "Pay from wallet", "پرداخت از کیف پول", "Оплатить из кошелька"), CallbackData: "shop:pay:" + order.ID}}}}
		b.reply(ctx, settings, chatID, fmt.Sprintf(localText(ctx, "Order <code>%s</code> is ready. Price: <b>%d toman</b>. Confirm payment from your wallet.", "سفارش <code>%s</code> آماده است. قیمت: <b>%d تومان</b>. پرداخت از کیف پول را تأیید کنید.", "Заказ <code>%s</code> готов. Цена: <b>%d томан</b>. Подтвердите оплату из кошелька."), escape(order.ID), order.PriceToman), keyboard)
	case "pay":
		if len(parts[2]) != 36 {
			_ = b.client.answerCallbackQuery(ctx, settings, query.ID, "Invalid order")
			return
		}
		username, err := b.shop.PayAndProvision(ctx, chatID, parts[2])
		if errors.Is(err, shop.ErrInsufficientFunds) {
			_ = b.client.answerCallbackQuery(ctx, settings, query.ID, "Insufficient balance")
			b.reply(ctx, settings, chatID, localText(ctx, "Your wallet balance is insufficient. Payment was not taken. Use /balance to check it.", "موجودی کیف پول کافی نیست و مبلغی کسر نشد. با /balance موجودی را ببینید.", "Недостаточно средств. Оплата не списана. Проверьте баланс командой /balance."), nil)
			return
		}
		if err != nil {
			_ = b.client.answerCallbackQuery(ctx, settings, query.ID, "Delivery pending")
			b.reply(ctx, settings, chatID, localText(ctx, "The order could not be delivered yet. If payment was taken, it is recorded once. Use /orders to retry delivery; do not buy the plan again.", "سرویس هنوز تحویل نشد. اگر مبلغ کسر شده، فقط یک‌بار ثبت شده است. از /orders تحویل همین سفارش را دوباره امتحان کنید و طرح را دوباره نخرید.", "Услуга пока не выдана. Если оплата списана, она учтена один раз. Повторите выдачу этого заказа через /orders; не покупайте тариф снова."), nil)
			b.logf("telegram shop: order delivery %s: %v", parts[2], err)
			return
		}
		_ = b.client.answerCallbackQuery(ctx, settings, query.ID, "Service ready")
		b.reply(ctx, settings, chatID, localText(ctx, "✅ Service <code>", "✅ سرویس <code>", "✅ Услуга <code>")+escape(username)+localText(ctx, "</code> is ready. Use /my to see your subscription link.", "</code> آماده است. لینک اشتراک را با /my ببینید.", "</code> готова. Ссылка подписки доступна через /my."), nil)
	default:
		_ = b.client.answerCallbackQuery(ctx, settings, query.ID, "Invalid action")
	}
}
