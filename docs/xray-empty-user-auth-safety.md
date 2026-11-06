# Xray empty-user authentication safety

Panel-managed Shadowsocks 2022 and Hysteria listeners must not remain in the
active core configuration with zero authorized users. Their definitions remain
in the panel; the controller includes the listener again when an eligible user
is assigned to its service.

## Failure reproduced

Using official Xray 26.10.31, a local TCP echo exchange succeeded using only the
Shadowsocks 2022 server key after both managed clients were revoked. Hysteria 2
also accepted its transport's static auth value after its last managed user was
removed. The same failures occurred when a fresh node restored a durable policy
containing users marked limited. These are real authenticated tunnel exchanges,
not assumptions from a successful TCP connection to a proxy port.

Upstream behavior:

- [SS2022 configuration builder](https://github.com/XTLS/Xray-core/blob/v26.10.31/infra/conf/shadowsocks.go):
  an empty user list selects single-user server-key authentication.
- [Hysteria authentication](https://github.com/XTLS/Xray-core/blob/v26.10.31/transport/internet/hysteria/hub.go):
  an empty validator can use the transport's configured static auth value.

## Enforcement

- The controller omits managed SS2022/Hysteria listeners without eligible users.
- Quota filtering removes the entire affected fallback-capable listener when
  its last user is removed. Other users/listeners, API and routing are retained.
- Offline activation uses the same filter. A managed tag already containing an
  empty or missing user list is closed too; credentials cannot be resurrected by
  restarting the node.
- Both `clients` and the SS/Hysteria `users` alias are handled with Xray's
  non-null `clients` precedence. Unmanaged static listeners are preserved.
- Pending usage, ACK/reflection state, accounting intervals and coefficients
  are not changed by this patch.

## Verification

The regression was observed failing before the fix in all four real-core cases
(two protocols, direct quota filtering and fresh-node offline restart). After
the fix the API remains reachable, both the server-only key and the revoked
managed key fail to exchange tunneled data. Existing witness traffic, live
additions, duplicate sync, restart authentication and unchanged pending receipt
checks remain enabled for VLESS, VMess, Trojan, SS AEAD, SS2022 and Hysteria 2.

Unit coverage also checks partial revocation, both user-list aliases, initially
empty managed listeners, missing Hysteria settings, preservation of API/other
listeners and intentional unmanaged static authentication.

```sh
go test ./internal/app/nodeagent ./internal/app/nodecontroller ./internal/app/xrayconfig -count=1
go build ./cmd/...
go vet ./internal/app/nodeagent ./internal/app/nodecontroller ./internal/app/xrayconfig
ANTIMAGE_XRAY_E2E_BINARY=/path/to/xray go test ./internal/app/nodeagent \
  -run '^TestRealXrayLiveUserAdditions$' -count=1 -v
```

Linux native/race CI remains a separate verification gate. Local execution was
on Windows with checksum-verified official Xray; it is not a Production rollout.

## Limits and rollback

The existing quota cutoff still restarts the shared core to terminate active
streams. This patch closes a fallback-auth bypass; it does not introduce
per-user stream termination or transactional rollback of failed core startup.
Adding the first eligible user to an omitted listener requires full activation.

To roll back source, revert this PR's merge commit and run the verification
suite before publishing a replacement build. Reverting restores the fallback
auth exposure; it should not be treated as a safe quota-enforcement workaround.
No accounting files or database migration need to be removed.
