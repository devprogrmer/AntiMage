package nodeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const maxXrayOnlineCount = int64(1<<31 - 1)

// xrayOnlineClient queries Xray's local OnlineMap through the Xray CLI.
type xrayOnlineClient struct {
	xrayPath string
	apiPort  int
}

func newXrayOnlineClient(xrayPath string, apiPort int) *xrayOnlineClient {
	return &xrayOnlineClient{
		xrayPath: xrayPath,
		apiPort:  apiPort,
	}
}

// Xray v26.7.11 statsonline returns app.stats.command.GetStatsResponse:
//
//	{
//	 "stat": {
//	   "name": "user>>>42.alice>>>online",
//	   "value": 1
//	 }
//	}
//
// It does not return a top-level "count" field.
type xrayOnlineStatResponse struct {
	Stat *struct {
		Name  string          `json:"name"`
		Value json.RawMessage `json:"value"`
	} `json:"stat"`
}

// statsonlineiplist -all returns GetUsersStatsResponse. "users" is an array,
// and the online count for each user is the number of entries in "ips".
type xrayBulkOnlineResponse struct {
	Users []struct {
		Email string            `json:"email"`
		IPs   []json.RawMessage `json:"ips"`
	} `json:"users"`
}

func parseXrayOnlineCountResponse(output []byte, email string) (int32, error) {
	var response xrayOnlineStatResponse
	if err := json.Unmarshal(output, &response); err != nil {
		return 0, fmt.Errorf("decode statsonline response: %w", err)
	}
	if response.Stat == nil {
		return 0, fmt.Errorf("statsonline response is missing stat")
	}

	expectedName := "user>>>" + strings.TrimSpace(email) + ">>>online"
	if response.Stat.Name != expectedName {
		return 0, fmt.Errorf(
			"unexpected statsonline stat name %q, want %q",
			response.Stat.Name,
			expectedName,
		)
	}

	value, err := parseXrayStatValue(response.Stat.Value)
	if err != nil {
		return 0, fmt.Errorf("decode statsonline value: %w", err)
	}
	if value < 0 || value > maxXrayOnlineCount {
		return 0, fmt.Errorf("statsonline value %d is outside int32 range", value)
	}

	return int32(value), nil
}

func parseXrayBulkOnlineResponse(output []byte) (map[string]int32, error) {
	var response xrayBulkOnlineResponse
	if err := json.Unmarshal(output, &response); err != nil {
		return nil, fmt.Errorf("decode statsonlineiplist response: %w", err)
	}

	online := make(map[string]int32, len(response.Users))
	for _, user := range response.Users {
		email := strings.TrimSpace(user.Email)
		if email == "" {
			continue
		}

		count := len(user.IPs)
		if int64(count) > maxXrayOnlineCount {
			return nil, fmt.Errorf(
				"online IP count for %q exceeds int32 range",
				email,
			)
		}

		if count > 0 {
			online[email] = int32(count)
		}
	}

	return online, nil
}

// queryOnlineCount returns the number of online IPs currently tracked by
// Xray's OnlineMap for one runtime user.
func (c *xrayOnlineClient) queryOnlineCount(ctx context.Context, email string) (int32, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	args := []string{
		"api",
		"statsonline",
		"--server=" + fmt.Sprintf("127.0.0.1:%d", c.apiPort),
		"--email=" + email,
	}

	cmd := exec.CommandContext(ctx, c.xrayPath, args...)
	output, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf(
			"xray api statsonline failed for %s: %w",
			email,
			err,
		)
	}

	count, err := parseXrayOnlineCountResponse(output, email)
	if err != nil {
		return 0, fmt.Errorf(
			"parse statsonline response for %s: %w",
			email,
			err,
		)
	}

	return count, nil
}

// queryAllOnlineUsers returns email -> online IP count using Xray's bulk
// GetUsersStats API exposed by `statsonlineiplist -all`.
func (c *xrayOnlineClient) queryAllOnlineUsers(ctx context.Context) (map[string]int32, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	args := []string{
		"api",
		"statsonlineiplist",
		"--server=" + fmt.Sprintf("127.0.0.1:%d", c.apiPort),
		"-all",
	}

	cmd := exec.CommandContext(ctx, c.xrayPath, args...)
	output, err := cmd.Output()
	if err != nil {
		// Bulk support is an optimization. Returning an empty result allows the
		// collector to fall back to per-user statsonline queries.
		return make(map[string]int32), nil
	}

	online, err := parseXrayBulkOnlineResponse(output)
	if err != nil {
		// A response-shape mismatch must trigger the safe per-user fallback
		// instead of silently interpreting every user as offline.
		return nil, err
	}

	return online, nil
}

// isOnlineCommandAvailable checks whether the configured Xray binary exposes
// the statsonline CLI command.
func (c *xrayOnlineClient) isOnlineCommandAvailable(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	args := []string{
		"api",
		"statsonline",
		"--server=" + fmt.Sprintf("127.0.0.1:%d", c.apiPort),
		"--email=test@example.com",
	}

	cmd := exec.CommandContext(ctx, c.xrayPath, args...)
	output, err := cmd.CombinedOutput()

	if err != nil {
		outputStr := strings.ToLower(string(output))
		if strings.Contains(outputStr, "unknown command") ||
			strings.Contains(outputStr, "not found") ||
			strings.Contains(outputStr, "unrecognized") {
			return false
		}
	}

	return true
}
