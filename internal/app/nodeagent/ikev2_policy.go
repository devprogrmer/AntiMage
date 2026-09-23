package nodeagent

import (
	"context"
	"fmt"
	"net/netip"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

type ikev2PolicySession struct {
	InboundTag string
	User       ikev2RuntimeUser
	SA         ikev2RawSA
	Delta      uint64
	IPs        []string
}

func ikev2PolicyUsesEAP(
	inbound ikev2RuntimeInbound,
) bool {
	mode := strings.ToLower(strings.TrimSpace(
		openVPNStringSetting(
			inbound.Settings,
			"auth_mode",
			"password",
		),
	))

	return mode == "password" ||
		mode == "password+certificate"
}

func ikev2PolicyFindUser(
	inbound ikev2RuntimeInbound,
	identity string,
) (ikev2RuntimeUser, bool) {
	identity = strings.TrimSpace(identity)

	for _, user := range inbound.Users {
		if strings.TrimSpace(user.Username) ==
			identity {
			return user, true
		}
	}

	return ikev2RuntimeUser{}, false
}

func ikev2PolicyIPv4s(
	values []string,
) []string {
	seen := map[string]struct{}{}
	result := []string{}

	for _, value := range values {
		value = strings.TrimSpace(value)

		var addr netip.Addr

		if parsed, err := netip.ParseAddr(value); err == nil {
			addr = parsed
		} else if prefix, err := netip.ParsePrefix(value); err == nil {
			addr = prefix.Addr()
		} else {
			continue
		}

		if !addr.Is4() {
			continue
		}

		value = addr.String()

		if _, exists := seen[value]; exists {
			continue
		}

		seen[value] = struct{}{}
		result = append(result, value)
	}

	sort.Strings(result)

	return result
}

func (s *Server) ikev2PolicySADelta(
	inboundTag string,
	sa ikev2RawSA,
) uint64 {
	var delta uint64

	for _, child := range sa.Children {
		if !strings.EqualFold(
			strings.TrimSpace(child.State),
			"INSTALLED",
		) {
			continue
		}

		total := ikev2SafeAdd(
			child.BytesIn,
			child.BytesOut,
		)

		key := ikev2UsageBaselineKey(
			inboundTag,
			sa,
			child,
		)

		baseline, exists :=
			s.ikev2UsageBaseline[key]

		delta = ikev2SafeAdd(
			delta,
			ikev2UsageDelta(
				total,
				baseline,
				exists,
			),
		)
	}

	return delta
}

func ikev2PolicyReason(
	user ikev2RuntimeUser,
	now time.Time,
	currentDelta uint64,
) string {
	status := strings.ToLower(
		strings.TrimSpace(user.Status),
	)

	if status == "on_hold" {
		return ""
	}

	if status != "" &&
		status != "active" {
		return "status " + status
	}

	if user.Expire != nil &&
		*user.Expire > 0 &&
		*user.Expire <= now.Unix() {
		return "expired"
	}

	if user.DataLimit != nil &&
		*user.DataLimit > 0 {
		limit := uint64(*user.DataLimit)

		var used uint64
		if user.UsedTraffic > 0 {
			used = uint64(user.UsedTraffic)
		}

		if used >= limit {
			return "data limit reached"
		}

		if currentDelta >= limit-used {
			return "data limit reached"
		}
	}

	return ""
}

func ikev2PolicySAOrder(
	session ikev2PolicySession,
) uint64 {
	value, err := strconv.ParseUint(
		strings.TrimSpace(
			session.SA.UniqueID,
		),
		10,
		64,
	)

	if err != nil {
		return ^uint64(0)
	}

	return value
}

func (s *Server) terminateIKEv2SA(
	ctx context.Context,
	sa ikev2RawSA,
	reason string,
) error {
	ikeID := strings.TrimSpace(sa.UniqueID)

	if ikeID == "" {
		return fmt.Errorf(
			"IKEv2 SA has no unique id",
		)
	}

	path, err := exec.LookPath("swanctl")
	if err != nil {
		return fmt.Errorf(
			"swanctl not installed: %w",
			err,
		)
	}

	commandCtx, cancel :=
		context.WithTimeout(
			ctx,
			10*time.Second,
		)
	defer cancel()

	output, err := exec.CommandContext(
		commandCtx,
		path,
		"--terminate",
		"--ike-id",
		ikeID,
		"--force",
	).CombinedOutput()

	if err != nil {
		detail := strings.TrimSpace(
			string(output),
		)

		if detail == "" {
			detail = err.Error()
		}

		return fmt.Errorf(
			"swanctl terminate ike-id=%s: %s",
			ikeID,
			detail,
		)
	}

	s.appendLog(
		fmt.Sprintf(
			"ikev2 SA terminated: ike_id=%s user_remote=%s reason=%s",
			ikeID,
			strings.TrimSpace(sa.RemoteHost),
			reason,
		),
	)

	return nil
}

func (s *Server) enforceIKEv2Policies(
	ctx context.Context,
	runtimes map[string]ikev2RuntimeInbound,
	sas []ikev2RawSA,
) (map[string]struct{}, error) {
	terminated := map[string]struct{}{}
	sessions := []ikev2PolicySession{}

	for _, sa := range sas {
		if !strings.EqualFold(
			strings.TrimSpace(sa.State),
			"ESTABLISHED",
		) {
			continue
		}

		inbound, exists :=
			runtimes[sa.ConnectionName]

		if !exists {
			continue
		}

		identity :=
			strings.TrimSpace(
				sa.RemoteEAPID,
			)

		if identity == "" {
			identity =
				strings.TrimSpace(
					sa.RemoteID,
				)
		}

		user, found :=
			ikev2PolicyFindUser(
				inbound,
				identity,
			)

		if !found {
			if ikev2PolicyUsesEAP(inbound) {
				if err := s.terminateIKEv2SA(
					ctx,
					sa,
					"unknown or disabled user",
				); err != nil {
					s.appendLog(
						"ikev2 unknown user terminate failed: " +
							err.Error(),
					)
				} else {
					terminated[strings.TrimSpace(
						sa.UniqueID,
					)] = struct{}{}
				}
			}

			continue
		}

		sessions = append(
			sessions,
			ikev2PolicySession{
				InboundTag: strings.TrimSpace(
					inbound.Tag,
				),

				User: user,

				SA: sa,

				Delta: s.ikev2PolicySADelta(
					inbound.Tag,
					sa,
				),

				IPs: ikev2PolicyIPv4s(
					sa.RemoteVIPs,
				),
			},
		)
	}

	byUser := map[int64][]int{}
	userDelta := map[int64]uint64{}

	for index := range sessions {
		userID :=
			sessions[index].User.UserID

		if userID <= 0 {
			continue
		}

		byUser[userID] =
			append(
				byUser[userID],
				index,
			)

		userDelta[userID] =
			ikev2SafeAdd(
				userDelta[userID],
				sessions[index].Delta,
			)
	}

	now := time.Now().UTC()

	for userID, indexes := range byUser {
		if len(indexes) == 0 {
			continue
		}

		user := sessions[indexes[0]].User

		reason := ikev2PolicyReason(
			user,
			now,
			userDelta[userID],
		)

		if reason != "" {
			for _, index := range indexes {
				sa := sessions[index].SA

				if err := s.terminateIKEv2SA(
					ctx,
					sa,
					reason,
				); err != nil {
					s.appendLog(
						"ikev2 policy terminate failed: " +
							err.Error(),
					)
					continue
				}

				terminated[strings.TrimSpace(
					sa.UniqueID,
				)] = struct{}{}
			}

			continue
		}

		if user.DeviceLimit <= 0 ||
			int64(len(indexes)) <=
				user.DeviceLimit {
			continue
		}

		sort.SliceStable(
			indexes,
			func(i int, j int) bool {
				return ikev2PolicySAOrder(
					sessions[indexes[i]],
				) < ikev2PolicySAOrder(
					sessions[indexes[j]],
				)
			},
		)

		for position :=
			int(user.DeviceLimit); position < len(indexes); position++ {
			index := indexes[position]
			sa := sessions[index].SA

			if err := s.terminateIKEv2SA(
				ctx,
				sa,
				"device limit reached",
			); err != nil {
				s.appendLog(
					"ikev2 device limit terminate failed: " +
						err.Error(),
				)
				continue
			}

			terminated[strings.TrimSpace(
				sa.UniqueID,
			)] = struct{}{}
		}
	}

	speedBindings :=
		[]ikev2SpeedBinding{}

	for _, session := range sessions {
		if _, dead := terminated[strings.TrimSpace(
			session.SA.UniqueID,
		)]; dead {
			continue
		}

		for _, ip := range session.IPs {
			speedBindings = append(
				speedBindings,
				ikev2SpeedBinding{
					UserID: session.User.UserID,

					IPv4: ip,

					UploadRate: session.User.
						UploadSpeedLimit,

					DownloadRate: session.User.
						DownloadSpeedLimit,
				},
			)
		}
	}

	if err := reconcileIKEv2SpeedLimits(
		speedBindings,
	); err != nil {
		return terminated, err
	}

	return terminated, nil
}
