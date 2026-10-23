package nodecontroller

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/antimage/antimage/internal/app/nodeclient"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

const (
	grpcAnyConnectUserIDField    = "anyconnect_user_id"
	grpcAnyConnectOperationField = "anyconnect_operation"
	grpcAnyConnectRevisionField  = "anyconnect_revision"
	grpcAnyConnectUserJSONField  = "anyconnect_user_json"
	grpcAnyConnectNameField      = "anyconnect_username"
	grpcAnyConnectRemovePrefix   = "anyconnect-user:"
)

func isAnyConnectProtocol(protocol string) bool {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "anyconnect", "cisco":
		return true
	default:
		return false
	}
}

func grpcAnyConnectMutationUser(user RemoteAccessRuntimeUser, operation string, revision uint64) (*nodev1.InboundUser, error) {
	if user.UserID <= 0 || revision == 0 {
		return nil, fmt.Errorf("AnyConnect mutation requires positive user id and revision")
	}
	operation = strings.ToLower(strings.TrimSpace(operation))
	if operation != "add" && operation != "update" && operation != "remove" {
		return nil, fmt.Errorf("unsupported AnyConnect user operation %q", operation)
	}
	fields := map[string]string{
		grpcAnyConnectUserIDField:    strconv.FormatInt(user.UserID, 10),
		grpcAnyConnectOperationField: operation,
		grpcAnyConnectRevisionField:  strconv.FormatUint(revision, 10),
	}
	if operation != "remove" {
		if strings.TrimSpace(user.Username) == "" || user.Password == "" {
			return nil, fmt.Errorf("AnyConnect user %d is missing runtime credentials", user.UserID)
		}
		raw, err := json.Marshal(user)
		if err != nil {
			return nil, err
		}
		fields[grpcAnyConnectUserJSONField] = string(raw)
	}
	return &nodev1.InboundUser{Protocol: "anyconnect", Fields: fields}, nil
}

func (c Controller) grpcApplyAnyConnectUserOperation(ctx context.Context, client *nodeclient.Client, node NodeRow, operation OperationRow) error {
	if !operation.UserID.Valid || operation.UserID.Int64 <= 0 || operation.ID <= 0 {
		return fmt.Errorf("AnyConnect user operation requires valid user and operation ids")
	}
	var serviceID sql.NullInt64
	if err := c.repo.db.QueryRowContext(ctx, `SELECT service_id FROM users WHERE id = ?`, operation.UserID.Int64).Scan(&serviceID); err != nil {
		return err
	}
	if !serviceID.Valid || serviceID.Int64 <= 0 {
		return fmt.Errorf("AnyConnect user %d has no service", operation.UserID.Int64)
	}
	tags, err := c.repo.ServiceAllowedTags(ctx)
	if err != nil {
		return err
	}
	runtime, err := c.repo.AnyConnectRuntime(ctx, node.ID)
	if err != nil {
		return err
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
		return fmt.Errorf("unsupported AnyConnect user operation %q", operation.OperationType)
	}
	matched := 0
	for _, in := range runtime.Inbounds {
		if !tags[serviceID.Int64][in.Tag] {
			continue
		}
		matched++
		opID := fmt.Sprintf("%s-%d-%s", operation.OperationType, operation.ID, in.Tag)
		var resp *nodev1.RuntimeActionResponse
		if mutation == "remove" {
			username := ""
			for i := range in.Users {
				if in.Users[i].UserID == operation.UserID.Int64 {
					username = strings.TrimSpace(in.Users[i].Username)
					break
				}
			}
			email := fmt.Sprintf("%s%d:%d", grpcAnyConnectRemovePrefix, operation.UserID.Int64, operation.ID)
			if username != "" {
				email = fmt.Sprintf("%s%d:%d:%s", grpcAnyConnectRemovePrefix, operation.UserID.Int64, operation.ID, base64.RawURLEncoding.EncodeToString([]byte(username)))
			}
			resp, err = client.Runtime().RemoveUser(ctx, &nodev1.RemoveInboundUserRequest{OperationId: opID, InboundTag: in.Tag, Email: email})
		} else {
			var desired *RemoteAccessRuntimeUser
			for i := range in.Users {
				if in.Users[i].UserID == operation.UserID.Int64 {
					v := in.Users[i]
					desired = &v
					break
				}
			}
			if desired == nil {
				return fmt.Errorf("active AnyConnect user %d is absent from inbound %s runtime policy", operation.UserID.Int64, in.Tag)
			}
			u, e := grpcAnyConnectMutationUser(*desired, mutation, uint64(operation.ID))
			if e != nil {
				return e
			}
			req := &nodev1.InboundUserRequest{OperationId: opID, InboundTag: in.Tag, User: u}
			if mutation == "add" {
				resp, err = client.Runtime().AddUser(ctx, req)
			} else {
				resp, err = client.Runtime().UpdateUser(ctx, req)
			}
		}
		if err != nil {
			return fmt.Errorf("apply AnyConnect %s for inbound %s: %w", mutation, in.Tag, err)
		}
		if resp == nil || !resp.GetAccepted() || strings.TrimSpace(resp.GetOperationId()) != opID {
			return fmt.Errorf("apply AnyConnect %s for inbound %s: invalid acknowledgement", mutation, in.Tag)
		}
	}
	if matched == 0 {
		return fmt.Errorf("no AnyConnect inbound is permitted for user %d service on node %d", operation.UserID.Int64, node.ID)
	}
	return nil
}
