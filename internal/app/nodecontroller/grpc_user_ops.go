package nodecontroller

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/antimage/antimage/internal/app/nodeclient"
	userread "github.com/antimage/antimage/internal/app/user"
	"github.com/antimage/antimage/internal/app/xrayconfig"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

func (c Controller) grpcApplyUserOperation(ctx context.Context, client *nodeclient.Client, node NodeRow, operation OperationRow, prepared *preparedRuntimeConfig) error {
	email, err := c.legacyOperationEmail(ctx, operation)
	if err != nil {
		if operation.OperationType == "remove_user" || operation.OperationType == "disable_user" {
			operationID := fmt.Sprintf("%s-missing-user-%d", operation.OperationType, operation.ID)
			var runtimeReq *nodev1.RuntimeConfigRequest
			if prepared != nil && prepared.nodeID == node.ID && strings.TrimSpace(prepared.configJSON) != "" {
				runtimeReq = &nodev1.RuntimeConfigRequest{OperationId: operationID, ConfigJson: prepared.configJSON, OvRuntimeJson: prepared.ovRuntimeJSON, DesiredRevision: uint64(operation.ID)}
			} else {
				configJSON, syncErr := c.buildRuntimeConfig(ctx, node)
				if syncErr != nil {
					return syncErr
				}
				runtimeReq, syncErr = c.runtimeConfigRequest(ctx, node, operationID, configJSON)
				if syncErr != nil {
					return syncErr
				}
			}
			if syncErr := c.prepareRuntimeRevision(ctx, client, node.ID, runtimeReq); syncErr != nil {
				return syncErr
			}
			return c.executeLegacyNodeCommand(legacyConfigEvidence(ctx, runtimeReq.ConfigJson), node.ID, runtimeReq.OperationId, "sync_config", func(worker context.Context, fence *nodev1.DestructiveFence) (*nodev1.RuntimeActionResponse, error) {
				runtimeReq.Fence = fence
				return client.Runtime().SyncConfig(worker, runtimeReq)
			}, func(worker context.Context, res *nodev1.RuntimeActionResponse) error {
				_, finishErr := c.finishRuntime(worker, node, res.GetRuntime(), res.GetMessage())
				return finishErr
			})
		}
		return err
	}
	if operation.UserID.Valid {
		protocols, protocolErr := c.repo.RuntimeMutationProtocolsByUserIDs(ctx, []int64{operation.UserID.Int64})
		if protocolErr != nil {
			return protocolErr
		}
		userProtocols := protocols[operation.UserID.Int64]
		if len(userProtocols) == 1 && strings.EqualFold(strings.TrimSpace(userProtocols[0]), "wireguard") {
			capability := targetedUserCapability("wireguard", operation.OperationType)
			if capability != "" && client.Supports(capability) {
				return c.grpcApplyWireGuardUserOperation(ctx, client, node, operation)
			}
		}
		if len(userProtocols) == 1 && strings.EqualFold(strings.TrimSpace(userProtocols[0]), "amneziawg") {
			capability := targetedUserCapability("amneziawg", operation.OperationType)
			if capability != "" && client.Supports(capability) {
				return c.grpcApplyAmneziaWGUserOperation(ctx, client, node, operation)
			}
		}
		if len(userProtocols) == 1 && strings.EqualFold(strings.TrimSpace(userProtocols[0]), "openvpn") {
			capability := targetedUserCapability("openvpn", operation.OperationType)
			if capability != "" && client.Supports(capability) {
				return c.grpcApplyOpenVPNUserOperation(ctx, client, node, operation)
			}
		}
		if len(userProtocols) == 1 && strings.EqualFold(strings.TrimSpace(userProtocols[0]), "l2tp") {
			capability := targetedUserCapability("l2tp", operation.OperationType)
			if capability != "" && client.Supports(capability) {
				return c.grpcApplyL2TPUserOperation(ctx, client, node, operation)
			}
			return c.grpcApplyLegacyFullSyncForUserOperation(ctx, client, node, operation, prepared)
		}
		if len(userProtocols) == 1 && strings.EqualFold(strings.TrimSpace(userProtocols[0]), "pptp") {
			capability := targetedUserCapability("pptp", operation.OperationType)
			if capability != "" && client.Supports(capability) {
				return c.grpcApplyPPTPUserOperation(ctx, client, node, operation)
			}
			return c.grpcApplyLegacyFullSyncForUserOperation(ctx, client, node, operation, prepared)
		}
	}
	switch operation.OperationType {
	case "remove_user", "disable_user":
		return c.grpcRemoveUserFromNode(ctx, client, node, operation, email)
	case "add_user", "enable_user":
		return c.grpcAddUserToNode(ctx, client, node, operation, email, true)
	case "update_user":
		return c.grpcAddUserToNode(ctx, client, node, operation, email, false)
	default:
		return fmt.Errorf("unsupported runtime user operation: %s", operation.OperationType)
	}
}

