package nodeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// xrayOnlineClient queries Xray's OnlineMap via CLI
// Uses 'xray api statsonline' or 'xray api statsonlineiplist' commands
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

// xrayOnlineUserResponse represents the response from xray api statsonline
type xrayOnlineUserResponse struct {
	Count int32 `json:"count"` // Number of active connections/IPs for this user
}

// queryOnlineCount queries the online connection count for a specific user email
// Returns count > 0 if user is online (may have multiple active connections/IPs)
// Returns 0 if user is offline
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
		// If command fails, it might be because:
		// 1. Xray version doesn't support statsonline
		// 2. User is not in OnlineMap (offline)
		// 3. statsUserOnline not enabled in config
		return 0, fmt.Errorf("xray api statsonline failed for %s: %w", email, err)
	}

	// Parse JSON output
	var response xrayOnlineUserResponse
	if err := json.Unmarshal(output, &response); err != nil {
		return 0, fmt.Errorf("parse statsonline response for %s: %w", email, err)
	}

	// Count represents number of active connections/IPs
	// count > 0 means user is online
	// count can be > 1 if user has multiple active connections
	return response.Count, nil
}

// queryAllOnlineUsers queries all online users in bulk
// Returns map of email -> connection count
// This is more efficient than per-user queries when checking many users
func (c *xrayOnlineClient) queryAllOnlineUsers(ctx context.Context) (map[string]int32, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// Try to get all online users at once
	// Note: The exact CLI command for bulk retrieval may vary by Xray version
	// We'll try multiple approaches

	// Approach 1: Try statsonlineiplist with -all flag (if supported)
	args := []string{
		"api",
		"statsonlineiplist",
		"--server=" + fmt.Sprintf("127.0.0.1:%d", c.apiPort),
		"-all",
	}

	cmd := exec.CommandContext(ctx, c.xrayPath, args...)
	output, err := cmd.Output()

	if err != nil {
		// Command might not be supported or flag not available
		// Fall back to empty map (will use per-user queries)
		return make(map[string]int32), nil
	}

	// Try to parse bulk response
	// Format may be JSON with user data
	var bulkData map[string]interface{}
	if err := json.Unmarshal(output, &bulkData); err != nil {
		// If parsing fails, return empty map to trigger per-user fallback
		return make(map[string]int32), nil
	}

	// Extract online users from bulk response
	// The exact format depends on Xray version
	// This is a best-effort parse
	onlineUsers := make(map[string]int32)

	// Common format: {"users": {"email1": count1, "email2": count2}}
	if users, ok := bulkData["users"].(map[string]interface{}); ok {
		for email, countVal := range users {
			switch v := countVal.(type) {
			case float64:
				onlineUsers[email] = int32(v)
			case int:
				onlineUsers[email] = int32(v)
			case int32:
				onlineUsers[email] = v
			}
		}
	}

	return onlineUsers, nil
}

// isOnlineCommandAvailable checks if Xray supports online CLI commands
// by attempting a simple statsonline query
func (c *xrayOnlineClient) isOnlineCommandAvailable(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	// Try to query with a dummy email to see if command exists
	args := []string{
		"api",
		"statsonline",
		"--server=" + fmt.Sprintf("127.0.0.1:%d", c.apiPort),
		"--email=test@example.com",
	}

	cmd := exec.CommandContext(ctx, c.xrayPath, args...)
	output, err := cmd.CombinedOutput()

	if err != nil {
		// Check if error is about unknown command vs. other errors
		outputStr := string(output)
		if strings.Contains(outputStr, "unknown command") ||
			strings.Contains(outputStr, "not found") ||
			strings.Contains(strings.ToLower(outputStr), "unrecognized") {
			return false
		}
	}

	// If we got any response (even if user doesn't exist), command is available
	return true
}
