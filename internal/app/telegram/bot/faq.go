package bot

import "context"

// faqText describes only flows the native bot actually supports. It avoids
// promising instant bank-transfer approval or an automatic gateway payment.
func faqText(ctx context.Context) string {
	return localText(ctx,
		"❓ <b>AntiMage help</b>\n\n<b>How do I buy a service?</b>\nOpen /shop, choose a plan, create an order, then confirm payment from your wallet. Your order remains in /orders if delivery needs a retry.\n\n<b>How do I add wallet funds?</b>\nUse /topup &lt;amount&gt;. Follow the provider's payment instructions and send a receipt photo to the bot with caption /receipt &lt;request_id&gt;. Funds appear only after an administrator approves the receipt.\n\n<b>Where is my subscription?</b>\nUse /my. If no service appears, ask your provider to link your Telegram ID to the AntiMage account.\n\n<b>How do I get help?</b>\nSend /support &lt;message&gt;, then read replies with /tickets or /ticket &lt;id&gt;.",
		"❓ <b>راهنمای AntiMage</b>\n\n<b>چطور سرویس بخرم؟</b>\nبا /shop طرح را انتخاب و سفارش ایجاد کنید، سپس پرداخت از کیف پول را تأیید کنید. اگر تحویل نیاز به تلاش دوباره داشت، همان سفارش در /orders می‌ماند.\n\n<b>چطور کیف پول را شارژ کنم؟</b>\n/topup &lt;مبلغ&gt; را بزنید. طبق اطلاعات پرداخت فروشنده واریز کنید و عکس رسید را با عنوان /receipt &lt;شناسهٔ درخواست&gt; به ربات بفرستید. موجودی فقط پس از تأیید مدیر اضافه می‌شود.\n\n<b>لینک اشتراکم کجاست؟</b>\n/my را بزنید. اگر سرویسی نمی‌بینید، از فروشنده بخواهید شناسهٔ تلگرام شما را به کاربر AntiMage وصل کند.\n\n<b>چطور پشتیبانی بگیرم؟</b>\n/support &lt;پیام&gt; را بفرستید و پاسخ‌ها را با /tickets یا /ticket &lt;شناسه&gt; ببینید.",
		"❓ <b>Помощь AntiMage</b>\n\n<b>Как купить услугу?</b>\nОткройте /shop, выберите тариф, создайте заказ и подтвердите оплату из кошелька. Если выдачу нужно повторить, заказ останется в /orders.\n\n<b>Как пополнить кошелёк?</b>\nОтправьте /topup &lt;сумма&gt;. Следуйте инструкциям продавца и отправьте фото чека боту с подписью /receipt &lt;ID запроса&gt;. Средства появятся только после одобрения администратором.\n\n<b>Где моя подписка?</b>\nОтправьте /my. Если услуга не видна, попросите продавца привязать ваш Telegram ID к аккаунту AntiMage.\n\n<b>Как обратиться в поддержку?</b>\nОтправьте /support &lt;сообщение&gt;, затем смотрите ответы через /tickets или /ticket &lt;ID&gt;.",
	)
}