func (c Controller) grpcApplyLegacyFullSyncForUserOperation(ctx context.Context, client *nodeclient.Client, node NodeRow, operation OperationRow, prepared *preparedRuntimeConfig) error {
	operationID := fmt.Sprintf("legacy-full-sync-%s-%d", operation.OperationType, operation.ID)
	var request *nodev1.RuntimeConfigRequest
	if prepared != nil && prepared.nodeID == node.ID && strings.TrimSpace(prepared.configJSON) != "" {
		request = &nodev1.RuntimeConfigRequest{
			OperationId: operationID, ConfigJson: prepared.configJSON,
			OvRuntimeJson: prepared.ovRuntimeJSON, DesiredRevision: uint64(operation.ID),
		}
	} else {
		configJSON, err := c.buildRuntimeConfig(ctx, node)
		if err != nil {
			return err
		}
		request, err = c.runtimeConfigRequest(ctx, node, operationID, configJSON)
		if err != nil {
			return err
		}
	}
	if err := c.prepareRuntimeRevision(ctx, client, node.ID, request); err != nil {
		return err
	}
	return c.executeLegacyNodeCommand(legacyConfigEvidence(ctx, request.ConfigJson), node.ID, request.OperationId, "sync_config", func(worker context.Context, fence *nodev1.DestructiveFence) (*nodev1.RuntimeActionResponse, error) {
		request.Fence = fence
		return client.Runtime().SyncConfig(worker, request)
	}, func(worker context.Context, response *nodev1.RuntimeActionResponse) error {
		_, err := c.finishRuntime(worker, node, response.GetRuntime(), response.GetMessage())
		return err
	})
}

