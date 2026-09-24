package nodeagent

import (
	"fmt"
	"strings"
)

func nativeSpeedIsPPPProtocol(protocol string) bool {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "l2tp", "pptp":
		return true
	default:
		return false
	}
}

func nativeSpeedUsersHaveLimits(
	users []openVPNRuntimeUser,
) bool {
	for _, user := range users {
		if user.UploadSpeedLimit > 0 ||
			user.DownloadSpeedLimit > 0 {
			return true
		}
	}

	return false
}

func nativeSpeedHandlePPPSessionEvent(
	protocol string,
	eventName string,
	interfaceName string,
	address string,
	userID int64,
	policy nativeSessionUserPolicy,
) (bool, error) {
	if !nativeSpeedIsPPPProtocol(protocol) {
		return false, nil
	}

	interfaceName = strings.TrimSpace(interfaceName)
	eventName = strings.ToLower(strings.TrimSpace(eventName))

	switch eventName {
	case "stop":
		if interfaceName != "" {
			nativeSpeedClearInterface(interfaceName)
		}

		return false, nil

	case "start":
	default:
		return false, nil
	}

	if policy.UploadSpeedLimit <= 0 &&
		policy.DownloadSpeedLimit <= 0 {
		return false, nil
	}

	if interfaceName == "" {
		return false, fmt.Errorf(
			"%s speed limit interface is missing",
			protocol,
		)
	}

	address = strings.TrimSpace(address)
	if address == "" {
		return false, fmt.Errorf(
			"%s speed limit remote address is missing",
			protocol,
		)
	}

	nativeSpeedClearInterface(interfaceName)

	if err := nativeSpeedAttachIPv4(
		interfaceName,
		address,
		userID,
		policy.UploadSpeedLimit,
		policy.DownloadSpeedLimit,
	); err != nil {
		return false, err
	}

	return true, nil
}
