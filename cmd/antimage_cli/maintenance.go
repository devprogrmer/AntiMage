package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	operationapp "github.com/antimage/antimage/internal/app/operations"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func (c *cli) runMaintenance(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: maintenance request <action> [options] | maintenance check <fencing options>")
	}
	switch args[0] {
	case "check":
		return c.checkMaintenanceFence(args[1:])
	case "request":
		return c.requestMaintenance(args[1:])
	default:
		return fmt.Errorf("unknown maintenance subcommand")
	}
}

func (c *cli) checkMaintenanceFence(args []string) error {
	flags := flag.NewFlagSet("maintenance check", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	opID := flags.String("operation-id", "", "")
	executor := flags.String("executor-id", "", "")
	command := flags.String("command-id", "", "")
	action := flags.String("action", "", "")
	generation := flags.Int64("lease-generation", 0, "")
	resource := flags.Int64("resource-generation", 0, "")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return fmt.Errorf("invalid maintenance fencing arguments")
	}
	if *opID == "" || *executor == "" || *command == "" || *generation <= 0 || *resource <= 0 {
		return fmt.Errorf("persistent operation and positive generations required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	op, err := operationapp.Get(ctx, c.db, *opID)
	if err != nil {
		return fmt.Errorf("operation unavailable")
	}
	allowed := map[string][]string{"update": {"update"}, "restart": {"restart", "soft-reload"}, "soft-reload": {"soft-reload"}, "update-rollback": {"rollback", "update"}, "update-commit": {"update", "rollback"}, "update-activate": {"update", "rollback"}, "restart-activate": {"restart", "soft-reload"}}
	allowed["resume-panel-install"] = []string{"update"}
	allowed["resume-panel-restore"] = []string{"update", "rollback"}
	allowed["resume-panel-restart"] = []string{"update", "rollback"}
	types, ok := allowed[*action]
	if !ok {
		return fmt.Errorf("unsupported destructive Panel command")
	}
	matches := false
	for _, kind := range types {
		matches = matches || op.Type == kind
	}
	if !matches || op.TargetType != "panel" || op.TargetID != "panel" || operationapp.Terminal(op.State) {
		return fmt.Errorf("command does not own an active Panel operation")
	}
	digest := sha256.Sum256([]byte(op.ID + "|" + *action))
	if *command != fmt.Sprintf("panel-command-%x", digest[:16]) {
		return fmt.Errorf("command identity does not match persisted operation/action")
	}
	lease := operationapp.ExecutorLease{OperationID: op.ID, ExecutorID: *executor, Generation: *generation, ResourceGeneration: *resource, TargetType: op.TargetType, TargetID: op.TargetID, Dialect: c.dialect}
	if err := operationapp.CheckExecutorLease(operationapp.WithExecutorLease(ctx, lease), c.db, op.ID); err != nil {
		return fmt.Errorf("stale or expired Panel command rejected")
	}
	return nil
}

func maintenanceRequest(args []string) (string, map[string]any, error) {
	if len(args) == 0 {
		return "", nil, fmt.Errorf("maintenance action required")
	}
	action := args[0]
	flags := flag.NewFlagSet("maintenance request", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	version := flags.String("version", "", "")
	channel := flags.String("channel", "stable", "")
	policy := flags.String("policy", "pinned", "")
	node := flags.Int64("node-id", 0, "")
	backup := flags.String("backup-id", "", "")
	confirm := flags.Bool("confirm", false, "")
	source := flags.String("source-operation-id", "", "")
	geoTemplate := flags.String("template-id", "", "")
	_ = flags.Bool("n", true, "")
	_ = flags.Bool("no-logs", true, "")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
		return "", nil, fmt.Errorf("invalid maintenance request options")
	}
	payload := map[string]any{}
	switch action {
	case "update":
		payload["version"], payload["channel"], payload["policy"] = *version, *channel, *policy
		return "/api/maintenance/update", payload, nil
	case "restart":
		return "/api/maintenance/restart", payload, nil
	case "soft-reload":
		return "/api/maintenance/soft-reload", payload, nil
	case "update-rollback":
		if !*confirm {
			return "", nil, fmt.Errorf("rollback requires --confirm")
		}
		payload["backup_identity"], payload["confirm"] = *backup, *confirm
		return "/api/maintenance/rollback", payload, nil
	case "node-update":
		if *node <= 0 {
			return "", nil, fmt.Errorf("positive node-id required")
		}
		payload["version"], payload["channel"], payload["policy"] = *version, *channel, *policy
		return "/api/node/" + strconv.FormatInt(*node, 10) + "/service/update", payload, nil
	case "core-update", "geo-update":
		if *node <= 0 {
			return "", nil, fmt.Errorf("positive node-id required")
		}
		suffix := "xray/update"
		payload["version"] = *version
		if action == "geo-update" {
			suffix = "geo/update"
			payload = map[string]any{"template_id": *geoTemplate}
		}
		return "/api/node/" + strconv.FormatInt(*node, 10) + "/" + suffix, payload, nil
	case "node-restart", "node-rollback", "core-restart", "sync-config", "runtime-stop":
		if *node <= 0 {
			return "", nil, fmt.Errorf("positive node-id required")
		}
		suffix := map[string]string{"node-restart": "service/restart", "node-rollback": "service/rollback", "core-restart": "restart", "sync-config": "sync", "runtime-stop": "stop"}[action]
		if action == "node-rollback" {
			if !*confirm {
				return "", nil, fmt.Errorf("rollback requires --confirm")
			}
			payload["source_operation_id"], payload["confirm"] = *source, *confirm
		}
		return "/api/node/" + strconv.FormatInt(*node, 10) + "/" + suffix, payload, nil
	default:
		return "", nil, fmt.Errorf("unsupported maintenance action; use the authenticated service operation")
	}
}

func (c *cli) requestMaintenance(args []string) error {
	path, payload, err := maintenanceRequest(args)
	if err != nil {
		return err
	}
	base, err := url.Parse(strings.TrimSpace(os.Getenv("ANTIMAGE_CLI_API_URL")))
	if err != nil || base.Host == "" || (base.Scheme != "https" && base.Scheme != "http") || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return fmt.Errorf("ANTIMAGE_CLI_API_URL must be the Panel HTTP(S) origin")
	}
	// Plaintext credentials are allowed only on the local loopback interface.
	if base.Scheme == "http" && base.Hostname() != "127.0.0.1" && base.Hostname() != "::1" && base.Hostname() != "localhost" {
		return fmt.Errorf("remote Panel CLI access requires HTTPS")
	}
	token := strings.TrimSpace(os.Getenv("ANTIMAGE_CLI_API_TOKEN"))
	if token == "" {
		return fmt.Errorf("configure an authorized Panel API token for operational CLI requests")
	}
	base.Path = strings.TrimRight(base.Path, "/") + path
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, base.String(), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("invalid Panel request")
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-AntiMage-Origin", "cli")
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return fmt.Errorf("operational CLI redirects are forbidden")
	}}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("Panel request failed; inspect operation history before retry")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("Panel rejected operation (HTTP %d); inspect diagnostics/history", response.StatusCode)
	}
	// Print only service-controlled operation identities, never raw response data.
	var result map[string]any
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil {
		return fmt.Errorf("Panel accepted request; inspect operation history for its outcome")
	}
	for key, value := range maintenanceResponseIdentity(result) {
		fmt.Printf("%s=%s\n", key, value)
	}
	return nil
}

var maintenanceResponseIdentifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,159}$`)

func maintenanceResponseIdentity(result map[string]any) map[string]string {
	if operation, ok := result["operation"].(map[string]any); ok {
		result = operation
	}
	identity := make(map[string]string)
	for _, key := range []string{"id", "operation_id", "phase"} {
		if value, ok := result[key].(string); ok && maintenanceResponseIdentifier.MatchString(value) {
			identity[key] = value
		}
	}
	return identity
}
