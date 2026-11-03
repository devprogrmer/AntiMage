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
		b.reply(ctx, settings, chatID, "Shop unavailable.", nil)
		return
	}
	result, err := b.shop.SalesOverview(ctx)
	if err != nil {
		b.reply(ctx, settings, chatID, "Could not load sales report. Please try again.", nil)
		return
	}
	b.reply(ctx, settings, chatID, fmt.Sprintf("📊 <b>AntiMage sales</b>\nOrders: %d\nAwaiting payment: %d\nPaid, awaiting delivery: %d\nDelivered: %d\nRecorded paid value: %d toman",
		result.TotalOrders, result.PendingOrders, result.UnfulfilledOrders, result.CompletedOrders, result.PaidToman), nil)
}

func (b *Bot) handleAdminWallet(ctx context.Context, settings Settings, chatID int64, arg string) {
	owner, err := strconv.ParseInt(strings.TrimSpace(arg), 10, 64)
	if err != nil || owner <= 0 {
		b.reply(ctx, settings, chatID, "Usage: /wallet &lt;telegram_id&gt;", nil)
		return
	}
	b.handleWalletHistory(ctx, settings, chatID, owner)
}

func (b *Bot) handleWalletHistory(ctx context.Context, settings Settings, chatID, telegramID int64) {
	if b.shop == nil {
		b.reply(ctx, settings, chatID, "Wallet unavailable.", nil)
		return
	}
	balance, err := b.shop.Balance(ctx, telegramID)
	if err != nil {
		b.reply(ctx, settings, chatID, "Could not load wallet balance.", nil)
		return
	}
	entries, err := b.shop.History(ctx, telegramID)
	if err != nil {
		b.reply(ctx, settings, chatID, "Could not load wallet history.", nil)
		return
	}
	lines := []string{fmt.Sprintf("💳 <b>Wallet %d</b> · %d toman", telegramID, balance)}
	if len(entries) == 0 {
		lines = append(lines, "No transactions recorded.")
	}
	for _, entry := range entries {
		lines = append(lines, fmt.Sprintf("%s · %+d toman · %s · <code>%s</code>", escape(entry.CreatedAt), entry.Delta, escape(entry.Reason), escape(entry.Reference)))
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
			b.reply(ctx, settings, chatID, "Usage: /credit &lt;telegram_id&gt; &lt;toman&gt; &lt;unique_reference&gt;", nil)
			return
		}
		owner, ownerErr := strconv.ParseInt(fields[0], 10, 64)
		amount, amountErr := strconv.ParseInt(fields[1], 10, 64)
		if ownerErr != nil || amountErr != nil || owner <= 0 || amount <= 0 {
			b.reply(ctx, settings, chatID, "Telegram ID and amount must be positive integers.", nil)
			return
		}
		balance, err := b.shop.AdminCredit(ctx, owner, amount, "admin:"+fields[2])
		if err != nil {
			b.reply(ctx, settings, chatID, "Credit failed: "+escape(err.Error()), nil)
			return
		}
		b.reply(ctx, settings, chatID, fmt.Sprintf("Credit reference processed once. Wallet %d balance: %d toman.", owner, balance), nil)
	case "/plan-add":
		if len(fields) < 5 || msg.MessageID <= 0 {
			b.reply(ctx, settings, chatID, "Usage: /plan-add &lt;service_id&gt; &lt;toman&gt; &lt;GB&gt; &lt;days&gt; &lt;title&gt;", nil)
			return
		}
		serviceID, e1 := strconv.ParseInt(fields[0], 10, 64)
		price, e2 := strconv.ParseInt(fields[1], 10, 64)
		gb, e3 := strconv.ParseInt(fields[2], 10, 64)
		days, e4 := strconv.ParseInt(fields[3], 10, 64)
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil || serviceID <= 0 || price <= 0 || gb <= 0 || gb > math.MaxInt64/(1<<30) || days <= 0 {
			b.reply(ctx, settings, chatID, "Invalid service, price, GB, or duration.", nil)
			return
		}
		product, err := b.shop.AddProduct(ctx, shop.Product{ServiceID: serviceID, Title: strings.Join(fields[4:], " "), PriceToman: price, DataLimitBytes: gb * (1 << 30), DurationDays: days}, fmt.Sprintf("tg-plan:%d:%d", chatID, msg.MessageID))
		if err != nil {
			b.reply(ctx, settings, chatID, "Plan creation failed: "+escape(err.Error()), nil)
			return
		}
		b.reply(ctx, settings, chatID, fmt.Sprintf("Plan #%d created for service #%d. Use /shop to view it.", product.ID, product.ServiceID), nil)
	case "/plan-disable":
		if len(fields) != 1 {
			b.reply(ctx, settings, chatID, "Usage: /plan-disable &lt;product_id&gt;", nil)
			return
		}
		productID, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil || productID <= 0 {
			b.reply(ctx, settings, chatID, "Invalid product ID.", nil)
			return
		}
		if err := b.shop.DisableProduct(ctx, productID); err != nil {
			b.reply(ctx, settings, chatID, "Plan disable failed: "+escape(err.Error()), nil)
			return
		}
		b.reply(ctx, settings, chatID, fmt.Sprintf("Plan #%d disabled.", productID), nil)
	}
}

