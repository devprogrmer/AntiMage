# AntiMage Telegram bot and Mini App

The Telegram bot runs inside the AntiMage panel process. It starts polling when Telegram is enabled and a bot token is saved. No separate external application is required.

## Set up

1. Serve the panel over a **public HTTPS** address. Confirm that `https://YOUR-PANEL/telegram-app` opens. The page cannot authorize a user when opened outside Telegram.
2. Create a bot with BotFather. Save its token in **Panel settings → Telegram**. Do not place the token in a command, documentation, or chat message.
3. In the same settings form, set **Telegram Mini App URL** to the exact public HTTPS address ending in `/telegram-app`, then save. If the URL is absent, the bot continues to work but does not show a Mini App button.
4. For admin commands, enter the admin's numeric Telegram ID in the allowed admin chat IDs and in that active full-access admin's AntiMage profile. Customer access is separate: a customer sees only accounts linked to their Telegram ID.
5. Open the bot's private chat and send `/start`. The menu and Mini App button appear. Use `/lang fa`, `/lang en`, or `/lang ru` to select the bot language. `/faq` explains the supported purchase, receipt, subscription, and support steps. The Mini App also has its own language selector.

## Sales flow

1. An admin creates a product with `/plan-add <service_id> <toman> <GB> <days> <title>` and configures bank-transfer instructions with `/payment-details <text>`.
2. A customer opens **Plans** in the bot or Mini App and creates an order. Repeating the same request does not create a second order.
3. The customer pays from the wallet. If delivery fails after payment, the order remains recoverable; retry delivery from **Orders** instead of buying again. Wallet debits use a unique order reference.
4. To fund the wallet manually, the customer requests a top-up, transfers the amount, and sends a receipt photo to the bot with caption `/receipt <topup_id>`. The Mini App can create and display the top-up request; receipt photo upload and admin approval happen in the bot. The wallet is credited only after admin approval.
5. Customer support requests can be created and read from the bot or Mini App. Customers can reply to their own open tickets in the Mini App. Admin replies are made through the bot.

## Security and operations

- Every Mini App API request sends raw `Telegram.WebApp.initData`. The panel validates its HMAC with the currently configured bot token, rejects missing or duplicate fields, and rejects data older than 24 hours. Browser `initDataUnsafe` is never trusted for authorization.
- Mini App orders, wallet, top-ups, support and linked services are scoped to the verified Telegram ID. Admin privileges are not inherited by the Mini App.
- `/api/telegram-app/orders` and `/api/telegram-app/pay` use the same wallet and provisioning services as the bot. The API does not credit wallets from an unapproved receipt.
- A bot token change invalidates previously issued Mini App initData. Send `/start` again after changing the token.
- The bot requires a private chat for account and admin actions. Notification groups do not grant access.
