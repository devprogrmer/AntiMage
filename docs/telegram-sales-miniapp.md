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

## Renew an existing account or buy more traffic

1. Open **My services → Renew / add data** in the bot, or the plan selector on the account's Mini App card.
2. Only products for that account's service are offered. Review the plan price, traffic and duration. Both actions use the displayed product price; there is no hidden per-GB conversion.
3. **Renew** preserves remaining traffic and adds the purchased traffic. It extends a dated account from the later of its current expiry and now. An account awaiting its first connection stays on hold and gets a longer activation duration. An unlimited-duration account keeps unlimited duration.
4. **Add data only** preserves expiry. Expired accounts must be renewed first. Disabled, deleted and unlimited-traffic accounts cannot receive these extensions.
5. Confirm the order and pay it from the wallet. The existing username, credentials, subscription, consumed traffic and accounting history stay intact. If usage has already exceeded the previous limit, purchased traffic starts above the current usage.
6. If delivery needs a retry, use the same order. Account changes, node outbox entries and order completion commit together. A process restart cannot apply the same extension twice.

## Configure online wallet payments (Zarinpal)

The gateway is optional. Manual transfer and receipt approval remain available. Configure these server-side environment values in the panel's `.env`, using the merchant ID from your own Zarinpal account:

```dotenv
ANTIMAGE_SHOP_ZARINPAL_MERCHANT_ID=YOUR-MERCHANT-UUID
ANTIMAGE_SHOP_PAYMENT_CALLBACK_URL=https://YOUR-PANEL/telegram-payment-return
ANTIMAGE_SHOP_ZARINPAL_SANDBOX=false
```

Load the configuration using your normal panel update/restart procedure. The callback must use public HTTPS and the exact `/telegram-payment-return` path. Do not paste merchant credentials or the bot token into a chat. For an isolated test panel, set sandbox to `true` and use a test UUID. Sandbox credits are test funds: never enable sandbox on a panel selling real services.

Customers can use `/payonline <toman>`, **Online payments**, or the Mini App's **Pay online** button beside the top-up amount. They pay at Zarinpal, return to Telegram, and use **Check payment** or `/payment <payment_id>` if the return page was interrupted. After the wallet is credited, they pay their existing service order from **Orders**. `/payments` shows their online payment history.

Amounts displayed and stored by AntiMage are integer toman. The gateway receives integer rial (exactly ten times the stored amount) with `currency=IRR`. A callback's `Status=OK` is never sufficient to credit a wallet: AntiMage verifies the saved authority and saved amount with Zarinpal. Provider responses `100` and `101` are accepted only with a positive payment reference. Unique authority, provider reference and wallet journal entries prevent duplicate credit. A verified payment interrupted during settlement is retried by the recovery worker; an awaiting payment can be checked again by its owner.

The implementation follows [Zarinpal's connection guide](https://www.zarinpal.com/docs/paymentGateway/connectToGateway), [currency guide](https://www.zarinpal.com/docs/paymentGateway/moreFeatures/currency), and [sandbox guide](https://www.zarinpal.com/docs/paymentGateway/sandBox). Migration 90 adds extension orders; migration 91 adds durable gateway payments. Back up the database before upgrading. Do not drop these tables when rolling back binaries: retained payment journals are required for reconciliation.

## Mini App endpoints

All endpoints below require signed Telegram initData and enforce account ownership. Do not use a panel admin token in place of initData.

| Endpoint | Request | Result |
| --- | --- | --- |
| `POST /api/telegram-app/orders` | `product_id`, UUID `request_key`; optional `action` (`renew` or `volume`) and `username` | Immutable pending order |
| `POST /api/telegram-app/pay` | `order_id` | Pay once and create/extend the account |
| `POST /api/telegram-app/gateway` | `amount_toman`, UUID `request_key` | Checkout URL for that customer |
| `POST /api/telegram-app/gateway/verify` | `payment_id` | Provider verification and idempotent wallet credit |
| `GET /api/telegram-app/state` | None | Owner-scoped services, orders, wallet, receipts, tickets and online payments |

`GET /telegram-payment-return` is the public provider return route. It accepts no amount or customer identity from the browser. It cannot authorize panel or bot operations.

## Security and operations

- Every Mini App API request sends raw `Telegram.WebApp.initData`. The panel validates its HMAC with the currently configured bot token, rejects missing or duplicate fields, and rejects data older than 24 hours. Browser `initDataUnsafe` is never trusted for authorization.
- Mini App orders, wallet, top-ups, support and linked services are scoped to the verified Telegram ID. Admin privileges are not inherited by the Mini App.
- `/api/telegram-app/orders` and `/api/telegram-app/pay` use the same wallet and provisioning services as the bot. The API does not credit wallets from an unapproved receipt.
- A bot token change invalidates previously issued Mini App initData. Send `/start` again after changing the token.
- The bot requires a private chat for account and admin actions. Notification groups do not grant access.