func (c Controller) grpcApplyAmneziaWGUserOperation(ctx context.Context, client *nodeclient.Client, node NodeRow, operation OperationRow) error {
	if !operation.UserID.Valid || operation.UserID.Int64 <= 0 {
		return fmt.Errorf("AmneziaWG user operation requires a valid user id")
	}
	var serviceID sql.NullInt64
	if err := c.repo.db.QueryRowContext(ctx, `SELECT service_id FROM users WHERE id = ?`, operation.UserID.Int64).Scan(&serviceID); err != nil {
		return err
	}
	if !serviceID.Valid || serviceID.Int64 <= 0 {
		return fmt.Errorf("AmneziaWG user %d has no service", operation.UserID.Int64)
	}
	raw, err := c.repo.NodeRawConfig(ctx, node)
	if err != nil {
		return err
	}
	serviceTags, err := c.repo.ServiceAllowedTags(ctx)
	if err != nil {
		return err
	}
	var tags []string
	for _, inbound := range listOfMaps(raw["inbounds"]) {
		if !strings.EqualFold(strings.TrimSpace(stringValue(inbound["protocol"])), "amneziawg") {
			continue
		}
		tag := strings.TrimSpace(stringValue(inbound["tag"]))
		if tag != "" && serviceTags[serviceID.Int64][tag] && OVInboundMatchesTarget(inbound, xrayconfig.NodeTargetID(node.ID)) {
			tags = append(tags, tag)
		}
	}
	if len(tags) == 0 {
		return fmt.Errorf("no AmneziaWG inbound is assigned to user %d on node %d", operation.UserID.Int64, node.ID)
	}
	mutation := ""
	switch operation.OperationType {
	case "add_user", "enable_user":
		mutation = "add"
	case "update_user":
		mutation = "update"
	case "remove_user", "disable_user":
		mutation = "remove"
	default:
		return fmt.Errorf("unsupported AmneziaWG user operation %q", operation.OperationType)
	}
	var desiredByTag map[string][]AWGRuntimePeer
	if mutation != "remove" {
		runtimeConfig, runtimeErr := c.repo.AWGRuntime(ctx, node.ID)
		if runtimeErr != nil {
			return runtimeErr
		}
		desiredByTag = make(map[string][]AWGRuntimePeer, len(runtimeConfig.Inbounds))
		for _, inbound := range runtimeConfig.Inbounds {
			for _, peer := range inbound.Peers {
				if peer.UserID == operation.UserID.Int64 {
					desiredByTag[inbound.Tag] = append(desiredByTag[inbound.Tag], peer)
				}
			}
		}
	}
	for _, tag := range tags {
		operationID := fmt.Sprintf("%s-%d-%s", operation.OperationType, operation.ID, tag)
		var response *nodev1.RuntimeActionResponse
		if mutation == "remove" {
			response, err = client.Runtime().RemoveUser(ctx, &nodev1.RemoveInboundUserRequest{
				OperationId: operationID, InboundTag: tag,
				Email: fmt.Sprintf("amneziawg-user:%d", operation.UserID.Int64),
			})
		} else {
			user, userErr := grpcAmneziaWGMutationUser(operation.UserID.Int64, mutation, desiredByTag[tag])
			if userErr != nil {
				return userErr
			}
			request := &nodev1.InboundUserRequest{OperationId: operationID, InboundTag: tag, User: user}
			if mutation == "add" {
				response, err = client.Runtime().AddUser(ctx, request)
			} else {
				response, err = client.Runtime().UpdateUser(ctx, request)
			}
		}
		if err != nil {
			return fmt.Errorf("apply AmneziaWG %s for inbound %s: %w", mutation, tag, err)
		}
		if response == nil || !response.GetAccepted() || strings.TrimSpace(response.GetOperationId()) != operationID {
			return fmt.Errorf("apply AmneziaWG %s for inbound %s: missing, rejected, or mismatched acknowledgement", mutation, tag)
		}
	}
	return nil
}

