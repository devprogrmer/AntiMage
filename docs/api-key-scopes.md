# API key scopes

An administrator can create a key with `scope: "users_read"` from My Account or
`POST /api/myaccount/api-keys`. This key can only make GET requests to the
explicitly reviewed user endpoints: `/api/users`, `/api/users/usage`,
`/api/users/onlines`, `/api/v2/users` and its children, and `/api/user/*`.
The owner's existing admin permissions still apply. All other paths and all
write methods return 403. Newly added paths remain inaccessible until added
to the allowlist in `internal/app/api/middleware.go`.

The default `full` scope preserves the behavior of existing keys and remains
bounded by the owner's admin role. Existing keys receive `full` during the
database migration. To narrow one of those keys, revoke it and create a new
one with `users_read`.
