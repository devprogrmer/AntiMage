package backup

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	adminapp "github.com/antimage/antimage/internal/app/admin"
)

// Only destination login identities are carried into a migration. Their old
// sessions, API keys, service grants and accounting remain on the old panel.
type destinationAdmin struct {
	username      string
	passwordHash  string
	passwordReset sql.NullTime
	require2FA    bool
	totpPlaintext string
	totpEnabledAt sql.NullTime
	totpLastCount sql.NullInt64
}

func (s *Service) captureDestinationAdmins(ctx context.Context) ([]destinationAdmin, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT username, hashed_password, password_reset_at,
		COALESCE(require_2fa, 0), COALESCE(totp_secret, ''), totp_enabled_at, totp_last_counter
		FROM admins WHERE role = 'full_access' AND status = 'active'
			AND COALESCE(hashed_password, '') <> '' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var admins []destinationAdmin
	var encrypted []string
	for rows.Next() {
		var row destinationAdmin
		var username sql.NullString
		var required int64
		var secret string
		if err := rows.Scan(&username, &row.passwordHash, &row.passwordReset,
			&required, &secret, &row.totpEnabledAt, &row.totpLastCount); err != nil {
			return nil, err
		}
		if !username.Valid || strings.TrimSpace(username.String) == "" {
			return nil, fmt.Errorf("an active destination administrator has no username")
		}
		row.username = username.String
		row.require2FA = required != 0
		admins = append(admins, row)
		encrypted = append(encrypted, secret)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var oldKey string
	for i, secret := range encrypted {
		if secret == "" {
			continue
		}
		if oldKey == "" {
			oldKey, err = adminapp.NewRepository(s.db, s.dialect).AdminSecret(ctx)
			if err != nil {
				return nil, err
			}
		}
		admins[i].totpPlaintext, err = adminapp.DecryptTOTPSecret(secret, oldKey)
		if err != nil {
			return nil, fmt.Errorf("cannot decrypt destination administrator %q TOTP secret: %w", admins[i].username, err)
		}
	}
	return admins, nil
}

func (s *Service) restoreDestinationAdmins(ctx context.Context, admins []destinationAdmin) ([]string, error) {
	if len(admins) == 0 {
		return nil, nil
	}
	newKey := ""
	for _, row := range admins {
		if row.totpPlaintext != "" {
			var err error
			newKey, err = adminapp.NewRepository(s.db, s.dialect).AdminSecret(ctx)
			if err != nil {
				return nil, err
			}
			break
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT username FROM admins`)
	if err != nil {
		return nil, err
	}
	used := map[string]bool{}
	for rows.Next() {
		var username sql.NullString
		if err := rows.Scan(&username); err != nil {
			rows.Close()
			return nil, err
		}
		if username.Valid {
			used[strings.ToLower(username.String)] = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	warnings := []string{}
	for _, row := range admins {
		username := row.username
		if used[strings.ToLower(username)] {
			username = destinationAdminAlias(username, used)
			warnings = append(warnings, fmt.Sprintf("Destination administrator %q was preserved as %q because the backup already contains that username. Sign in with the destination password.", row.username, username))
		}
		used[strings.ToLower(username)] = true
		var encrypted any
		if row.totpPlaintext != "" {
			encrypted, err = adminapp.EncryptTOTPSecret(row.totpPlaintext, newKey)
			if err != nil {
				return nil, err
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO admins
			(username, hashed_password, password_reset_at, role, status, created_by,
			require_2fa, totp_secret, totp_enabled_at, totp_last_counter)
			VALUES (?, ?, ?, 'full_access', 'active', 'root', ?, ?, ?, ?)`,
			username, row.passwordHash, row.passwordReset, row.require2FA,
			encrypted, row.totpEnabledAt, row.totpLastCount); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	warnings = append(warnings, fmt.Sprintf("Preserved %d active destination administrator login(s). Existing sessions and API keys must be created again.", len(admins)))
	return warnings, nil
}

func destinationAdminAlias(original string, used map[string]bool) string {
	for n := 1; ; n++ {
		suffix := fmt.Sprintf("_local_%d", n)
		prefix := []rune(original)
		if len(prefix) > 34-len(suffix) {
			prefix = prefix[:34-len(suffix)]
		}
		candidate := string(prefix) + suffix
		if !used[strings.ToLower(candidate)] {
			return candidate
		}
	}
}