func (b *Bot) handleShopCommand(ctx context.Context, settings Settings, chatID, telegramID int64, command string) {
	if b.shop == nil {
		b.reply(ctx, settings, chatID, "The shop is not available yet.", nil)
		return
	}
	switch command {
	case "/balance":
		balance, err := b.shop.Balance(ctx, telegramID)
		if err != nil {
			b.reply(ctx, settings, chatID, "Could not load your wallet. Please try again.", nil)
			return
		}
		b.reply(ctx, settings, chatID, fmt.Sprintf("💳 Wallet balance: <b>%d toman</b>", balance), nil)
	case "/transactions":
		b.handleWalletHistory(ctx, settings, chatID, telegramID)
	case "/shop":
		products, err := b.shop.Products(ctx)
		if err != nil {
			b.reply(ctx, settings, chatID, "Could not load plans. Please try again.", nil)
			return
		}
		if len(products) == 0 {
			b.reply(ctx, settings, chatID, "No plans are currently available.", nil)
			return
		}
		for _, product := range products {
			label := fmt.Sprintf("%s · %d toman · %d days · %s", product.Title, product.PriceToman, product.DurationDays, formatBytes(product.DataLimitBytes))
			keyboard := &InlineKeyboard{InlineKeyboard: [][]InlineButton{{{Text: "Create order", CallbackData: fmt.Sprintf("shop:buy:%d", product.ID)}}}}
			b.reply(ctx, settings, chatID, "🛒 "+escape(label), keyboard)
		}
	case "/orders":
		orders, err := b.shop.Orders(ctx, telegramID)
		if err != nil {
			b.reply(ctx, settings, chatID, "Could not load orders. Please try again.", nil)
			return
		}
		if len(orders) == 0 {
			b.reply(ctx, settings, chatID, "You have no orders yet. Use /shop to browse plans.", nil)
			return
		}
		for _, order := range orders {
			label := fmt.Sprintf("Order %s\nStatus: %s\nPrice: %d toman", order.ID, order.Status, order.PriceToman)
			var keyboard *InlineKeyboard
			if order.Status == "pending" || order.Status == "paid" || order.Status == "provisioning" {
				keyboard = &InlineKeyboard{InlineKeyboard: [][]InlineButton{{{Text: "Pay / retry delivery", CallbackData: "shop:pay:" + order.ID}}}}
			}
			if order.Username != nil && order.Status == "completed" {
				label += "\nService: " + *order.Username
			}
			b.reply(ctx, settings, chatID, escape(label), keyboard)
		}
	}
}

func (b *Bot) handleShopCallback(ctx context.Context, settings Settings, query *CallbackQuery) {
	chatID := query.From.ID
	if b.shop == nil {
		_ = b.client.answerCallbackQuery(ctx, settings, query.ID, "Shop unavailable")
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
			b.reply(ctx, settings, chatID, "Could not create the order. Please try again.", nil)
			return
		}
		_ = b.client.answerCallbackQuery(ctx, settings, query.ID, "Order created")
		keyboard := &InlineKeyboard{InlineKeyboard: [][]InlineButton{{{Text: "Pay from wallet", CallbackData: "shop:pay:" + order.ID}}}}
		b.reply(ctx, settings, chatID, fmt.Sprintf("Order <code>%s</code> is ready. Price: <b>%d toman</b>. Confirm payment from your wallet.", escape(order.ID), order.PriceToman), keyboard)
	case "pay":
		if len(parts[2]) != 36 {
			_ = b.client.answerCallbackQuery(ctx, settings, query.ID, "Invalid order")
			return
		}
		username, err := b.shop.PayAndProvision(ctx, chatID, parts[2])
		if errors.Is(err, shop.ErrInsufficientFunds) {
			_ = b.client.answerCallbackQuery(ctx, settings, query.ID, "Insufficient balance")
			b.reply(ctx, settings, chatID, "Your wallet balance is insufficient. Payment was not taken. Use /balance to check it.", nil)
			return
		}
		if err != nil {
			_ = b.client.answerCallbackQuery(ctx, settings, query.ID, "Delivery pending")
			b.reply(ctx, settings, chatID, "The order could not be delivered yet. If payment was taken, it is recorded once. Use /orders to retry delivery; do not buy the plan again.", nil)
			b.logf("telegram shop: order delivery %s: %v", parts[2], err)
			return
		}
		_ = b.client.answerCallbackQuery(ctx, settings, query.ID, "Service ready")
		b.reply(ctx, settings, chatID, "✅ Service <code>"+escape(username)+"</code> is ready. Use /my to see your subscription link.", nil)
	default:
		_ = b.client.answerCallbackQuery(ctx, settings, query.ID, "Invalid action")
	}
}
