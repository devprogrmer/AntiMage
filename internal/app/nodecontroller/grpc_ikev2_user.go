package nodecontroller

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/antimage/antimage/internal/app/nodeclient"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"strconv"
	"strings"
)

const (
	grpcIKEv2UserIDField    = "ikev2_user_id"
	grpcIKEv2OperationField = "ikev2_operation"
	grpcIKEv2RevisionField  = "ikev2_revision"
	grpcIKEv2UserJSONField  = "ikev2_user_json"
	grpcIKEv2RemovePrefix   = "ikev2-user:"
)

func grpcIKEv2MutationUser(user RemoteAccessRuntimeUser, operation string, revision uint64) (*nodev1.InboundUser, error) {
	if user.UserID <= 0 || revision == 0 {
		return nil, fmt.Errorf("IKEv2 mutation requires positive user id and revision")
	}
	operation = strings.ToLower(strings.TrimSpace(operation))
	if operation != "add" && operation != "update" && operation != "remove" {
		return nil, fmt.Errorf("unsupported IKEv2 user operation %q", operation)
	}
	fields := map[string]string{grpcIKEv2UserIDField: strconv.FormatInt(user.UserID, 10), grpcIKEv2OperationField: operation, grpcIKEv2RevisionField: strconv.FormatUint(revision, 10)}
	if operation != "remove" {
		if strings.TrimSpace(user.Username) == "" || user.Password == "" {
			return nil, fmt.Errorf("IKEv2 user %d is missing runtime credentials", user.UserID)
		}
		b, e := json.Marshal(user)
		if e != nil {
			return nil, e
		}
		fields[grpcIKEv2UserJSONField] = string(b)
	}
	return &nodev1.InboundUser{Protocol: "ikev2", Fields: fields}, nil
}
func (c Controller) grpcApplyIKEv2UserOperation(ctx context.Context, client *nodeclient.Client, node NodeRow, operation OperationRow) error {
	if !operation.UserID.Valid || operation.UserID.Int64 <= 0 || operation.ID <= 0 {
		return fmt.Errorf("IKEv2 user operation requires valid user and operation ids")
	}
	var serviceID sql.NullInt64
	if err := c.repo.db.QueryRowContext(ctx, `SELECT service_id FROM users WHERE id = ?`, operation.UserID.Int64).Scan(&serviceID); err != nil {
		return err
	}
	if !serviceID.Valid || serviceID.Int64 <= 0 {
		return fmt.Errorf("IKEv2 user %d has no service", operation.UserID.Int64)
	}
	tags, err := c.repo.ServiceAllowedTags(ctx)
	if err != nil {
		return err
	}
	runtime, err := c.repo.IKEv2Runtime(ctx, node.ID)
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
		return fmt.Errorf("unsupported IKEv2 user operation %q", operation.OperationType)
	}
	for _, in := range runtime.Inbounds {
		if !tags[serviceID.Int64][in.Tag] {
			continue
		}
		opID := fmt.Sprintf("%s-%d-%s", operation.OperationType, operation.ID, in.Tag)
		var resp *nodev1.RuntimeActionResponse
		if mutation == "remove" {
			resp, err = client.Runtime().RemoveUser(ctx, &nodev1.RemoveInboundUserRequest{OperationId: opID, InboundTag: in.Tag, Email: fmt.Sprintf("%s%d:%d", grpcIKEv2RemovePrefix, operation.UserID.Int64, operation.ID)})
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
				return fmt.Errorf("active IKEv2 user %d is absent from inbound %s runtime policy", operation.UserID.Int64, in.Tag)
			}
			u, e := grpcIKEv2MutationUser(*desired, mutation, uint64(operation.ID))
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
			return fmt.Errorf("apply IKEv2 %s for inbound %s: %w", mutation, in.Tag, err)
		}
		if resp == nil || !resp.GetAccepted() || strings.TrimSpace(resp.GetOperationId()) != opID {
			return fmt.Errorf("apply IKEv2 %s for inbound %s: invalid acknowledgement", mutation, in.Tag)
		}
	}
	return nil
}
