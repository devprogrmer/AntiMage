package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"time"

	adminapp "github.com/antimage/antimage/internal/app/admin"
)

func selfAdminBudgetOrPrivilegeChange(fields map[string]json.RawMessage) bool {
	for _, field := range []string{
		"role", "permissions", "data_limit", "traffic_limit_mode", "use_service_traffic_limits",
		"services", "service_limits", "users_limit", "delete_user_usage_limit_enabled", "delete_user_usage_limit",
	} {
		if _, ok := fields[field]; ok {
			return true
		}
	}
	return false
}

func resellerChildPermissions() adminapp.AdminPermissions {
	perms := adminapp.RoleDefaultPermissions(adminapp.RoleStandard)
	perms.Users.Delete = true
	perms.Users.AllowUnlimitedData = false
	return perms
}

func budgetValue(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func resellerServiceScope(assigned []int64, services *[]int64) bool {
	if services == nil {
		return true
	}
	allowed := make(map[int64]struct{}, len(assigned))
	for _, id := range assigned {
		allowed[id] = struct{}{}
	}
	for _, id := range *services {
		if _, ok := allowed[id]; !ok {
			return false
		}
	}
	return true
}

func resellerParentIDTx(ctx context.Context, tx *sql.Tx, createdBy string) (int64, error) {
	if createdBy == "" {
		return 0, nil
	}
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM admins WHERE username = ? AND role = ?`, createdBy, string(adminapp.RoleReseller)).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return id, err
}

// A grant is spent when issued. Deleting or reducing a child admin never
// refunds it; only a deliberate adjustment by a full-access admin can do so.
func reserveResellerBudgetTx(ctx context.Context, tx *sql.Tx, resellerID, amount int64) error {
	if amount <= 0 {
		return statusError{status: http.StatusUnprocessableEntity, detail: "Traffic grant must be positive"}
	}
	result, err := tx.ExecContext(ctx, `UPDATE admins
SET created_traffic = COALESCE(created_traffic, 0) + ?
WHERE id = ? AND role = ? AND status = ? AND data_limit IS NOT NULL
AND data_limit >= ? AND COALESCE(created_traffic, 0) <= data_limit - ?`,
		amount, resellerID, string(adminapp.RoleReseller), string(adminapp.StatusActive), amount, amount)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return statusError{status: http.StatusForbidden, detail: "Reseller traffic budget exceeded"}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO admin_created_traffic_logs (admin_id, service_id, amount, action, created_at) VALUES (?, NULL, ?, ?, ?)`,
		resellerID, amount, "reseller_child_grant", dbTimestamp(time.Now().UTC()))
	return err
}
