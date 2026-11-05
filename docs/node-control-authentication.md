# Node control authentication

The node control listener requires and verifies a client certificate. The peer's
leaf certificate must match an explicit trusted pin, and normal TLS verification
also checks its validity period, signature and client-auth usage. A certificate
with the same name or signed by an allowed certificate is not an automatic grant.
There is no first-client enrollment or unauthenticated fallback.

## Existing binary and Docker install bundles

The panel currently presents the node-specific certificate and private key from
the node install bundle. The node pins that same certificate by default, so a
standard existing bundle needs no new secret or database migration. Do not
replace a bundle just to update the binary. Keep the control port reachable only
from the intended Master in addition to certificate authentication.

The panel separately pins the node's server certificate. These two checks have
different purposes; verifying the server alone does not authenticate the Master.

## Explicit Master identity

Trust file selection, in order:

1. `ANTIMAGE_NODE_MASTER_CERT_FILE` when configured.
2. `SSL_CLIENT_CERT_FILE` for existing installations that set it.
3. `SSL_CERT_FILE`, the node's install-bundle certificate.

The file contains PEM leaf certificates only, at most 1 MiB. A missing, empty or
malformed explicit file refuses startup before the control listener is opened;
it never falls back to accepting other clients. Paths are on the node host.
Changing the trust file takes effect on the next normal node service start.

Expired certificates and certificates not yet valid are rejected even if pinned.
Before rollout, confirm that the current install bundle is valid and that the
panel presents the corresponding identity. Do not enable insecure verification
to work around an expired bundle; rotate the identity in a controlled rollout.

## Rotation

For a deliberately separate Master identity, concatenate the current and new
trusted leaf certificates in the explicit trust file. Roll out that overlap,
switch the Master's client identity, verify control connectivity and accounting
delivery, then remove the retired pin and roll out the reduced trust file.
Changing a shared server/client install bundle also changes the server pin held
by the panel and must be coordinated at both ends. Merely allowing the new
client pin does not change the panel's server pin or its client private key.

Never paste private keys or full installation bundles into an issue, chat or log.

## Regression coverage

Tests cover the real node listener with the current bundle, a foreign self-signed
certificate and no certificate. TLS 1.2 and 1.3 handshake tests additionally
cover separate Master pins, overlapping rotation, retired identities, the same
name with different credentials, expired/future validity and server-only usage.
These tests use temporary local identities and never contact Production.
