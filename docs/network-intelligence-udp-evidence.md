# UDP evidence and application detection

Local development change, not pushed or released. All existing feature flags
remain off by default. No network configuration, TC rule, accounting operation,
database schema or production service is changed by this implementation.

## Supported observations

| Evidence | Classification | Timing |
| --- | --- | --- |
| STUN Binding headers, cookie, transaction ID and opposite-direction reply | STUN connectivity checks; no executable/game inference | Matched transaction RTT |
| Valve direct GameNetworkingSockets challenge/reply, connection ID and echoed timestamp | GameNetworkingSockets **direct UDP**, not Steam Datagram Relay or a specific game | Matched challenge RTT |
| Discord Voice IP Discovery request/reply and SSRC | Discord Voice discovery; no proof of ongoing voice activity | First unambiguous discovery exchange RTT |
| Complete unsplit Source server query/reply and consistent full plain GameID | Source server query; versioned catalogue can identify CS2/Dota 2 **server query** | Unavailable: no echoed unique request token |
| Arbitrary UDP, shared destination port, incomplete reply, encrypted unknown protocol | Unknown | N/A |

All classifications are probable observations, not authenticated proof of the
client executable. Source server advertisements can be spoofed. Browsing a
server is not proof of playing. STUN is used by many applications. Direct GNS
must not be presented as SDR. A destination such as 162.254.199.170:27051 is not
sufficient evidence of a particular application.

RTT is measured at the node observation point and includes remote processing.
Upload exchange means a client-originated request and destination reply;
download exchange means the opposite. Jitter is variation between matched
transaction RTT samples, not RTP transit jitter or in-game latency. Packet loss
is always null/N/A for these protocols. Missing transactions cannot establish
network loss. No ongoing timing is invented after a handshake/discovery ends.

## Bounds and lifecycle

The existing 128-byte socket snapshot remains unchanged. The parser reads only
the available snapshot. Long, split, compressed or incomplete Source replies
remain Unknown. IPv6/options can further reduce available protocol header space.
No application strings, discovered IPs, voice bodies, Steam IDs or packet bytes
are retained. Correlation IDs exist only in session-local bounded memory and
never enter telemetry, history, logs or API output.

Each capture has at most min(flow capacity,128) protocol states, eight pending
transactions and 32 reuse guards per state, plus two 32-sample timing windows.
Requests expire after ten seconds. Evidence and timing expire after one minute.
Retries invalidate timing; duplicates, wrong directions, unmatched IDs and
expired replies do not add samples. Stable Discord SSRC reuse is rejected for
the current generation. Gaps, reordered observations, session replacement and
Stop clear correlation. Capture/flow capacity may suppress new observations;
unsupported/suppressed measurements remain unavailable. No extra worker,
goroutine, active probe or forwarding operation is introduced.

## Catalogue compatibility

The existing bounded application-signature loader supports both v1 destination
rules and v2 query AppID rules. The embedded v2 catalogue maps official Steam
AppIDs 730 and 570, expires on 2027-02-26, and is loaded only when monitoring is
enabled and no custom signature path is configured. An invalid explicit custom
file never silently enables the built-in catalogue. Expiry disables title
matches; fresh protocol-only evidence can still be shown. Conflicting rules
return Unknown. Evidence strings and the number of returned entries are bounded.

To extend the database use the existing administrator signature file setting.
A v2 query rule contains `application`, `steam_app_id`, `protocol: "udp"` and a
verifiable `source`; it must omit destination prefixes and ports. All existing
database size, rule count, validity and source/name limits apply. Catalogue
updates are explicit reviewed artifacts, not automatic remote downloads.

Sources: [STUN RFC8489](https://www.rfc-editor.org/rfc/rfc8489.html),
[Valve UDP challenge schema](https://github.com/ValveSoftware/GameNetworkingSockets/blob/master/src/common/steamnetworkingsockets_messages_udp.proto),
[Valve UDP framing](https://github.com/ValveSoftware/GameNetworkingSockets/blob/master/src/steamnetworkingsockets/clientlib/steamnetworkingsockets_udp.h),
[Discord Voice IP Discovery](https://docs.discord.com/developers/topics/voice-connections#ip-discovery),
[Source query implementation](https://github.com/rumblefrog/go-a2s/blob/master/info.go),
[CS2](https://store.steampowered.com/app/730/CounterStrike_2/),
[Dota 2](https://store.steampowered.com/app/570/Dota_2/).

## Validation boundary

Local socket tests exercise actual loopback datagrams, then the decoder and
session adapter; they are not a WireGuard/raw-socket E2E claim. The Linux-only
`TestCaptureUDPProtocolRealExchange` exercises the real capture parser and
kernel timestamps and requires `ANTIMAGE_CAPTURE_E2E=1` and the disposable guest
marker. Compile success does not establish its execution result. Real game
clients, encrypted SDR traffic, privileged Linux capture, the new CPU/RAM cost
under forwarding traffic and release acceptance still require isolated runtime
validation. Existing ACK history remains explicitly TCP ACK history; UDP
transaction timing is not relabelled or persisted as TCP ACK data.
