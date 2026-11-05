package user

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"time"

	adminapp "github.com/antimage/antimage/internal/app/admin"
)

// ApplyShopExtensionTx uses the normal user permission, reseller budget,
// service capacity and node-outbox checks, without committing independently.
func (s Service) ApplyShopExtensionTx(ctx context.Context, tx *sql.Tx, admin adminapp.Admin, telegramID, serviceID int64, username, kind string, bytes, days int64) error {
	if tx == nil || telegramID <= 0 || serviceID <= 0 {
		return fmt.Errorf("invalid extension")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET username=username WHERE LOWER(username)=LOWER(?) AND telegram_id=? AND service_id=?`, username, telegramID, serviceID); err != nil {
		return err
	}
	existing, err := s.repo.existingUserTx(ctx, tx, username)
	if err != nil {
		return err
	}
	var owner int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(telegram_id,0) FROM users WHERE id=?`, existing.ID).Scan(&owner); err != nil {
		return err
	}
	if owner != telegramID || existing.ServiceID == nil || *existing.ServiceID != serviceID {
		return fmt.Errorf("account does not belong to this order")
	}
	payload, fields, err := shopExtensionPayload(existing, bytes, days, kind, time.Now().UTC())
	if err != nil {
		return err
	}
	_, err = s.repo.updateUserMutationTx(ctx, tx, admin, username, payload, fields)
	return err
}

func shopExtensionPayload(existing existingUserRow, bytes, days int64, kind string, now time.Time) (UserModify, map[string]json.RawMessage, error) {
	invalid := func() (UserModify, map[string]json.RawMessage, error) {
		return UserModify{}, nil, fmt.Errorf("account cannot receive this extension")
	}
	if bytes <= 0 || days <= 0 || days > math.MaxInt64/86400 || (kind != "renew" && kind != "volume") || existing.DataLimit == nil || *existing.DataLimit <= 0 || existing.UsedTraffic < 0 {
		return invalid()
	}
	status := existing.Status
	if status != UserStatusActive && status != UserStatusLimited && status != UserStatusExpired && status != UserStatusOnHold {
		return invalid()
	}
	if kind == "volume" && (status == UserStatusExpired || (existing.Expire != nil && *existing.Expire <= now.Unix())) {
		return invalid()
	}
	base := max(*existing.DataLimit, existing.UsedTraffic)
	if base > math.MaxInt64-bytes {
		return invalid()
	}
	newLimit := base + bytes
	payload := UserModify{UserPayloadBase: UserPayloadBase{DataLimit: &newLimit}, Status: UserStatusModifyActive}
	fields := map[string]json.RawMessage{"data_limit": nil, "status": nil}
	if status == UserStatusOnHold {
		payload.Status = UserStatusModifyOnHold
		payload.OnHoldExpireDuration = existing.OnHoldExpireDuration
	}
	if kind == "renew" {
		seconds := days * 86400
		if status == UserStatusOnHold {
			if existing.OnHoldExpireDuration == nil || *existing.OnHoldExpireDuration <= 0 || *existing.OnHoldExpireDuration > math.MaxInt64-seconds {
				return invalid()
			}
			duration := *existing.OnHoldExpireDuration + seconds
			payload.OnHoldExpireDuration = &duration
			fields["on_hold_expire_duration"] = nil
		} else {
			// An unlimited-duration service keeps its existing time policy.
			if existing.Expire != nil && *existing.Expire > 0 {
				base := max(now.Unix(), *existing.Expire)
				if base > math.MaxInt64-seconds {
					return invalid()
				}
				expiry := base + seconds
				payload.Expire = &expiry
				fields["expire"] = nil
			}
		}
	} else if existing.Expire != nil && status != UserStatusOnHold {
		payload.Expire = existing.Expire
	}
	return payload, fields, nil
}
