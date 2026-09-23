package nodeagent

import (
	"fmt"
	"sort"
	"strings"
)

type nativeSpeedAttachment struct {
	InterfaceName string `json:"interface_name"`
	Address       string `json:"address"`
	UserID        int64  `json:"user_id"`
	UploadRate    int64  `json:"upload_rate"`
	DownloadRate  int64  `json:"download_rate"`
}

func nativeStaticSpeedAttachments(
	wgPrepared []preparedWireGuardRuntime,
	awgPrepared []preparedAmneziaWGRuntime,
	ovInbounds []openVPNRuntimeInbound,
) ([]nativeSpeedAttachment, []string, error) {
	attachments := []nativeSpeedAttachment{}
	interfaceSet := map[string]struct{}{}

	add := func(
		interfaceName string,
		address string,
		userID int64,
		uploadRate int64,
		downloadRate int64,
	) error {
		interfaceName = strings.TrimSpace(interfaceName)
		if interfaceName == "" {
			return fmt.Errorf("speed limit interface is empty")
		}

		interfaceSet[interfaceName] = struct{}{}

		if uploadRate <= 0 && downloadRate <= 0 {
			return nil
		}

		if userID <= 0 {
			return fmt.Errorf(
				"speed limit interface %s has invalid user id %d",
				interfaceName,
				userID,
			)
		}

		attachments = append(
			attachments,
			nativeSpeedAttachment{
				InterfaceName: interfaceName,
				Address:       strings.TrimSpace(address),
				UserID:        userID,
				UploadRate:    uploadRate,
				DownloadRate:  downloadRate,
			},
		)

		return nil
	}

	for _, runtime := range wgPrepared {
		interfaceSet[runtime.InterfaceName] = struct{}{}

		for _, peer := range runtime.Inbound.Peers {
			if err := add(
				runtime.InterfaceName,
				peer.Address,
				peer.UserID,
				peer.UploadSpeedLimit,
				peer.DownloadSpeedLimit,
			); err != nil {
				return nil, nil, err
			}
		}
	}

	for _, runtime := range awgPrepared {
		interfaceSet[runtime.InterfaceName] = struct{}{}

		for _, peer := range runtime.Inbound.Peers {
			if err := add(
				runtime.InterfaceName,
				peer.Address,
				peer.UserID,
				peer.UploadSpeedLimit,
				peer.DownloadSpeedLimit,
			); err != nil {
				return nil, nil, err
			}
		}
	}

	for _, inbound := range ovInbounds {
		interfaceName := openVPNTunName(
			strings.TrimSpace(inbound.Tag),
		)

		interfaceSet[interfaceName] = struct{}{}

		for _, user := range inbound.Users {
			if err := add(
				interfaceName,
				user.IPv4Address,
				user.UserID,
				user.UploadSpeedLimit,
				user.DownloadSpeedLimit,
			); err != nil {
				return nil, nil, err
			}
		}
	}

	interfaces := make([]string, 0, len(interfaceSet))
	for interfaceName := range interfaceSet {
		interfaceName = strings.TrimSpace(interfaceName)

		if interfaceName != "" {
			interfaces = append(
				interfaces,
				interfaceName,
			)
		}
	}

	sort.Strings(interfaces)

	sort.SliceStable(
		attachments,
		func(i int, j int) bool {
			if attachments[i].InterfaceName !=
				attachments[j].InterfaceName {
				return attachments[i].InterfaceName <
					attachments[j].InterfaceName
			}

			if attachments[i].UserID !=
				attachments[j].UserID {
				return attachments[i].UserID <
					attachments[j].UserID
			}

			return attachments[i].Address <
				attachments[j].Address
		},
	)

	return attachments, interfaces, nil
}

func applyNativeSpeedAttachments(
	attachments []nativeSpeedAttachment,
) error {
	for _, item := range attachments {
		if err := nativeSpeedAttachIPv4(
			item.InterfaceName,
			item.Address,
			item.UserID,
			item.UploadRate,
			item.DownloadRate,
		); err != nil {
			return fmt.Errorf(
				"apply speed limit user=%d interface=%s address=%s: %w",
				item.UserID,
				item.InterfaceName,
				item.Address,
				err,
			)
		}
	}

	return nil
}

func clearNativeSpeedInterfaces(
	interfaces []string,
) {
	for _, interfaceName := range interfaces {
		nativeSpeedClearInterface(interfaceName)
	}
}

func rollbackNativeSpeedState(
	previous nativeSpeedLimitState,
	interfaces []string,
) {
	clearNativeSpeedInterfaces(interfaces)

	_ = applyNativeSpeedAttachments(
		previous.Attachments,
	)
}

func (s *Server) reconcileNativeStaticSpeedLimits(
	wgPrepared []preparedWireGuardRuntime,
	awgPrepared []preparedAmneziaWGRuntime,
	ovInbounds []openVPNRuntimeInbound,
	l2tpInbounds []l2TPRuntimeInbound,
	pptpInbounds []pptpRuntimeInbound,
) error {
	if nativeSpeedLimitGOOS != "linux" {
		return nil
	}

	attachments, interfaces, err :=
		nativeStaticSpeedAttachments(
			wgPrepared,
			awgPrepared,
			ovInbounds,
		)
	if err != nil {
		return err
	}

	previous, err := s.loadNativeSpeedLimitState()
	if err != nil {
		return fmt.Errorf(
			"load native speed limit state: %w",
			err,
		)
	}

	desiredActions, err :=
		nativeSpeedActionSet(attachments)
	if err != nil {
		return err
	}

	for _, inbound := range l2tpInbounds {
		for _, user := range inbound.Users {
			if err := nativeSpeedAddDesiredUserActions(
				desiredActions,
				user.UserID,
				user.UploadSpeedLimit,
				user.DownloadSpeedLimit,
			); err != nil {
				return err
			}
		}
	}

	for _, inbound := range pptpInbounds {
		for _, user := range inbound.Users {
			if err := nativeSpeedAddDesiredUserActions(
				desiredActions,
				user.UserID,
				user.UploadSpeedLimit,
				user.DownloadSpeedLimit,
			); err != nil {
				return err
			}
		}
	}

	allInterfaces := nativeSpeedInterfaceUnion(
		previous.Interfaces,
		interfaces,
	)

	clearNativeSpeedInterfaces(allInterfaces)

	if err := applyNativeSpeedAttachments(
		attachments,
	); err != nil {
		rollbackNativeSpeedState(
			previous,
			allInterfaces,
		)

		nativeSpeedDeleteNewActions(desiredActions, previous.Actions)
		return err
	}

	next := nativeSpeedLimitState{
		Interfaces:  interfaces,
		Actions:     nativeSpeedActionSlice(desiredActions),
		Attachments: attachments,
	}

	if err := s.saveNativeSpeedLimitState(next); err != nil {
		rollbackNativeSpeedState(
			previous,
			allInterfaces,
		)

		_ = s.saveNativeSpeedLimitState(previous)

		nativeSpeedDeleteNewActions(desiredActions, previous.Actions)
		return fmt.Errorf(
			"save native speed limit state: %w",
			err,
		)
	}

	for _, index := range previous.Actions {
		if _, keep := desiredActions[index]; keep {
			continue
		}

		nativeSpeedDeleteActionIndex(index)
	}

	if len(attachments) > 0 {
		s.appendLog(
			fmt.Sprintf(
				"native static speed limits reconciled: attachments=%d interfaces=%d actions=%d",
				len(attachments),
				len(interfaces),
				len(desiredActions),
			),
		)
	}

	return nil
}
