package nodecontroller

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/antimage/antimage/internal/app/nodeclient"
	"github.com/antimage/antimage/internal/app/xrayconfig"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

const (
	grpcOpenVPNUserIDField     = "openvpn_user_id"
	grpcOpenVPNOperationField  = "openvpn_operation"
	grpcOpenVPNRevisionField   = "openvpn_revision"
	grpcOpenVPNUserJSONField   = "openvpn_user_json"
	grpcOpenVPNRemoveUserEmail = "openvpn-user:"
)

func grpcOpenVPNMutationUser(user OVRuntimeUser, operation string, revision uint64) (*nodev1.InboundUser, error) {
	if user.UserID <= 0 || revision == 0 {
		return nil, fmt.Errorf("OpenVPN mutation requires positive user id and revision")
	}
	operation = strings.ToLower(strings.TrimSpace(operation))
	if operation != "add" && operation != "update" && operation != "remove" {
		return nil, fmt.Errorf("unsupported OpenVPN user operation %q", operation)
	}
	fields := map[string]string{
		grpcOpenVPNUserIDField:    strconv.FormatInt(user.UserID, 10),
		grpcOpenVPNOperationField: operation,
		grpcOpenVPNRevisionField:  strconv.FormatUint(revision, 10),
	}
	if operation != "remove" {
		if strings.TrimSpace(user.VPNUsername) == "" || user.Password == "" {
			return nil, fmt.Errorf("OpenVPN user %d is missing runtime credentials", user.UserID)
		}
		encoded, err := json.Marshal(user)
		if err != nil {
			return nil, fmt.Errorf("encode OpenVPN user %d: %w", user.UserID, err)
		}
		fields[grpcOpenVPNUserJSONField] = string(encoded)
	}
	return &nodev1.InboundUser{Protocol: "openvpn", Fields: fields}, nil
}

func (c Controller) grpcApplyOpenVPNUserOperation(ctx context.Context, client *nodeclient.Client, node NodeRow, operation OperationRow) error {
	if !operation.UserID.Valid || operation.UserID.Int64 <= 0 || operation.ID <= 0 {
		return fmt.Errorf("OpenVPN user operation requires valid user and operation ids")
	}
	raw, err := c.repo.NodeRawConfig(ctx, node)
	if err != nil {
		return err
	}
	serviceTags, err := c.repo.ServiceAllowedTags(ctx)
	if err != nil {
		return err
	}
	inboundSpeedLimits, err := c.repo.inboundSpeedLimitsByTag(ctx)
	if err != nil {
		return err
	}
	type target struct {
		tag      string
		settings map[string]any
	}
	targets := make([]target, 0)
	for _, inbound := range listOfMaps(raw["inbounds"]) {
		if !strings.EqualFold(strings.TrimSpace(stringValue(inbound["protocol"])), "openvpn") {
			continue
		}
		tag := strings.TrimSpace(stringValue(inbound["tag"]))
		if tag == "" || !OVInboundMatchesTarget(inbound, xrayconfig.NodeTargetID(node.ID)) {
			continue
		}
		serviceIDs, serviceErr := c.repo.OVServiceIDsForInbound(ctx, tag)
		if serviceErr != nil {
			return serviceErr
		}
		assigned := false
		for _, serviceID := range serviceIDs {
			if serviceTags[serviceID][tag] {
				assigned = true
				break
			}
		}
		if assigned {
			targets = append(targets, target{tag: tag, settings: OVRuntimeSettings(inbound)})
		}
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].tag < targets[j].tag })
	if len(targets) == 0 {
		return fmt.Errorf("no OpenVPN inbound is assigned to user %d on node %d", operation.UserID.Int64, node.ID)
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
		return fmt.Errorf("unsupported OpenVPN user operation %q", operation.OperationType)
	}
	revision := uint64(operation.ID)
	for _, inbound := range targets {
		operationID := fmt.Sprintf("%s-%d-%s", operation.OperationType, operation.ID, inbound.tag)
		var response *nodev1.RuntimeActionResponse
		if mutation == "remove" {
			response, err = client.Runtime().RemoveUser(ctx, &nodev1.RemoveInboundUserRequest{
				OperationId: operationID,
				InboundTag:  inbound.tag,
				Email:       fmt.Sprintf("%s%d:%d", grpcOpenVPNRemoveUserEmail, operation.UserID.Int64, revision),
			})
		} else {
			serviceIDs, serviceErr := c.repo.OVServiceIDsForInbound(ctx, inbound.tag)
			if serviceErr != nil {
				return serviceErr
			}
			users, usersErr := c.repo.OVUsersForServices(ctx, inbound.tag, serviceIDs, stringValue(inbound.settings["ipv4_pool_cidr"]))
			if usersErr != nil {
				return usersErr
			}
			var desired *OVRuntimeUser
			for i := range users {
				if users[i].UserID == operation.UserID.Int64 {
					copy := users[i]
					desired = &copy
					break
				}
			}
			if desired == nil {
				return fmt.Errorf("active OpenVPN user %d is absent from inbound %s runtime policy", operation.UserID.Int64, inbound.tag)
			}
			desired.UploadSpeedLimit, desired.DownloadSpeedLimit = applyInboundSpeedLimit(inboundSpeedLimits[inbound.tag], desired.UploadSpeedLimit, desired.DownloadSpeedLimit)
			user, userErr := grpcOpenVPNMutationUser(*desired, mutation, revision)
			if userErr != nil {
				return userErr
			}
			request := &nodev1.InboundUserRequest{OperationId: operationID, InboundTag: inbound.tag, User: user}
			if mutation == "add" {
				response, err = client.Runtime().AddUser(ctx, request)
			} else {
				response, err = client.Runtime().UpdateUser(ctx, request)
			}
		}
		if err != nil {
			return fmt.Errorf("apply OpenVPN %s for inbound %s: %w", mutation, inbound.tag, err)
		}
		if !validOpenVPNMutationAcknowledgement(response, operationID) {
			return fmt.Errorf("apply OpenVPN %s for inbound %s: runtime returned an invalid acknowledgement", mutation, inbound.tag)
		}
	}
	return nil
}

func validOpenVPNMutationAcknowledgement(response *nodev1.RuntimeActionResponse, operationID string) bool {
	return response != nil && response.GetAccepted() && strings.TrimSpace(operationID) != "" && response.GetOperationId() == operationID
}
