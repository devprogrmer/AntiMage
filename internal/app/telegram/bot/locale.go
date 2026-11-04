package bot

import (
	"context"
	"strings"
)

type languageContextKey struct{}

func languageFromContext(ctx context.Context) string {
	if language, ok := ctx.Value(languageContextKey{}).(string); ok {
		return normalizeLanguage(language)
	}
	return "en"
}

func localText(ctx context.Context, en, fa, ru string) string {
	switch languageFromContext(ctx) {
	case "fa":
		return fa
	case "ru":
		return ru
	default:
		return en
	}
}

func normalizeLanguage(raw string) string {
	switch strings.ToLower(strings.SplitN(strings.TrimSpace(raw), "-", 2)[0]) {
	case "fa":
		return "fa"
	case "ru":
		return "ru"
	default:
		return "en"
	}
}

var languageLabels = map[string]map[string]string{
	"fa": {
		"System": "سیستم", "Nodes": "نودها", "Plans": "طرح‌ها", "Sales": "فروش", "Wallet": "کیف پول", "Orders": "سفارش‌ها", "Transactions": "تراکنش‌ها", "My services": "سرویس‌های من", "Support": "پشتیبانی", "Trial": "آزمایشی", "Top-ups": "شارژها", "AntiMage Mini App": "مینی‌اپ AntiMage", "Disable": "غیرفعال‌کردن", "Activate": "فعال‌کردن", "Reset usage": "صفرکردن مصرف", "Revoke sub": "لغو اشتراک", "Links": "لینک‌ها", "Edit note": "ویرایش یادداشت", "Refresh": "به‌روزرسانی", "Delete": "حذف", "Yes, delete": "بله، حذف شود", "Cancel": "انصراف",
	},
	"ru": {
		"System": "Система", "Nodes": "Узлы", "Plans": "Тарифы", "Sales": "Продажи", "Wallet": "Кошелёк", "Orders": "Заказы", "Transactions": "Транзакции", "My services": "Мои услуги", "Support": "Поддержка", "Trial": "Пробный доступ", "Top-ups": "Пополнения", "AntiMage Mini App": "Мини-приложение AntiMage", "Disable": "Отключить", "Activate": "Активировать", "Reset usage": "Сбросить трафик", "Revoke sub": "Отозвать подписку", "Links": "Ссылки", "Edit note": "Изменить заметку", "Refresh": "Обновить", "Delete": "Удалить", "Yes, delete": "Да, удалить", "Cancel": "Отмена",
	},
}

func menuForLanguage(admin bool, language, miniAppURL string) *InlineKeyboard {
	var menu *InlineKeyboard
	if admin {
		menu = mainMenuKeyboard()
	} else {
		menu = customerMenuKeyboard()
	}
	menu = localizeKeyboard(language, menu)
	menu = appendMiniAppButton(menu, miniAppURL)
	labels := languageLabels[normalizeLanguage(language)]
	if len(menu.InlineKeyboard) > 0 && len(menu.InlineKeyboard[len(menu.InlineKeyboard)-1]) == 1 && menu.InlineKeyboard[len(menu.InlineKeyboard)-1][0].WebApp != nil {
		button := &menu.InlineKeyboard[len(menu.InlineKeyboard)-1][0]
		if translated := labels["AntiMage Mini App"]; translated != "" {
			button.Text = "📱 " + translated
		}
	}
	menu.InlineKeyboard = append(menu.InlineKeyboard, []InlineButton{{Text: "🇮🇷 فارسی", CallbackData: "lang:fa"}, {Text: "🇬🇧 English", CallbackData: "lang:en"}, {Text: "🇷🇺 Русский", CallbackData: "lang:ru"}})
	return menu
}

func localizeKeyboard(language string, keyboard *InlineKeyboard) *InlineKeyboard {
	if keyboard == nil {
		return nil
	}
	labels := languageLabels[normalizeLanguage(language)]
	for i := range keyboard.InlineKeyboard {
		for j := range keyboard.InlineKeyboard[i] {
			button := &keyboard.InlineKeyboard[i][j]
			for english, translated := range labels {
				button.Text = strings.ReplaceAll(button.Text, english, translated)
			}
		}
	}
	return keyboard
}

func welcomeForLanguage(language string, admin bool) string {
	switch normalizeLanguage(language) {
	case "fa":
		if admin {
			return "🤖 <b>مدیریت AntiMage</b>\n/user نام‌کاربری · /usage نام‌کاربری · /system · /nodes · /node شناسه\n/sales · /shop · /orders · /balance · /transactions · /my\n/credit شناسه‌تلگرام مبلغ مرجع · /plan-add شناسه‌سرویس تومان گیگ روز عنوان · /plan-disable شناسه‌طرح\n/trial-config شناسه‌سرویس گیگ روز · /trial-off · /wallet شناسه‌تلگرام\n/support متن · /tickets · /ticket شناسه · /reply شناسه متن\n/topup مبلغ · /receipt شناسه · /topups · /pending-topups · /topup-review شناسه · /payment-details متن\n/lang fa|en|ru"
		}
		return "به AntiMage خوش آمدید. از دکمه‌های زیر طرح‌ها، کیف پول، سفارش‌ها، سرویس‌ها و پشتیبانی را باز کنید. برای راهنما /help را بزنید."
	case "ru":
		if admin {
			return "🤖 <b>Управление AntiMage</b>\n/user имя · /usage имя · /system · /nodes · /node ID\n/sales · /shop · /orders · /balance · /transactions · /my\n/credit Telegram_ID сумма ключ · /plan-add ID_услуги томан ГБ дни название · /plan-disable ID_тарифа\n/trial-config ID_услуги ГБ дни · /trial-off · /wallet Telegram_ID\n/support текст · /tickets · /ticket ID · /reply ID текст\n/topup сумма · /receipt ID · /topups · /pending-topups · /topup-review ID · /payment-details текст\n/lang fa|en|ru"
		}
		return "Добро пожаловать в AntiMage. Кнопки ниже откроют тарифы, кошелёк, заказы, услуги и поддержку. Помощь: /help."
	default:
		if admin {
			return helpText
		}
		return "Welcome to AntiMage. Use /shop to view plans, /trial for a one-time trial, /topup to fund your wallet, /balance and /transactions for your wallet, /orders for purchases, /my for services, or /support for help."
	}
}

func languageChangedText(language string) string {
	switch normalizeLanguage(language) {
	case "fa":
		return "زبان ربات به فارسی تغییر کرد."
	case "ru":
		return "Язык бота изменён на русский."
	default:
		return "Bot language changed to English."
	}
}
