#!/usr/bin/env bash

panel_database_fence_check() {
    [ -n "${FENCE_OPERATION_ID:-}" ] && [ -n "${PANEL_EXECUTOR_ID:-}" ] || {
        echo "An active service operation and executor are required for destructive Panel commands" >&2
        return 1
    }
    antimage_cli maintenance check --operation-id "$FENCE_OPERATION_ID" \
        --executor-id "$PANEL_EXECUTOR_ID" --lease-generation "$LEASE_GENERATION" \
        --resource-generation "$RESOURCE_GENERATION" --command-id "$COMMAND_ID" \
        --action "$PANEL_FENCE_ACTION"
}

panel_check_and_execute() {
    panel_database_fence_check || return 1
    "$@"
}

panel_owned_boundary() {
    panel_database_fence_check || return 1
    node_guarded_boundary panel_check_and_execute "$@"
}

panel_owned_dispatch() {
    local action="$1"; shift
    panel_database_fence_check || return 1
    node_command_fence accept || return 1
    lock_node_command || return 1
    local result=0
    case "$action" in
        update|resume-panel-install) update_command "$@" || result=$? ;;
        restart) restart_command "$@" || result=$? ;;
        update-rollback|resume-panel-restore) update_rollback_command "$@" || result=$? ;;
        update-commit) panel_owned_boundary update_commit_command "$@" || result=$? ;;
        fenced-panel-restart|resume-panel-restart) panel_owned_boundary systemctl restart "$APP_NAME.service" || result=$? ;;
        *) echo "Unsupported owned Panel command" >&2; result=1 ;;
    esac
    if [ "$result" -eq 0 ]; then
        finish_node_command || return 1
    fi
    # A failed/interrupted command retains its started journal record. It must
    # be reconciled from DB/files/runtime rather than dispatched a second time.
    return "$result"
}

panel_schedule_owned_restart() {
    local next_action="restart-activate"
    case "$PANEL_FENCE_ACTION" in update|update-rollback|resume-panel-install|resume-panel-restore) next_action="update-activate" ;; esac
    local next_command
    next_command=$(python3 - "$FENCE_OPERATION_ID" "$next_action" <<'PY'
import hashlib,sys
print('panel-command-'+hashlib.sha256((sys.argv[1]+'|'+sys.argv[2]).encode()).hexdigest()[:32])
PY
    ) || return 1
    command -v systemd-run >/dev/null 2>&1 || {
        echo "Owned Panel activation requires a bounded service-manager unit" >&2
        return 1
    }
    systemd-run --unit "${APP_NAME}-activate-${next_command}" --collect \
        --property=RuntimeMaxSec=180 --property=TimeoutStopSec=10 --property=KillMode=control-group \
        -- "$ANTIMAGE_SCRIPT_INSTALL_PATH" fenced-panel-restart \
        --fence-operation-id "$FENCE_OPERATION_ID" --executor-id "$PANEL_EXECUTOR_ID" \
        --lease-generation "$LEASE_GENERATION" --resource-generation "$RESOURCE_GENERATION" \
        --command-id "$next_command" --owned-action "$next_action"
}

panel_route_destructive_command() {
    local cmd="$1"; shift
    # The DB check is the authority; merely supplying these flags or being root
    # grants no execution rights. Strip only fencing options for legacy parsers.
    local args=()
    PANEL_FENCE_ACTION="$cmd"
    while [ "$#" -gt 0 ]; do
        case "$1" in
            --fence-operation-id) FENCE_OPERATION_ID="${2:?Operation required}"; shift 2 ;;
            --executor-id) PANEL_EXECUTOR_ID="${2:?Executor required}"; shift 2 ;;
            --lease-generation) LEASE_GENERATION="${2:?Lease generation required}"; shift 2 ;;
            --resource-generation) RESOURCE_GENERATION="${2:?Resource generation required}"; shift 2 ;;
            --command-id) COMMAND_ID="${2:?Command identity required}"; shift 2 ;;
            --owned-action) PANEL_FENCE_ACTION="${2:?Owned action required}"; shift 2 ;;
            -h|--help) echo "Use: antimage cli maintenance request <action> [options]"; return 0 ;;
            *) args+=("$1"); shift ;;
        esac
    done
    RESOURCE_ID="panel"
    if [ -n "${FENCE_OPERATION_ID:-}" ]; then
        case "$cmd:$PANEL_FENCE_ACTION" in
            update:update|restart:restart|update-rollback:update-rollback|update-commit:update-commit|fenced-panel-restart:update-activate|fenced-panel-restart:restart-activate|resume-panel-install:resume-panel-install|resume-panel-restore:resume-panel-restore|resume-panel-restart:resume-panel-restart)
                panel_owned_dispatch "$cmd" "${args[@]}" ;;
            *) echo "Command/action fencing mismatch" >&2; return 1 ;;
        esac
        return $?
    fi
    case "$cmd" in
        update|restart|update-rollback)
            antimage_cli maintenance request "$cmd" "${args[@]}" ;;
        core-update)
            antimage_cli maintenance request core-update "${args[@]}" ;;
        *)
            echo "This destructive command has no authorized service operation; use the authenticated maintenance service" >&2
            return 1 ;;
    esac
}
