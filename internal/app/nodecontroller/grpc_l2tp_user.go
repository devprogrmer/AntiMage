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
	grpcL2TPUserIDField    = "l2tp_user_id"
	grpcL2TPOperationField = "l2tp_operation"
	grpcL2TPRevisionField  = "l2tp_revision"
	grpcL2TPUserJSONField  = "l2tp_user_json"
	grpcL2TPRemovePrefix   = "l2tp-user:"
)

func grpcL2TPMutationUser(user L2TPRuntimeUser, operation string, revision uint64) (*nodev1.InboundUser, error) {
	if user.UserID <= 0 || revision == 0 {
		return nil, fmt.Errorf("L2TP mutation requires positive user id and revision")
	}
	operation = strings.ToLower(strings.TrimSpace(operation))
	if operation != "add" && operation != "update" && operation != "remove" {
		return nil, fmt.Errorf("unsupported L2TP user operation %q", operation)
	}
	fields := map[string]string{
		grpcL2TPUserIDField:    strconv.FormatInt(user.UserID, 10),
		grpcL2TPOperationField: operation,
		grpcL2TPRevisionField:  strconv.FormatUint(revision, 10),
	}
	if operation != "remove" {
		if strings.TrimSpace(user.VPNUsername) == "" || user.Password == "" {
			return nil, fmt.Errorf("L2TP user %d is missing runtime credentials", user.UserID)
		}
		encoded, err := json.Marshal(user)
		if err != nil {
			return nil, fmt.Errorf("encode L2TP user %d: %w", user.UserID, err)
		}
		fields[grpcL2TPUserJSONField] = string(encoded)
	}
	return &nodev1.InboundUser{Protocol: "l2tp", Fields: fields}, nil
}

func (c Controller) grpcApplyL2TPUserOperation(ctx context.Context, client *nodeclient.Client, node NodeRow, operation OperationRow) error {
	if !operation.UserID.Valid || operation.UserID.Int64 <= 0 || operation.ID <= 0 {
		return fmt.Errorf("L2TP user operation requires valid user and operation ids")
	}
	var serviceID sql.NullInt64
	if err := c.repo.db.QueryRowContext(ctx, `SELECT service_id FROM users WHERE id = ?`, operation.UserID.Int64).Scan(&serviceID); err != nil {
		return err
	}
	if !serviceID.Valid || serviceID.Int64 <= 0 {
		return fmt.Errorf("L2TP user %d has no service", operation.UserID.Int64)
	}
	raw, err := c.repo.NodeRawConfig(ctx, node)
	if err != nil {
		return err
	}
	serviceTags, err := c.repo.ServiceAllowedTags(ctx)
	if err != nil {
		return err
	}
	type target struct {
		tag  string
		pool string
	}
	targets := make([]target, 0)
	for _, inbound := range listOfMaps(raw["inbounds"]) {
		if !strings.EqualFold(strings.TrimSpace(stringValue(inbound["protocol"])), "l2tp") {
			continue
		}
		tag := strings.TrimSpace(stringValue(inbound["tag"]))
		if tag == "" || !serviceTags[serviceID.Int64][tag] || !OVInboundMatchesTarget(inbound, xrayconfig.NodeTargetID(node.ID)) {
			continue
		}
		settings := L2TPRuntimeSettings(inbound)
		targets = append(targets, target{tag: tag, pool: stringValue(settings["ipv4_pool_cidr"])})
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].tag < targets[j].tag })
	if len(targets) == 0 {
		return fmt.Errorf("no L2TP inbound is assigned to user %d on node %d", operation.UserID.Int64, node.ID)
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
		return fmt.Errorf("unsupported L2TP user operation %q", operation.OperationType)
	}
	for _, inbound := range targets {
		operationID := fmt.Sprintf("%s-%d-%s", operation.OperationType, operation.ID, inbound.tag)
		var response *nodev1.RuntimeActionResponse
		if mutation == "remove" {
			response, err = client.Runtime().RemoveUser(ctx, &nodev1.RemoveInboundUserRequest{
				OperationId: operationID,
				InboundTag:  inbound.tag,
				Email:       fmt.Sprintf("%s%d:%d", grpcL2TPRemovePrefix, operation.UserID.Int64, operation.ID),
			})
		} else {
			users, usersErr := c.repo.L2TPUsersForServices(ctx, []int64{serviceID.Int64}, inbound.pool)
			if usersErr != nil {
				return usersErr
			}
			var desired *L2TPRuntimeUser
			for i := range users {
				if users[i].UserID == operation.UserID.Int64 {
					copy := users[i]
					desired = &copy
					break
				}
			}
			if desired == nil {
				return fmt.Errorf("active L2TP user %d is absent from inbound %s runtime policy", operation.UserID.Int64, inbound.tag)
			}
			coefficient := runtimeInboundUsageCoefficients(listOfMaps(raw["inbounds"]))[inbound.tag]
			desired.UsageCoefficient = normalizeUsageFactor(node.UsageCoefficient)
			desired.InboundCoefficient = coefficient
			reflections, reflectionErr := c.repo.runtimeUsageReflections(ctx, node.ID, []int64{desired.UserID})
			if reflectionErr != nil {
				return fmt.Errorf("load L2TP usage reflection watermark: %w", reflectionErr)
			}
			reflection := reflections[desired.UserID]
			desired.UsedTraffic = reflection.UsedTraffic
			desired.ReflectedUsageBatchID = reflection.BatchID
			user, userErr := grpcL2TPMutationUser(*desired, mutation, uint64(operation.ID))
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
			return fmt.Errorf("apply L2TP %s for inbound %s: %w", mutation, inbound.tag, err)
		}
		if response == nil || !response.GetAccepted() || strings.TrimSpace(response.GetOperationId()) != operationID {
			return fmt.Errorf("apply L2TP %s for inbound %s: runtime returned an invalid acknowledgement", mutation, inbound.tag)
		}
	}
	return nil
}
