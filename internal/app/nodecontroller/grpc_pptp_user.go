package nodecontroller

import (
	"context"
	"database/sql"
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
	grpcPPTPUserIDField    = "pptp_user_id"
	grpcPPTPOperationField = "pptp_operation"
	grpcPPTPRevisionField  = "pptp_revision"
	grpcPPTPUserJSONField  = "pptp_user_json"
	grpcPPTPRemovePrefix   = "pptp-user:"
)

func grpcPPTPMutationUser(user PPTPRuntimeUser, operation string, revision uint64) (*nodev1.InboundUser, error) {
	if user.UserID <= 0 || revision == 0 {
		return nil, fmt.Errorf("PPTP mutation requires positive user id and revision")
	}
	operation = strings.ToLower(strings.TrimSpace(operation))
	if operation != "add" && operation != "update" && operation != "remove" {
		return nil, fmt.Errorf("unsupported PPTP user operation %q", operation)
	}
	fields := map[string]string{
		grpcPPTPUserIDField:    strconv.FormatInt(user.UserID, 10),
		grpcPPTPOperationField: operation,
		grpcPPTPRevisionField:  strconv.FormatUint(revision, 10),
	}
	if operation != "remove" {
		if strings.TrimSpace(user.VPNUsername) == "" || user.Password == "" {
			return nil, fmt.Errorf("PPTP user %d is missing runtime credentials", user.UserID)
		}
		encoded, err := json.Marshal(user)
		if err != nil {
			return nil, fmt.Errorf("encode PPTP user %d: %w", user.UserID, err)
		}
		fields[grpcPPTPUserJSONField] = string(encoded)
	}
	return &nodev1.InboundUser{Protocol: "pptp", Fields: fields}, nil
}

func (c Controller) grpcApplyPPTPUserOperation(ctx context.Context, client *nodeclient.Client, node NodeRow, operation OperationRow) error {
	if !operation.UserID.Valid || operation.UserID.Int64 <= 0 || operation.ID <= 0 {
		return fmt.Errorf("PPTP user operation requires valid user and operation ids")
	}
	var serviceID sql.NullInt64
	if err := c.repo.db.QueryRowContext(ctx, `SELECT service_id FROM users WHERE id = ?`, operation.UserID.Int64).Scan(&serviceID); err != nil {
		return err
	}
	if !serviceID.Valid || serviceID.Int64 <= 0 {
		return fmt.Errorf("PPTP user %d has no service", operation.UserID.Int64)
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
		tag  string
		pool string
	}
	targets := make([]target, 0)
	for _, inbound := range listOfMaps(raw["inbounds"]) {
		if !strings.EqualFold(strings.TrimSpace(stringValue(inbound["protocol"])), "pptp") {
			continue
		}
		tag := strings.TrimSpace(stringValue(inbound["tag"]))
		if tag == "" || !serviceTags[serviceID.Int64][tag] || !OVInboundMatchesTarget(inbound, xrayconfig.NodeTargetID(node.ID)) {
			continue
		}
		settings := PPTPRuntimeSettings(inbound)
		targets = append(targets, target{tag: tag, pool: stringValue(settings["ipv4_pool_cidr"])})
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].tag < targets[j].tag })
	if len(targets) == 0 {
		return fmt.Errorf("no PPTP inbound is assigned to user %d on node %d", operation.UserID.Int64, node.ID)
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
		return fmt.Errorf("unsupported PPTP user operation %q", operation.OperationType)
	}
	for _, inbound := range targets {
		operationID := fmt.Sprintf("%s-%d-%s", operation.OperationType, operation.ID, inbound.tag)
		var response *nodev1.RuntimeActionResponse
		if mutation == "remove" {
			response, err = client.Runtime().RemoveUser(ctx, &nodev1.RemoveInboundUserRequest{
				OperationId: operationID,
				InboundTag:  inbound.tag,
				Email:       fmt.Sprintf("%s%d:%d", grpcPPTPRemovePrefix, operation.UserID.Int64, operation.ID),
			})
		} else {
			users, usersErr := c.repo.PPTPUsersForServices(ctx, []int64{serviceID.Int64}, inbound.pool)
			if usersErr != nil {
				return usersErr
			}
			var desired *PPTPRuntimeUser
			for i := range users {
				if users[i].UserID == operation.UserID.Int64 {
					copy := users[i]
					desired = &copy
					break
				}
			}
			if desired == nil {
				return fmt.Errorf("active PPTP user %d is absent from inbound %s runtime policy", operation.UserID.Int64, inbound.tag)
			}
			coefficient := runtimeInboundUsageCoefficients(listOfMaps(raw["inbounds"]))[inbound.tag]
			desired.UsageCoefficient = normalizeUsageFactor(node.UsageCoefficient)
			desired.InboundCoefficient = coefficient
			desired.UploadSpeedLimit, desired.DownloadSpeedLimit = applyInboundSpeedLimit(inboundSpeedLimits[inbound.tag], desired.UploadSpeedLimit, desired.DownloadSpeedLimit)
			reflections, reflectionErr := c.repo.runtimeUsageReflections(ctx, node.ID, []int64{desired.UserID})
			if reflectionErr != nil {
				return fmt.Errorf("load PPTP usage reflection watermark: %w", reflectionErr)
			}
			reflection := reflections[desired.UserID]
			desired.UsedTraffic = reflection.UsedTraffic
			desired.ReflectedUsageBatchID = reflection.BatchID
			desired.ReflectedUsageLedger = reflection.Ledger
			user, userErr := grpcPPTPMutationUser(*desired, mutation, uint64(operation.ID))
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
			return fmt.Errorf("apply PPTP %s for inbound %s: %w", mutation, inbound.tag, err)
		}
		if response == nil || !response.GetAccepted() || strings.TrimSpace(response.GetOperationId()) != operationID {
			return fmt.Errorf("apply PPTP %s for inbound %s: runtime returned an invalid acknowledgement", mutation, inbound.tag)
		}
	}
	return nil
}
