# Telegram shop extensions implementation plan

**Goal:** Deliver paid renewal and traffic add-ons to the customer's existing account with crash-safe fulfillment.

**Architecture:** Keep immutable product snapshots in shop_orders; attach a separate typed action and target account in the same transaction. Payment retains the existing wallet journal. Fulfillment updates the account through the existing permission, capacity and node-outbox path and commits order completion in the same transaction. Recovery dispatches both new accounts and extensions.

**Tech stack:** Go, database/sql, SQLite/MySQL, Telegram Bot API, embedded Mini App.

- [x] Add failing shop tests for ownership, intent replay and fulfillment rollback; implement order action storage and transaction callback.
- [x] Add migration 90 and user extension calculation tests. Preserve consumed traffic, add bought bytes above current usage/limit, extend expiry from max(now, expiry), and keep on-hold accounts on hold. Volume-only orders retain expiry. Reject disabled/deleted, cross-owner/service and unlimited-volume accounts.
- [x] Reuse user mutation validation and queue insertion within the fulfillment transaction. Test failures before completion and retry after restart.
- [x] Connect order creation/payment/recovery, bot commands/buttons and Mini App in English, Persian and Russian. Verify authenticated cross-owner rejection.
- [x] Add a configurable payment gateway with provider verification, immutable amount/currency and duplicate-callback tests; keep manual receipts. Official sandbox roundtrip verified (reference 26560201); real-money merchant activation remains an operator configuration step.
- [ ] Run affected unit/integration/build checks, review the final diff, commit and push. Merge only after the full requested scope and checks are complete.
