package vpnuimigration

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)

const maxSourceAccounts = 100_000

func Analyze(ctx context.Context, path string) (Analysis, error) {
	path, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil || path == "" {
		return Analysis{}, Error{Message: "Invalid vpn-ui backup path"}
	}
	dsn := "file:" + filepath.ToSlash(path) + "?mode=ro&immutable=1"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return Analysis{}, Error{Message: "Unable to open vpn-ui backup"}
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return Analysis{}, Error{Message: "The selected file is not a readable SQLite backup"}
	}

	if hasTable(ctx, db, "accounts") {
		return analyzeAccounts(ctx, db)
	}
	if hasTable(ctx, db, "client_traffics") {
		return analyzeLegacyClients(ctx, db)
	}
	return Analysis{}, Error{Message: "The selected SQLite database is not a supported vpn-ui backup"}
}

func analyzeAccounts(ctx context.Context, db *sql.DB) (Analysis, error) {
	required := []string{"id", "email", "total_gb", "expiry_time", "enable", "limit_ip"}
	columns, err := tableColumns(ctx, db, "accounts")
	if err != nil || !containsColumns(columns, required) {
		return Analysis{}, Error{Message: "The vpn-ui accounts schema is not supported"}
	}
	deviceExpr := "NULL"
	if columns["user_limit_override"] {
		deviceExpr = "user_limit_override"
	}
	noteExpr := "''"
	if columns["comment"] {
		noteExpr = "comment"
	}
	rows, err := db.QueryContext(ctx, `SELECT id, email, total_gb, expiry_time, enable, limit_ip, `+deviceExpr+`, `+noteExpr+` FROM accounts ORDER BY id LIMIT ?`, maxSourceAccounts+1)
	if err != nil {
		return Analysis{}, Error{Message: "Unable to read vpn-ui accounts"}
	}
	defer rows.Close()
	traffic, err := loadTraffic(ctx, db)
	if err != nil {
		return Analysis{}, err
	}
	result := Analysis{}
	seen := map[string]struct{}{}
	for rows.Next() {
		var id, total, expiry, limitIP int64
		var enabled bool
		var username, note string
		var device sql.NullInt64
		if err := rows.Scan(&id, &username, &total, &expiry, &enabled, &limitIP, &device, &note); err != nil {
			return Analysis{}, Error{Message: "Unable to decode vpn-ui accounts"}
		}
		account, ok := normalizeAccount(id, username, enabled, traffic[strings.ToLower(strings.TrimSpace(username))], total, expiry, limitIP, device, note)
		if !ok {
			result.Warnings = append(result.Warnings, fmt.Sprintf("Skipped vpn-ui account %d with an invalid username", id))
			continue
		}
		key := strings.ToLower(account.Username)
		if _, exists := seen[key]; exists {
			result.Warnings = append(result.Warnings, "Skipped duplicate vpn-ui username "+account.Username)
			continue
		}
		seen[key] = struct{}{}
		result.Accounts = append(result.Accounts, account)
	}
	if len(result.Accounts) > maxSourceAccounts {
		return Analysis{}, Error{Message: "vpn-ui backup contains too many accounts"}
	}
	return result, rows.Err()
}

func analyzeLegacyClients(ctx context.Context, db *sql.DB) (Analysis, error) {
	columns, err := tableColumns(ctx, db, "client_traffics")
	if err != nil || !containsColumns(columns, []string{"id", "email", "enable", "up", "down", "expiry_time", "total"}) {
		return Analysis{}, Error{Message: "The legacy vpn-ui client schema is not supported"}
	}
	rows, err := db.QueryContext(ctx, `SELECT id, email, enable, up, down, expiry_time, total FROM client_traffics ORDER BY id LIMIT ?`, maxSourceAccounts+1)
	if err != nil {
		return Analysis{}, Error{Message: "Unable to read legacy vpn-ui clients"}
	}
	defer rows.Close()
	result := Analysis{Warnings: []string{"Legacy vpn-ui backup detected; IP and device limits are unavailable"}}
	seen := map[string]struct{}{}
	for rows.Next() {
		var id, up, down, expiry, total int64
		var username string
		var enabled bool
		if err := rows.Scan(&id, &username, &enabled, &up, &down, &expiry, &total); err != nil {
			return Analysis{}, Error{Message: "Unable to decode legacy vpn-ui clients"}
		}
		account, ok := normalizeAccount(id, username, enabled, nonNegative(up)+nonNegative(down), total, expiry, 0, sql.NullInt64{}, "")
		if !ok {
			continue
		}
		key := strings.ToLower(account.Username)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result.Accounts = append(result.Accounts, account)
	}
	if len(result.Accounts) > maxSourceAccounts {
		return Analysis{}, Error{Message: "vpn-ui backup contains too many accounts"}
	}
	return result, rows.Err()
}

func normalizeAccount(id int64, username string, enabled bool, used, total, expiry, limitIP int64, device sql.NullInt64, note string) (Account, bool) {
	username = strings.TrimSpace(username)
	if username == "" || len(username) > 34 {
		return Account{}, false
	}
	var dataLimit *int64
	if total > 0 {
		value := total
		dataLimit = &value
	}
	var expires *int64
	if expiry > 0 {
		if expiry > 10_000_000_000 {
			expiry /= 1000
		}
		expires = &expiry
	}
	deviceLimit := int64(0)
	if device.Valid && device.Int64 > 0 {
		deviceLimit = device.Int64
	}
	return Account{SourceID: id, Username: username, Enabled: enabled, UsedBytes: nonNegative(used), DataLimit: dataLimit, Expire: expires, IPLimit: nonNegative(limitIP), DeviceLimit: deviceLimit, Note: strings.TrimSpace(note)}, true
}

func loadTraffic(ctx context.Context, db *sql.DB) (map[string]int64, error) {
	result := map[string]int64{}
	if !hasTable(ctx, db, "client_traffics") {
		return result, nil
	}
	columns, err := tableColumns(ctx, db, "client_traffics")
	if err != nil || !containsColumns(columns, []string{"email", "up", "down"}) {
		return result, nil
	}
	rows, err := db.QueryContext(ctx, `SELECT email, up, down FROM client_traffics`)
	if err != nil {
		return nil, Error{Message: "Unable to read vpn-ui traffic counters"}
	}
	defer rows.Close()
	for rows.Next() {
		var email string
		var up, down int64
		if err := rows.Scan(&email, &up, &down); err != nil {
			return nil, Error{Message: "Unable to decode vpn-ui traffic counters"}
		}
		result[strings.ToLower(strings.TrimSpace(email))] = nonNegative(up) + nonNegative(down)
	}
	return result, rows.Err()
}

func hasTable(ctx context.Context, db *sql.DB, name string) bool {
	var found string
	return db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&found) == nil
}

func tableColumns(ctx context.Context, db *sql.DB, name string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(`+name+`)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]bool{}
	for rows.Next() {
		var cid, notNull, primary int
		var column, kind string
		var defaultValue any
		if err := rows.Scan(&cid, &column, &kind, &notNull, &defaultValue, &primary); err != nil {
			return nil, err
		}
		result[strings.ToLower(column)] = true
	}
	return result, rows.Err()
}

func containsColumns(columns map[string]bool, required []string) bool {
	for _, column := range required {
		if !columns[column] {
			return false
		}
	}
	return true
}

func nonNegative(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

func SortAccounts(accounts []Account) {
	sort.SliceStable(accounts, func(i, j int) bool { return accounts[i].SourceID < accounts[j].SourceID })
}