func (c Controller) grpcApplyWireGuardUserOperation(ctx context.Context, client *nodeclient.Client, node NodeRow, operation OperationRow) error {
	if !operation.UserID.Valid || operation.UserID.Int64 <= 0 {
		return fmt.Errorf("WireGuard user operation requires a valid user id")
	}
	var serviceID sql.NullInt64
	if err := c.repo.db.QueryRowContext(ctx, `SELECT service_id FROM users WHERE id = ?`, operation.UserID.Int64).Scan(&serviceID); err != nil {
		return err
	}
	if !serviceID.Valid || serviceID.Int64 <= 0 {
		return fmt.Errorf("WireGuard user %d has no service", operation.UserID.Int64)
	}
	raw, err := c.repo.NodeRawConfig(ctx, node)
	if err != nil {
		return err
	}
	serviceTags, err := c.repo.ServiceAllowedTags(ctx)
	if err != nil {
		return err
	}
	tags := make([]string, 0)
	for _, inbound := range listOfMaps(raw["inbounds"]) {
		if !strings.EqualFold(strings.TrimSpace(stringValue(inbound["protocol"])), "wireguard") {
			continue
		}
		tag := strings.TrimSpace(stringValue(inbound["tag"]))
		if tag == "" || !serviceTags[serviceID.Int64][tag] || !OVInboundMatchesTarget(inbound, xrayconfig.NodeTargetID(node.ID)) {
			continue
		}
		tags = append(tags, tag)
	}
	if len(tags) == 0 {
		return fmt.Errorf("no WireGuard inbound is assigned to user %d on node %d", operation.UserID.Int64, node.ID)
	}
	mutation := ""
	switch operation.OperationType {
	case "add_user", "enable_user":
		mutation = "add"
	case "update_user":
		mutation = "update"
	case "remove_user", "disable_user":
		mutation = "remove"
	default:
		return fmt.Errorf("unsupported WireGuard user operation %q", operation.OperationType)
	}
	var desiredByTag map[string][]WGRuntimePeer
	if mutation != "remove" {
		runtimeConfig, runtimeErr := c.repo.WGRuntime(ctx, node.ID)
		if runtimeErr != nil {
			return runtimeErr
		}
		desiredByTag = make(map[string][]WGRuntimePeer, len(runtimeConfig.Inbounds))
		for _, inbound := range runtimeConfig.Inbounds {
			for _, peer := range inbound.Peers {
				if peer.UserID == operation.UserID.Int64 {
					desiredByTag[inbound.Tag] = append(desiredByTag[inbound.Tag], peer)
				}
			}
		}
	}
	for _, tag := range tags {
		operationID := fmt.Sprintf("%s-%d-%s", operation.OperationType, operation.ID, tag)
		var response *nodev1.RuntimeActionResponse
		if mutation == "remove" {
			response, err = client.Runtime().RemoveUser(ctx, &nodev1.RemoveInboundUserRequest{
				OperationId: operationID,
				InboundTag:  tag,
				Email:       fmt.Sprintf("wireguard-user:%d", operation.UserID.Int64),
			})
		} else {
			user, userErr := grpcWireGuardMutationUser(operation.UserID.Int64, mutation, desiredByTag[tag])
			if userErr != nil {
				return userErr
			}
			request := &nodev1.InboundUserRequest{
				OperationId: operationID,
				InboundTag:  tag,
				User:        user,
			}
			if mutation == "add" {
				response, err = client.Runtime().AddUser(ctx, request)
			} else {
				response, err = client.Runtime().UpdateUser(ctx, request)
			}
		}
		if err != nil {
			return fmt.Errorf("apply WireGuard %s for inbound %s: %w", mutation, tag, err)
		}
		if response == nil || !response.GetAccepted() || strings.TrimSpace(response.GetOperationId()) != operationID {
			return fmt.Errorf("apply WireGuard %s for inbound %s: missing, rejected, or mismatched acknowledgement", mutation, tag)
		}
	}
	return nil
}

func (c Controller) grpcRemoveUserFromNode(ctx context.Context, client *nodeclient.Client, node NodeRow, operation OperationRow, legacyEmail string) error {
	tags, err := c.legacyRuntimeInboundTags(ctx, node)
	if err != nil {
		return err
	}
	var lastErr error
	for _, tag := range tags {
		for index, email := range []string{taggedRuntimeUserEmail(legacyEmail, tag), legacyEmail} {
			_, err := client.Runtime().RemoveUser(ctx, &nodev1.RemoveInboundUserRequest{
				OperationId: fmt.Sprintf("%s-%d-%s-%d", operation.OperationType, operation.ID, tag, index),
				InboundTag:  tag,
				Email:       email,
			})
			if err != nil {
				if isIgnorableLegacyRemoveError(err) {
					continue
				}
				lastErr = err
			}
		}
	}
	return lastErr
}

func (c Controller) grpcAddUserToNode(ctx context.Context, client *nodeclient.Client, node NodeRow, operation OperationRow, legacyEmail string, refreshExisting bool) error {
	raw, err := c.repo.NodeRawConfig(ctx, node)
	if err != nil {
		return err
	}
	users, err := c.repo.RuntimeUsersByID(ctx, operation.UserID.Int64)
	if err != nil {
		return err
	}
	if len(users) == 0 {
		return nil
	}
	serviceTags, err := c.repo.ServiceAllowedTags(ctx)
	if err != nil {
		return err
	}
	masks, err := c.repo.UUIDMasks(ctx)
	if err != nil {
		return err
	}
	inbounds := listOfMaps(raw["inbounds"])
	var lastErr error
	matched := 0
	applied := 0
	eligibleServiceUser := false
	for _, runtimeUser := range users {
		if !runtimeUser.ServiceID.Valid || runtimeUser.ServiceID.Int64 <= 0 {
			continue
		}
		eligibleServiceUser = true
		for _, inbound := range inbounds {
			tag := stringValue(inbound["tag"])
			protocol := strings.ToLower(stringValue(inbound["protocol"]))
			if tag == "" || protocol == "" || protocol != runtimeUser.Protocol {
				continue
			}
			if !serviceTags[runtimeUser.ServiceID.Int64][tag] {
				continue
			}
			matched++
			settings, err := userread.RuntimeProxySettings(runtimeUser.Settings, runtimeUser.Protocol, runtimeUser.CredentialKey, runtimeUser.Flow, masks)
			if err != nil {
				lastErr = err
				continue
			}
			if runtimeUser.Protocol == "shadowsocks" {
				settings = userread.RuntimeShadowsocksSettings(settings, ensureMap(inbound, "settings"))
			}
			if flow := stringValue(settings["flow"]); flow != "" && !flowSupportedForInbound(inbound) {
				delete(settings, "flow")
			}
			email := taggedRuntimeUserEmail(legacyEmail, tag)
			settings["email"] = email
			settings["protocol"] = protocol
			if refreshExisting {
				for index, existingEmail := range []string{email, legacyEmail} {
					_, _ = client.Runtime().RemoveUser(ctx, &nodev1.RemoveInboundUserRequest{
						OperationId: fmt.Sprintf("%s-remove-%d-%s-%d", operation.OperationType, operation.ID, tag, index),
						InboundTag:  tag,
						Email:       existingEmail,
					})
				}
			}
			request := &nodev1.InboundUserRequest{
				OperationId: fmt.Sprintf("%s-%d-%s", operation.OperationType, operation.ID, tag),
				InboundTag:  tag,
				User:        grpcInboundUserPayload(settings),
			}
			if refreshExisting {
				_, err = client.Runtime().AddUser(ctx, request)
			} else {
				_, err = client.Runtime().UpdateUser(ctx, request)
			}
			if err != nil {
				if isIgnorableLegacyAddError(err) {
					applied++
					continue
				}
				lastErr = err
				continue
			}
			applied++
		}
	}
	if applied == 0 {
		if !eligibleServiceUser || matched == 0 {
			return nil
		}
		if lastErr != nil {
			return lastErr
		}
		return fmt.Errorf("no service inbound user was applied for user %d on node %d", operation.UserID.Int64, node.ID)
	}
	return lastErr
}

func grpcInboundUserPayload(settings map[string]any) *nodev1.InboundUser {
	fields := map[string]string{}
	for _, key := range []string{"id", "password", "auth", "flow", "method"} {
		if value := stringValue(settings[key]); value != "" {
			fields[key] = value
		}
	}
	if value := intValue(settings["level"]); value != 0 {
		fields["level"] = fmt.Sprint(value)
	}
	if value := intValue(settings["cipher_type"]); value != 0 {
		fields["cipher_type"] = fmt.Sprint(value)
	}
	if value, ok := boolFromAny(settings["iv_check"]); ok {
		fields["iv_check"] = fmt.Sprint(value)
	}
	return &nodev1.InboundUser{
		Email:    stringValue(settings["email"]),
		Protocol: strings.ToLower(stringValue(settings["protocol"])),
		Fields:   fields,
	}
}
