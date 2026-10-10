#!/usr/bin/env bash
set -e

APP_NAME_FROM_ARG=0
INSTALL_DIR="/opt"
NODE_DISCOVERY_BASE="/opt"
SKIP_SERVICE_UPDATE=0
INSTALL_MODE_REQUESTED=""
NODE_VERSION_REQUESTED=""
NODE_VERSION_SET=0
RESOLVED_BUILD_JSON=""
UPDATE_OPERATION_ID=""
LEASE_GENERATION=""
FENCE_OPERATION_ID=""
COMMAND_ID=""
RESOURCE_GENERATION=""
RESOURCE_ID=""
ANTIMAGE_NODE_SCRIPT_FLAVOR="${ANTIMAGE_NODE_SCRIPT_FLAVOR:-binary}"
ANTIMAGE_NODE_SCRIPT_SOURCE_FILE="${ANTIMAGE_NODE_SCRIPT_SOURCE_FILE:-antimage-node-binary.sh}"

SCRIPT_DEFAULT_APP_NAME="${ANTIMAGE_NODE_DEFAULT_APP_NAME:-antimage-node}"

declare -a DISCOVERED_NODE_PATHS=()
declare -a DISCOVERED_NODE_NAMES=()

ensure_valid_app_name() {
    local candidate="${APP_NAME:-$SCRIPT_DEFAULT_APP_NAME}"
    if ! [[ "$candidate" =~ ^[a-zA-Z0-9][a-zA-Z0-9_-]*$ ]]; then
        candidate="antimage-node"
        echo "Invalid app name detected. Falling back to default: $candidate"
    fi
    APP_NAME="$candidate"
}

set_app_context() {
	APP_DIR="${ANTIMAGE_NODE_APP_DIR:-${APP_DIR:-}}"
    if [ -z "$APP_NAME" ]; then
        APP_NAME="$SCRIPT_DEFAULT_APP_NAME"
    fi
    ensure_valid_app_name

    if [ -z "${APP_DIR:-}" ] || [ ! -d "$APP_DIR" ]; then
        if [ -d "$INSTALL_DIR/$APP_NAME" ]; then
            APP_DIR="$INSTALL_DIR/$APP_NAME"
        elif [ -d "$INSTALL_DIR/AntiMage-node" ]; then
            APP_DIR="$INSTALL_DIR/AntiMage-node"
        else
            APP_DIR="$INSTALL_DIR/$APP_NAME"
        fi
    fi

    DATA_DIR="/var/lib/$APP_NAME"
    DATA_MAIN_DIR="/var/lib/$APP_NAME"
    COMPOSE_FILE="$APP_DIR/docker-compose.yml"
    BRANCH_FILE="$APP_DIR/.branch"
    INSTALL_MODE_FILE="$APP_DIR/.install-mode"
    CERT_FILE="$DATA_DIR/cert.pem"
    CERT_KEY_FILE="$DATA_DIR/cert.key"
    ENV_FILE="$APP_DIR/.env"

    BINARY_BIN_DIR="$APP_DIR/bin"
    BINARY_NODE="$BINARY_BIN_DIR/antimage-node"
    BINARY_METADATA_FILE="$APP_DIR/.binary-release.json"
    BINARY_SERVICE_UNIT="/etc/systemd/system/${APP_NAME}.service"
}

while [[ $# -gt 0 ]]; do
    key="$1"
    
    case $key in
        install|update|rollback|update-commit|fence-accept|fenced-restart|fenced-reboot|uninstall|up|down|restart|status|logs|core-update|install-script|update-script|uninstall-script|edit|script-install|script-update|script-uninstall|help)
            COMMAND="$1"
            shift # past argument
        ;;
        --mode)
            if [ -z "${2:-}" ]; then
                echo "Error: --mode requires docker or binary."
                exit 1
            fi
            INSTALL_MODE_REQUESTED="${2:-}"
            shift 2
        ;;
        --binary)
            INSTALL_MODE_REQUESTED="binary"
            shift
        ;;
        --docker|--dockerized)
            INSTALL_MODE_REQUESTED="docker"
            shift
        ;;
        --dev)
            if [ "$NODE_VERSION_SET" -eq 1 ] && [ "$NODE_VERSION_REQUESTED" != "dev" ]; then
                echo "Error: Cannot use --dev and --version options simultaneously."
                exit 1
            fi
            NODE_VERSION_REQUESTED="dev"
            NODE_VERSION_SET=1
            shift
        ;;
        --operation-id)
            UPDATE_OPERATION_ID="${2:?operation ID is required}"
            shift 2
        ;;
        --lease-generation)
            LEASE_GENERATION="${2:?lease generation is required}"
            shift 2
        ;;
        --fence-operation-id)
            FENCE_OPERATION_ID="${2:?fence operation identity is required}"
            shift 2
        ;;
        --command-id)
            COMMAND_ID="${2:?command identity is required}"
            shift 2
        ;;
        --resource-generation)
            RESOURCE_GENERATION="${2:?resource generation is required}"
            shift 2
        ;;
        --resource-id)
            RESOURCE_ID="${2:?resource identity is required}"
            shift 2
        ;;
        --backup-id)
            ROLLBACK_BACKUP_ID="${2:?backup identity is required}"
            shift 2
        ;;
        --resolved-build)
            RESOLVED_BUILD_JSON="${2:?resolved build JSON is required}"
            shift 2
        ;;
        --version)
            if [ "$NODE_VERSION_SET" -eq 1 ]; then
                echo "Error: Cannot use --dev and --version options simultaneously."
                exit 1
            fi
            if [ -z "${2:-}" ]; then
                echo "Error: --version requires a value."
                exit 1
            fi
            NODE_VERSION_REQUESTED="${2:-}"
            NODE_VERSION_SET=1
            shift 2
        ;;
        --name)
            if [[ "$COMMAND" == "install" || "$COMMAND" == "install-script" || "$COMMAND" == "script-install" ]]; then
                APP_NAME="$2"
                APP_NAME_FROM_ARG=1
                shift # past argument
            else
                echo "Error: --name parameter is only allowed with 'install' or 'install-script' commands."
                exit 1
            fi
            shift # past value
        ;;
        *)
            shift # past unknown argument
        ;;
    esac
done

# Fetch IP address from ipinfo.io API
NODE_IP=""
if [ "$COMMAND" = "install" ]; then
    NODE_IP=$(curl --max-time 10 -s -4 ifconfig.io || true)

# If the IPv4 retrieval is empty, attempt to retrieve the IPv6 address
if [ -z "$NODE_IP" ]; then
    NODE_IP=$(curl --max-time 10 -s -6 ifconfig.io || true)
fi
fi

if [ "$APP_NAME_FROM_ARG" -eq 0 ]; then
    if [ -n "${ANTIMAGE_NODE_APP_NAME:-}" ]; then
        APP_NAME="$ANTIMAGE_NODE_APP_NAME"
    elif [[ "$COMMAND" == "install" || "$COMMAND" == "install-script" || "$COMMAND" == "script-install" ]]; then
        APP_NAME="$SCRIPT_DEFAULT_APP_NAME"
    elif [ -z "${APP_NAME:-}" ]; then
        APP_NAME="$SCRIPT_DEFAULT_APP_NAME"
    fi
fi
ensure_valid_app_name

LAST_XRAY_CORES=5

ANTIMAGE_REPO="${ANTIMAGE_REPO:-devprogrmer/AntiMage}"
ANTIMAGE_REF="${ANTIMAGE_REF:-master}"
ANTIMAGE_SCRIPT_BASE_URL_EXPLICIT=0
if [ -n "${ANTIMAGE_SCRIPT_BASE_URL+x}" ]; then
    ANTIMAGE_SCRIPT_BASE_URL_EXPLICIT=1
fi
ANTIMAGE_SCRIPT_BASE_URL="${ANTIMAGE_SCRIPT_BASE_URL:-https://raw.githubusercontent.com/${ANTIMAGE_REPO}/${ANTIMAGE_REF}/scripts/antimage}"
ANTIMAGE_NODE_RELEASE_REPO="${ANTIMAGE_NODE_RELEASE_REPO:-devprogrmer/AntiMage}"
ANTIMAGE_NODE_BINARY_DEV_BRANCH="${ANTIMAGE_NODE_BINARY_DEV_BRANCH:-dev}"
ANTIMAGE_NODE_BINARY_DEV_RELEASE_TAG="${ANTIMAGE_NODE_BINARY_DEV_RELEASE_TAG:-dev-builds}"
ANTIMAGE_NODE_BINARY_WORKFLOW_NAME="${ANTIMAGE_NODE_BINARY_WORKFLOW_NAME:-binary-build}"
ANTIMAGE_NODE_BINARY_ARTIFACT_PREFIX="${ANTIMAGE_NODE_BINARY_ARTIFACT_PREFIX:-antimage-node-binaries}"
DEFAULT_XRAY_CORE_VERSION="${DEFAULT_XRAY_CORE_VERSION:-v26.7.11}"

# Default node channel values
BRANCH="master"
SCRIPT_URL="$ANTIMAGE_SCRIPT_BASE_URL/$ANTIMAGE_NODE_SCRIPT_SOURCE_FILE"

colorized_echo() {
    local color=$1
    local text=$2
    local style=${3:-0}  # Default style is normal

    case $color in
        "red")
            printf "\e[${style};91m${text}\e[0m\n"
        ;;
        "green")
            printf "\e[${style};92m${text}\e[0m\n"
        ;;
        "yellow")
            printf "\e[${style};93m${text}\e[0m\n"
        ;;
        "blue")
            printf "\e[${style};94m${text}\e[0m\n"
        ;;
        "magenta")
            printf "\e[${style};95m${text}\e[0m\n"
        ;;
        "cyan")
            printf "\e[${style};96m${text}\e[0m\n"
        ;;
        *)
            echo "${text}"
        ;;
    esac
}

ui_is_tty() {
    [ -t 1 ] && [ -z "${NO_COLOR:-}" ]
}

ui_supports_cursor_motion() {
    ui_is_tty && [ "${TERM:-dumb}" != "dumb" ]
}

ui_terminal_columns() {
    local columns="${COLUMNS:-}"
    if ! [[ "$columns" =~ ^[0-9]+$ ]] || [ "$columns" -lt 20 ]; then
        columns=""
        if command -v tput >/dev/null 2>&1; then
            columns=$(tput cols 2>/dev/null || true)
        fi
    fi
    if ! [[ "$columns" =~ ^[0-9]+$ ]] || [ "$columns" -lt 20 ]; then
        columns=80
    fi
    printf "%s" "$columns"
}

ui_color() {
    local code="$1"
    shift || true
    if ui_is_tty; then
        printf "\033[%sm%s\033[0m" "$code" "$*"
    else
        printf "%s" "$*"
    fi
}

ui_line() {
    ui_color "38;5;39" "â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€"
    printf "\n"
}

ui_header() {
    local title="$1"
    local subtitle="${2:-}"
    printf "\n"
    ui_color "38;5;45;1" "â•­â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â•®"
    printf "\n  "
    ui_color "38;5;231;1" "$title"
    printf "\n"
    if [ -n "$subtitle" ]; then
        printf "  "
        ui_color "38;5;117" "$subtitle"
        printf "\n"
    fi
    ui_color "38;5;45;1" "â•°â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â•¯"
    printf "\n"
}

ui_section() {
    printf "\n"
    ui_color "38;5;45;1" "â—† $1"
    printf "\n"
    ui_line
}

ui_status_row() {
    local label="$1"
    local value="$2"
    printf "  "
    ui_color "38;5;245" "$(printf '%-14s' "$label")"
    ui_color "38;5;231;1" "$value"
    printf "\n"
}

ui_menu_item() {
    local number="$1"
    local command="$2"
    local description="$3"
    local selected="${4:-0}"
    local columns command_width=20 description_width command_label description_text
    columns=$(ui_terminal_columns)
    if [ "$columns" -lt 30 ]; then
        command_width=$((columns - 10))
    fi
    [ "$command_width" -lt 1 ] && command_width=1
    description_width=$((columns - 10 - command_width))
    printf -v command_label "%-${command_width}.${command_width}s" "$command"
    if [ "$description_width" -gt 0 ]; then
        description_text="${description:0:$description_width}"
    else
        description_text=""
    fi
    printf "  "
    if [ "$selected" = "1" ]; then
        ui_color "38;5;16;48;5;45;1" " > "
    else
        printf "   "
    fi
    ui_color "38;5;45;1" "$(printf '%2s' "$number")"
    printf "  "
    if [ "$selected" = "1" ]; then
        ui_color "38;5;231;1" "$command_label"
        ui_color "38;5;231" "$description_text"
    else
        ui_color "38;5;231;1" "$command_label"
        ui_color "38;5;245" "$description_text"
    fi
    printf "\n"
}

ui_menu_category() {
    printf "\n"
    ui_color "38;5;117;1" "  $1"
    printf "\n"
}

ui_clear() {
    if ui_is_tty; then
        printf "\033[H\033[2J"
    fi
}

ui_read_menu_choice() {
    local selected="$1"
    local total="$2"
    local key rest digits

    IFS= read -rsn1 key || return 1
    case "$key" in
        "")
            echo "enter:$selected"
            return
        ;;
        $'\033')
            rest=""
            while [ "${#rest}" -lt 8 ] && IFS= read -rsn1 -t 0.05 key; do
                rest="${rest}${key}"
                case "$key" in
                    [A-Za-z~]) break ;;
                esac
            done
            case "$rest" in
                *A)
                    selected=$((selected - 1))
                    [ "$selected" -lt 1 ] && selected="$total"
                    echo "move:$selected"
                    return
                ;;
                *B)
                    selected=$((selected + 1))
                    [ "$selected" -gt "$total" ] && selected=1
                    echo "move:$selected"
                    return
                ;;
            esac
            echo "move:$selected"
            return
        ;;
        [0-9])
            digits="$key"
            while IFS= read -rsn1 -t 0.35 rest; do
                case "$rest" in
                    [0-9]) digits="${digits}${rest}" ;;
                    "") break ;;
                    *) break ;;
                esac
            done
            echo "value:$digits"
            return
        ;;
        q|Q)
            echo "quit:"
            return
        ;;
        *)
            IFS= read -r rest || true
            echo "value:${key}${rest}"
            return
        ;;
    esac
}

ui_spinner_run() {
    local message="$1"
    shift
    if ! ui_is_tty; then
        "$@"
        return $?
    fi

    local log_file
    log_file=$(mktemp)
    "$@" >"$log_file" 2>&1 &
    local pid=$!
    local frames=("â ‹" "â ™" "â ¹" "â ¸" "â ¼" "â ´" "â ¦" "â §" "â ‡" "â ")
    local i=0
    while kill -0 "$pid" >/dev/null 2>&1; do
        printf "\r"
        ui_color "38;5;45;1" "${frames[$((i % ${#frames[@]}))]}"
        printf " %s" "$message"
        sleep 0.08
        i=$((i + 1))
    done

    local status=0
    wait "$pid" || status=$?
    printf "\r\033[K"
    if [ "$status" -eq 0 ]; then
        ui_color "38;5;82;1" "âœ“"
        printf " %s\n" "$message"
        rm -f "$log_file"
        return 0
    fi

    ui_color "38;5;196;1" "âœ—"
    printf " %s\n" "$message"
    tail -n 80 "$log_file" >&2 || true
    rm -f "$log_file"
    return "$status"
}

ensure_env_file() {
    mkdir -p "$(dirname "$ENV_FILE")"
    touch "$ENV_FILE"
}

set_env_value() {
    local key="$1"
    local value="$2"
    value=$(echo "$value" | sed 's/^"//;s/"$//')
    ensure_env_file
    if grep -qE "^[[:space:]]*${key}[[:space:]]*=" "$ENV_FILE" 2>/dev/null; then
        sed -i "s|^[[:space:]]*${key}[[:space:]]*=.*|${key} = \"${value}\"|" "$ENV_FILE"
    else
        echo "${key} = \"${value}\"" >> "$ENV_FILE"
    fi
}

get_env_value() {
    local key="$1"
    if [ ! -f "$ENV_FILE" ]; then
        return
    fi

    grep -E "^[[:space:]]*${key}[[:space:]]*=" "$ENV_FILE" 2>/dev/null \
        | tail -n 1 \
        | sed -E 's/^[^=]+=//; s/^[[:space:]]*//; s/[[:space:]]*$//; s/^"//; s/"$//'
}

extract_container_name() {
    local compose_file="$1"
    if [ ! -f "$compose_file" ]; then
        return
    fi
    local match
    match=$(grep -m1 "container_name" "$compose_file" 2>/dev/null | awk -F: '{gsub(/["[:space:]]/, "", $2); print $2}')
    if [ -n "$match" ]; then
        echo "$match"
    fi
}

add_discovered_node_instance() {
    local dir="$1"
    local name="$2"
    local existing

    for existing in "${DISCOVERED_NODE_PATHS[@]}"; do
        if [ "$existing" = "$dir" ]; then
            return
        fi
    done

    if [ -z "$name" ]; then
        name=$(basename "$dir")
    fi
    DISCOVERED_NODE_PATHS+=("$dir")
    DISCOVERED_NODE_NAMES+=("$name")
}

discover_node_instances() {
    DISCOVERED_NODE_PATHS=()
    DISCOVERED_NODE_NAMES=()
    while IFS= read -r -d '' compose; do
        if ! grep -Eqi "(ghcr.io/devprogrmer|antimagepanel)/antimage-node" "$compose"; then
            continue
        fi
        local dir name
        dir=$(dirname "$compose")
        name=$(extract_container_name "$compose")
        if [ -z "$name" ]; then
            name=$(basename "$dir")
        fi
        add_discovered_node_instance "$dir" "$name"
    done < <(find "$NODE_DISCOVERY_BASE" -mindepth 1 -maxdepth 2 -type f -name "docker-compose.yml" -print0 2>/dev/null || true)

    while IFS= read -r -d '' mode_file; do
        local dir
        dir=$(dirname "$mode_file")
        add_discovered_node_instance "$dir" "$(basename "$dir")"
    done < <(find "$NODE_DISCOVERY_BASE" -mindepth 1 -maxdepth 2 -type f -name ".install-mode" -print0 2>/dev/null || true)

    while IFS= read -r -d '' binary_file; do
        local dir
        dir=$(dirname "$(dirname "$binary_file")")
        add_discovered_node_instance "$dir" "$(basename "$dir")"
    done < <(find "$NODE_DISCOVERY_BASE" -mindepth 2 -maxdepth 3 -type f -path "*/bin/antimage-node" -print0 2>/dev/null || true)
}

prompt_node_selection() {
    discover_node_instances
    local count=${#DISCOVERED_NODE_PATHS[@]}
    if [ "$count" -eq 0 ]; then
        colorized_echo red "No AntiMage-node installations detected under $NODE_DISCOVERY_BASE."
        colorized_echo yellow "Specify the node with --name <node-name> or install the node first."
        exit 1
    fi
    if [ "$count" -eq 1 ]; then
        APP_NAME="${DISCOVERED_NODE_NAMES[0]}"
        APP_DIR="${DISCOVERED_NODE_PATHS[0]}"
        return
    fi

    colorized_echo cyan "Select the AntiMage-node instance:"
    local idx=0
    for dir in "${DISCOVERED_NODE_PATHS[@]}"; do
        local display="${DISCOVERED_NODE_NAMES[$idx]}"
        printf "  %d) %s (%s)\n" $((idx + 1)) "$display" "$dir"
        idx=$((idx + 1))
    done
    local selection
    while true; do
        read -rp "Choice [1-$count]: " selection
        if [[ "$selection" =~ ^[0-9]+$ ]] && [ "$selection" -ge 1 ] && [ "$selection" -le "$count" ]; then
            local chosen=$((selection - 1))
            APP_NAME="${DISCOVERED_NODE_NAMES[$chosen]}"
            APP_DIR="${DISCOVERED_NODE_PATHS[$chosen]}"
            break
        fi
        echo "Invalid choice."
    done
}

set_app_context

set_branch_variables() {
    local selected_branch="${1:-master}"
    case "$selected_branch" in
        dev|development)
            BRANCH="dev"
            IMAGE_TAG="dev"
            DOCKER_IMAGE="ghcr.io/devprogrmer/antimage-node:dev"
        ;;
        *)
            BRANCH="master"
            IMAGE_TAG="latest"
            DOCKER_IMAGE="ghcr.io/devprogrmer/antimage-node:latest"
        ;;
    esac
    SCRIPT_BRANCH="$BRANCH"
    if [ "$BRANCH" = "dev" ]; then
        ANTIMAGE_REF="dev"
    else
        ANTIMAGE_REF="${ANTIMAGE_SCRIPT_REF:-master}"
    fi
    if [ "${ANTIMAGE_SCRIPT_BASE_URL_EXPLICIT:-0}" != "1" ]; then
        ANTIMAGE_SCRIPT_BASE_URL="https://raw.githubusercontent.com/${ANTIMAGE_REPO}/${ANTIMAGE_REF}/scripts/antimage"
    fi
    SCRIPT_URL="$ANTIMAGE_SCRIPT_BASE_URL/$ANTIMAGE_NODE_SCRIPT_SOURCE_FILE"
}

prompt_branch_selection() {
    local question
    if [[ "$BRANCH" == "dev" ]]; then
        question="Keep using the dev branch? (Y/n): "
    else
        question="Do you want to install AntiMage-node using the dev branch? (y/N): "
    fi
    read -p "$question" -r branch_answer
    if [[ "$BRANCH" == "dev" ]]; then
        if [[ -z "$branch_answer" || "$branch_answer" =~ ^[Yy]$ ]]; then
            set_branch_variables dev
        else
            set_branch_variables master
        fi
    else
        if [[ "$branch_answer" =~ ^[Yy]$ ]]; then
            set_branch_variables dev
        else
            set_branch_variables master
        fi
    fi
    colorized_echo blue "Selected branch: $BRANCH (image tag: $IMAGE_TAG)"
}

normalize_install_mode() {
    local mode
    mode=$(printf '%s' "${1:-}" | tr '[:upper:]' '[:lower:]')
    case "$mode" in
        docker|dockerized|compose)
            echo "docker"
        ;;
        binary|bin|native)
            echo "binary"
        ;;
        "")
            echo ""
        ;;
        *)
            colorized_echo red "Invalid install mode: $1" >&2
            colorized_echo yellow "Valid modes are: docker, binary" >&2
            exit 1
        ;;
    esac
}

script_install_mode() {
    case "${ANTIMAGE_NODE_SCRIPT_FLAVOR:-docker}" in
        docker|dockerized|compose)
            echo "docker"
        ;;
        binary|bin|native)
            echo "binary"
        ;;
        mixed|"")
            echo ""
        ;;
        *)
            colorized_echo red "Invalid node script flavor: $ANTIMAGE_NODE_SCRIPT_FLAVOR" >&2
            exit 1
        ;;
    esac
}

get_install_mode() {
    if [ -f "$INSTALL_MODE_FILE" ]; then
        normalize_install_mode "$(tr -d '[:space:]' < "$INSTALL_MODE_FILE")"
        return
    fi
    if [ -x "$BINARY_NODE" ] || [ -f "$BINARY_SERVICE_UNIT" ]; then
        echo "binary"
        return
    fi
    if [ -f "$COMPOSE_FILE" ]; then
        echo "docker"
        return
    fi
    local forced_mode
    forced_mode=$(script_install_mode)
    if [ -n "$forced_mode" ]; then
        echo "$forced_mode"
        return
    fi
    echo "docker"
}

is_binary_install() {
    [ "$(get_install_mode)" = "binary" ]
}

select_install_mode() {
    local requested_mode
    local forced_mode
    forced_mode=$(script_install_mode)
    requested_mode=$(normalize_install_mode "${1:-${ANTIMAGE_NODE_INSTALL_MODE:-}}")

    if [ -n "$forced_mode" ]; then
        if [ -n "$requested_mode" ] && [ "$requested_mode" != "$forced_mode" ]; then
            colorized_echo red "This script is dedicated to ${forced_mode} installs. Use the matching AntiMage-node script for $requested_mode." >&2
            exit 1
        fi
        echo "$forced_mode"
        return
    fi

    if [ -n "$requested_mode" ]; then
        echo "$requested_mode"
        return
    fi

    if [ ! -t 0 ]; then
        echo "docker"
        return
    fi

    colorized_echo cyan "Select AntiMage-node installation mode:" >&2
    colorized_echo yellow "  1) Dockerized" >&2
    colorized_echo yellow "  2) Binary (native systemd service, no Docker)" >&2
    read -r -p "Install mode [1]: " install_mode_answer

    case "$install_mode_answer" in
        2|binary|Binary|bin|native)
            echo "binary"
        ;;
        ""|1|docker|Docker|dockerized|compose)
            echo "docker"
        ;;
        *)
            colorized_echo red "Invalid install mode selection."
            exit 1
        ;;
    esac
}

ensure_script_matches_installed_mode() {
    local forced_mode
    local installed_mode
    forced_mode=$(script_install_mode)
    if [ -z "$forced_mode" ] || [ ! -d "$APP_DIR" ]; then
        return
    fi
    installed_mode=$(get_install_mode)
    if [ "$installed_mode" != "$forced_mode" ]; then
        colorized_echo red "This AntiMage-node installation is in ${installed_mode} mode, but ${0##*/} is the ${forced_mode} script."
        if [ "$installed_mode" = "binary" ]; then
            colorized_echo yellow "Use the AntiMage-node binary script."
        else
            colorized_echo yellow "Use the AntiMage-node Docker script."
        fi
        exit 1
    fi
}

select_node_version() {
    local requested_version="${1:-}"
    local install_mode="${2:-docker}"

    if [ -n "$requested_version" ]; then
        SELECTED_NODE_VERSION="$requested_version"
        return
    fi

    if [ ! -t 0 ]; then
        SELECTED_NODE_VERSION="latest"
        return
    fi

    colorized_echo cyan "Select AntiMage-node release channel for ${install_mode} mode:" >&2
    colorized_echo yellow "  1) latest" >&2
    if [ "$install_mode" = "binary" ]; then
        colorized_echo yellow "  2) dev (latest successful binary build from ${ANTIMAGE_NODE_BINARY_DEV_BRANCH})" >&2
    else
        colorized_echo yellow "  2) dev (Docker image tag dev)" >&2
    fi
    read -r -p "Release channel [1]: " node_version_answer

    case "$node_version_answer" in
        2|dev|Dev)
            SELECTED_NODE_VERSION="dev"
        ;;
        ""|1|latest|Latest|stable|Stable)
            SELECTED_NODE_VERSION="latest"
        ;;
        *)
            colorized_echo red "Invalid release channel selection."
            exit 1
        ;;
    esac
}

BRANCH="master"
IMAGE_TAG="latest"
SCRIPT_BRANCH="master"
DOCKER_IMAGE="ghcr.io/devprogrmer/antimage-node:latest"
SCRIPT_URL="$ANTIMAGE_SCRIPT_BASE_URL/$ANTIMAGE_NODE_SCRIPT_SOURCE_FILE"
if [ -f "$BRANCH_FILE" ]; then
    saved_branch=$(tr -d '[:space:]' < "$BRANCH_FILE")
    if [[ -n "$saved_branch" ]]; then
        set_branch_variables "$saved_branch"
    else
        set_branch_variables "$BRANCH"
    fi
else
    set_branch_variables "$BRANCH"
fi


check_running_as_root() {
    if [ "$(id -u)" != "0" ]; then
        colorized_echo red "This command must be run as root."
        exit 1
    fi
}

detect_os() {
    # Detect the operating system
    if [ -f /etc/lsb-release ]; then
        OS=$(lsb_release -si)
        elif [ -f /etc/os-release ]; then
        OS=$(awk -F= '/^NAME/{print $2}' /etc/os-release | tr -d '"')
        elif [ -f /etc/redhat-release ]; then
        OS=$(cat /etc/redhat-release | awk '{print $1}')
        elif [ -f /etc/arch-release ]; then
        OS="Arch"
    else
        colorized_echo red "Unsupported operating system"
        exit 1
    fi
}

remove_broken_xanmod_apt_sources() {
    local matches
    matches=$(grep -RIlE 'deb\.xanmod\.org|xanmod\.org' /etc/apt/sources.list /etc/apt/sources.list.d 2>/dev/null || true)
    if [ -z "$matches" ]; then
        return 1
    fi
    colorized_echo yellow "Removing broken XanMod apt source entries"
    while IFS= read -r file; do
        [ -n "$file" ] || continue
        case "$file" in
            /etc/apt/sources.list)
                sed -i.bak '/deb\.xanmod\.org/d;/xanmod\.org/d' "$file"
            ;;
            /etc/apt/sources.list.d/*)
                rm -f "$file"
            ;;
        esac
    done <<< "$matches"
    return 0
}

apt_update_with_repo_repair() {
    local log_file
    log_file=$(mktemp)
    if DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=a "$PKG_MANAGER" "$@" update -qq >"$log_file" 2>&1; then
        rm -f "$log_file"
        return 0
    fi
    cat "$log_file" >&2
    if grep -qiE 'deb\.xanmod\.org|xanmod.*release file|does not have a release file' "$log_file" && remove_broken_xanmod_apt_sources; then
        rm -f "$log_file"
        DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=a "$PKG_MANAGER" "$@" update -qq
        return
    fi
    rm -f "$log_file"
    return 1
}

detect_and_update_package_manager() {
    if [[ "$OS" == "Ubuntu"* ]] || [[ "$OS" == "Debian"* ]]; then
        PKG_MANAGER="apt-get"
        ui_spinner_run "Updating package index" apt_update_with_repo_repair
    elif [[ "$OS" == "CentOS"* ]] || [[ "$OS" == "AlmaLinux"* ]]; then
        PKG_MANAGER="yum"
        ui_spinner_run "Updating package index" "$PKG_MANAGER" update -y -q
        ui_spinner_run "Installing EPEL repository" "$PKG_MANAGER" install -y -q epel-release
    elif [[ "$OS" == "Fedora"* ]]; then
        PKG_MANAGER="dnf"
        ui_spinner_run "Updating package index" "$PKG_MANAGER" update -q -y
    elif [[ "$OS" == "Arch"* ]]; then
        PKG_MANAGER="pacman"
        ui_spinner_run "Updating package index" "$PKG_MANAGER" -Sy --noconfirm --quiet
    elif [[ "$OS" == "openSUSE"* ]]; then
        PKG_MANAGER="zypper"
        ui_spinner_run "Updating package index" "$PKG_MANAGER" refresh --quiet
    else
        colorized_echo red "Unsupported operating system"
        exit 1
    fi
}


detect_compose() {
    # Check if docker compose command exists
    if docker compose >/dev/null 2>&1; then
        COMPOSE='docker compose'
        elif docker-compose >/dev/null 2>&1; then
        COMPOSE='docker-compose'
    else
        colorized_echo red "docker compose not found"
        exit 1
    fi
}

install_package_impl() {
    local PACKAGE="$1"
    local reinstall="${2:-}"
    if [[ "$OS" == "Ubuntu"* ]] || [[ "$OS" == "Debian"* ]]; then
        DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=a $PKG_MANAGER -y -qq install ${reinstall:+--reinstall} "$PACKAGE" \
            -o Dpkg::Options::="--force-confdef" \
            -o Dpkg::Options::="--force-confold"
    elif [[ "$OS" == "CentOS"* ]] || [[ "$OS" == "AlmaLinux"* ]]; then
        $PKG_MANAGER install -y -q "$PACKAGE"
    elif [[ "$OS" == "Fedora"* ]]; then
        $PKG_MANAGER install -y -q "$PACKAGE"
    elif [[ "$OS" == "Arch"* ]]; then
        $PKG_MANAGER -S --noconfirm --quiet "$PACKAGE"
    elif [[ "$OS" == "openSUSE"* ]]; then
        PKG_MANAGER="zypper"
        $PKG_MANAGER --quiet install -y "$PACKAGE"
    else
        colorized_echo red "Unsupported operating system"
        exit 1
    fi
}

install_package () {
    if [ -z "$PKG_MANAGER" ]; then
        detect_and_update_package_manager
    fi

    local PACKAGE="$1"
    ui_spinner_run "Installing $PACKAGE" install_package_impl "$PACKAGE"
}

reinstall_package() {
    if [ -z "$PKG_MANAGER" ]; then
        detect_and_update_package_manager
    fi
    ui_spinner_run "Reinstalling $1" install_package_impl "$1" reinstall
}

package_available() {
    if [ -z "$PKG_MANAGER" ]; then
        detect_and_update_package_manager
    fi

    local PACKAGE="$1"
    if [[ "$OS" == "Ubuntu"* ]] || [[ "$OS" == "Debian"* ]]; then
        apt-cache show "$PACKAGE" >/dev/null 2>&1
    elif [[ "$OS" == "CentOS"* ]] || [[ "$OS" == "AlmaLinux"* ]]; then
        yum info "$PACKAGE" >/dev/null 2>&1
    elif [[ "$OS" == "Fedora"* ]]; then
        dnf info "$PACKAGE" >/dev/null 2>&1
    elif [[ "$OS" == "Arch"* ]]; then
        pacman -Si "$PACKAGE" >/dev/null 2>&1
    elif [[ "$OS" == "openSUSE"* ]]; then
        zypper --quiet info "$PACKAGE" >/dev/null 2>&1
    else
        return 1
    fi
}

ensure_vpn_host_prerequisites() {
    detect_os
    local packages=()

    if ! command -v modprobe >/dev/null 2>&1; then
        packages+=("kmod")
    fi

    if ! command -v sysctl >/dev/null 2>&1; then
        if [[ "$OS" == "CentOS"* ]] || [[ "$OS" == "AlmaLinux"* ]] || [[ "$OS" == "Fedora"* ]] || [[ "$OS" == "Arch"* ]]; then
            packages+=("procps-ng")
        else
            packages+=("procps")
        fi
    fi

    for package in "${packages[@]}"; do
        install_package "$package"
    done

    if [ ! -c /dev/net/tun ] && command -v modprobe >/dev/null 2>&1; then
        modprobe tun >/dev/null 2>&1 || true
    fi

    if [ ! -c /dev/net/tun ]; then
        mkdir -p /dev/net
        mknod /dev/net/tun c 10 200 >/dev/null 2>&1 || true
        chmod 0666 /dev/net/tun >/dev/null 2>&1 || true
    fi

    if [ ! -c /dev/net/tun ]; then
        colorized_echo red "Unable to provision /dev/net/tun; OpenVPN cannot operate on this node."
        return 1
    fi

    if command -v modprobe >/dev/null 2>&1; then
        if ! modprobe wireguard >/dev/null 2>&1; then
            colorized_echo yellow "WireGuard kernel module could not be loaded automatically; WireGuard availability will be verified by the node runtime."
        fi
    fi

    mkdir -p /etc/sysctl.d
    {
        echo "net.ipv4.ip_forward=1"
        if [ -e /proc/sys/net/ipv6/conf/all/forwarding ]; then
            echo "net.ipv6.conf.all.forwarding=1"
        fi
    } > /etc/sysctl.d/99-antimage-node-vpn.conf

    if ! sysctl -w net.ipv4.ip_forward=1 >/dev/null; then
        colorized_echo red "Unable to enable IPv4 forwarding required by VPN inbounds."
        return 1
    fi

    if [ -e /proc/sys/net/ipv6/conf/all/forwarding ]; then
        sysctl -w net.ipv6.conf.all.forwarding=1 >/dev/null 2>&1 || \
            colorized_echo yellow "Unable to enable IPv6 forwarding automatically."
    fi
}

ensure_l2tp_kernel_modules() {
    if ! command -v modprobe >/dev/null 2>&1; then
        colorized_echo red "modprobe is required for L2TP kernel support."
        return 1
    fi

    local required_modules=(ppp_generic pppox l2tp_ppp)
    local optional_modules=(pppol2tp af_key nf_tproxy_ipv4)
    local module
    local load_failed=false

    for module in "${required_modules[@]}"; do
        if ! modprobe "$module" >/dev/null 2>&1; then
            load_failed=true
            break
        fi
    done

    if [ "$load_failed" = true ] && { [[ "$OS" == "Ubuntu"* ]] || [[ "$OS" == "Debian"* ]]; }; then
        local kernel_release
        kernel_release="$(uname -r)"

        if [ -n "$kernel_release" ] && package_available "linux-modules-extra-${kernel_release}"; then
            colorized_echo yellow "Installing L2TP kernel modules for ${kernel_release}..."
            install_package "linux-modules-extra-${kernel_release}"
        fi
    fi

    for module in "${required_modules[@]}"; do
        if ! modprobe "$module" >/dev/null 2>&1; then
            colorized_echo red "Unable to load required L2TP kernel module: ${module}"
            return 1
        fi
    done

    for module in "${optional_modules[@]}"; do
        modprobe "$module" >/dev/null 2>&1 || true
    done

    mkdir -p /etc/modules-load.d
    printf '%s\n' \
        ppp_generic \
        pppox \
        l2tp_ppp \
        pppol2tp \
        > /etc/modules-load.d/99-antimage-l2tp.conf

    return 0
}
ensure_vpn_binary_prerequisites() {
    ensure_vpn_host_prerequisites

    local packages=()

    if ! command -v openvpn >/dev/null 2>&1; then
        packages+=("openvpn")
    fi

    if ! command -v wg >/dev/null 2>&1; then
        packages+=("wireguard-tools")
    fi

    if ! command -v nft >/dev/null 2>&1; then
        packages+=("nftables")
    fi

    if ! command -v iptables >/dev/null 2>&1; then
        packages+=("iptables")
    fi

    if ! command -v ip >/dev/null 2>&1; then
        if [[ "$OS" == "CentOS"* ]] || [[ "$OS" == "AlmaLinux"* ]] || [[ "$OS" == "Fedora"* ]]; then
            packages+=("iproute")
        else
            packages+=("iproute2")
        fi
    fi

    if ! command -v xl2tpd >/dev/null 2>&1; then
        packages+=("xl2tpd")
    fi

    if ! command -v pppd >/dev/null 2>&1; then
        packages+=("ppp")
    fi

    pptp_required=true
    if ! command -v pptpd >/dev/null 2>&1 && package_available "pptpd"; then
        packages+=("pptpd")
    elif ! command -v pptpd >/dev/null 2>&1; then
        pptp_required=false
        colorized_echo yellow "Package pptpd is not available for this distribution; PPTP runtime will remain unavailable on this node."
    fi

    if ! command -v ipsec >/dev/null 2>&1; then
        packages+=("strongswan" "strongswan-pki")
    elif ! command -v pki >/dev/null 2>&1; then
        packages+=("strongswan-pki")
    fi

    # AntiMage IKEv2 accounting/online detection uses the strongSwan VICI API
    # through swanctl --list-sas.
    if ! command -v swanctl >/dev/null 2>&1; then
        if package_available "strongswan-swanctl"; then
            packages+=("strongswan-swanctl")
        fi
    fi

    # EAP-MSCHAPv2 is required for native IKEv2 username/password auth.
    if ! find /usr/lib /usr/lib64 -type f         -name 'libstrongswan-eap-mschapv2.so'         -print -quit 2>/dev/null | grep -q .; then
        if package_available "libcharon-extauth-plugins"; then
            packages+=("libcharon-extauth-plugins")
        fi
    fi

    if ! command -v ocserv >/dev/null 2>&1 ||
       ! command -v ocpasswd >/dev/null 2>&1 ||
       ! command -v occtl >/dev/null 2>&1; then
        if command -v ocserv >/dev/null 2>&1 &&
           { [[ "$OS" == "Ubuntu"* ]] || [[ "$OS" == "Debian"* ]]; }; then
            reinstall_package "ocserv"
        else
            packages+=("ocserv")
        fi
    fi

    for package in "${packages[@]}"; do
        install_package "$package"
    done

    ensure_l2tp_kernel_modules

    local missing=()
    local command_name

    for command_name in openvpn wg ip iptables nft sysctl xl2tpd pppd ipsec pki swanctl ocserv ocpasswd occtl; do
        if ! command -v "$command_name" >/dev/null 2>&1; then
            missing+=("$command_name")
        fi
    done

    if ! find /usr/lib /usr/lib64 -type f         -name 'libstrongswan-eap-mschapv2.so'         -print -quit 2>/dev/null | grep -q .; then
        missing+=("strongswan-eap-mschapv2")
    fi

    if [ "$pptp_required" = true ] && ! command -v pptpd >/dev/null 2>&1; then
        missing+=("pptpd")
    fi

    if [ "${#missing[@]}" -ne 0 ]; then
        colorized_echo red "VPN runtime prerequisites are still missing: ${missing[*]}"
        return 1
    fi
}

ensure_haproxy_prerequisites() {
    if ! command -v haproxy >/dev/null 2>&1; then
        detect_os
        install_package haproxy
    fi
}

ensure_python3_venv() {
    detect_os
    if [[ "$OS" == "Ubuntu"* ]] || [[ "$OS" == "Debian"* ]]; then
        PY_VER=$(python3 -c 'import sys; print(f"{sys.version_info.major}.{sys.version_info.minor}")' 2>/dev/null || echo "3")
        install_package "python${PY_VER}-venv" || install_package python3-venv
    else
        install_package python3-venv
    fi
}

install_docker() {
    # Install Docker and Docker Compose using the official installation script
    colorized_echo blue "Installing Docker"
    curl -fsSL https://get.docker.com | sh
    colorized_echo green "Docker installed successfully"
}

detect_node_binary_arch() {
    case "$(uname -m)" in
        amd64|x86_64)
            echo "amd64"
        ;;
        arm64|aarch64)
            echo "arm64"
        ;;
        i386|i486|i586|i686)
            echo "386"
        ;;
        armv5l|armv5tel|armv5tejl)
            echo "armv5"
        ;;
        armv6l|armv6)
            echo "armv6"
        ;;
        armv7l|armv7)
            echo "armv7"
        ;;
        s390x)
            echo "s390x"
        ;;
        *)
            colorized_echo red "AntiMage-node binary install is not available for architecture: $(uname -m)" >&2
            colorized_echo yellow "Use Dockerized install for this server." >&2
            exit 1
        ;;
    esac
}

get_node_binary_release_asset_metadata() {
    local node_version="$1"
    local binary_arch="$2"
    local release_api
    local release_payload
    local resolved_tag
    local node_asset_name
    local node_asset_url

    if [ "$node_version" = "latest" ]; then
        release_api="https://api.github.com/repos/${ANTIMAGE_NODE_RELEASE_REPO}/releases/latest"
    else
        release_api="https://api.github.com/repos/${ANTIMAGE_NODE_RELEASE_REPO}/releases/tags/${node_version}"
    fi

    release_payload=$(curl -fsSL "$release_api") || {
        colorized_echo red "Unable to read AntiMage-node release metadata: $release_api" >&2
        exit 1
    }

    resolved_tag=$(echo "$release_payload" | jq -r '.tag_name // empty')
    node_asset_name="antimage-node-${resolved_tag}-linux-${binary_arch}"

    node_asset_url=$(echo "$release_payload" | jq -r --arg name "$node_asset_name" '
        .assets[]?
        | select(.name == $name)
        | .browser_download_url
    ' | head -n 1)

    if [ -z "$node_asset_url" ] || [ "$node_asset_url" = "null" ]; then
        colorized_echo red "No AntiMage-node binary release assets found for linux-${binary_arch}." >&2
        colorized_echo yellow "Use --dev after the dev binary workflow succeeds, or use Dockerized install." >&2
        exit 1
    fi

    printf '%s|%s\n' "${resolved_tag:-$node_version}" "$node_asset_url"
}

get_node_binary_dev_artifact_metadata() {
    local binary_arch="$1"
    local release_api
    local release_payload
    local release_asset_name
    local release_asset_url
    local release_target

    release_asset_name="antimage-node-dev-linux-${binary_arch}"
    release_api="https://api.github.com/repos/${ANTIMAGE_NODE_RELEASE_REPO}/releases/tags/${ANTIMAGE_NODE_BINARY_DEV_RELEASE_TAG}"
    if release_payload=$(curl -fsSL "$release_api" 2>/dev/null); then
        release_asset_url=$(echo "$release_payload" | jq -r --arg name "$release_asset_name" '
            .assets[]?
            | select(.name == $name)
            | .browser_download_url
        ' | head -n 1)
        if [ -n "$release_asset_url" ] && [ "$release_asset_url" != "null" ]; then
            release_target=$(echo "$release_payload" | jq -r '.target_commitish // empty')
            if [[ "$release_target" =~ ^[0-9a-fA-F]{7,40}$ ]]; then
                printf '%s|%s\n' "dev-${release_target:0:7}" "$release_asset_url"
            else
                printf '%s|%s\n' "dev-${ANTIMAGE_NODE_BINARY_DEV_BRANCH}" "$release_asset_url"
            fi
            return 0
        fi
    fi

    # A failed HEAD request must not divert a usable release download to Actions artifacts.
    release_asset_url="https://github.com/${ANTIMAGE_NODE_RELEASE_REPO}/releases/download/${ANTIMAGE_NODE_BINARY_DEV_RELEASE_TAG}/${release_asset_name}"
    printf '%s|%s\n' "dev-${ANTIMAGE_NODE_BINARY_DEV_BRANCH}" "$release_asset_url"
}

get_node_binary_pinned_dev_artifact_metadata() {
    local version="$1"
    local binary_arch="$2"
    local release_api="https://api.github.com/repos/${ANTIMAGE_NODE_RELEASE_REPO}/releases/tags/${ANTIMAGE_NODE_BINARY_DEV_RELEASE_TAG}"
    local release_payload
    local asset_name="antimage-node-${version}-linux-${binary_arch}"
    local asset_url

    release_payload=$(curl -fsSL "$release_api") || {
        colorized_echo red "Unable to read AntiMage-node dev build metadata: $release_api" >&2
        return 1
    }
    asset_url=$(echo "$release_payload" | jq -r --arg name "$asset_name" '.assets[]? | select(.name == $name) | .browser_download_url' | head -n 1)
    if [ -z "$asset_url" ] || [ "$asset_url" = "null" ]; then
        colorized_echo red "The requested AntiMage-node dev build $version is unavailable for linux-${binary_arch}." >&2
        return 1
    fi
    printf '%s|%s\n' "$version" "$asset_url"
}

verify_node_binary_checksum() {
    local asset_url="$1"
    local asset_path="$2"
    local asset_name="${asset_url##*/}"
    local checksums_name="checksums.txt"
    local expected actual
    local checksums_file="${asset_path}.checksums"

    if [[ "$asset_name" =~ ^antimage-node-(dev-[a-f0-9]{7,40})-linux- ]]; then
        checksums_name="checksums-${BASH_REMATCH[1]}.txt"
    fi
    local checksums_url="${asset_url%/*}/${checksums_name}"

    curl -fsSL --retry 2 --connect-timeout 15 "$checksums_url" -o "$checksums_file" || {
        colorized_echo red "Unable to download checksum metadata for ${asset_name}." >&2
        return 1
    }
    expected=$(awk -v name="$asset_name" '$2 == name { print $1; exit }' "$checksums_file")
    rm -f "$checksums_file"
    if [[ ! "$expected" =~ ^[a-fA-F0-9]{64}$ ]]; then
        colorized_echo red "Checksum metadata does not contain a valid SHA256 for ${asset_name}." >&2
        return 1
    fi
    actual=$(sha256sum "$asset_path" | awk '{print $1}')
    if [ "${actual,,}" != "${expected,,}" ]; then
        colorized_echo red "SHA256 verification failed for ${asset_name}." >&2
        return 1
    fi
}

write_node_binary_release_metadata() {
    local resolved_version="$1"
    local binary_arch="$2"
    local asset_url="$3"

    local resolved_metadata="${RESOLVED_BUILD_JSON:-}"
    [ -n "$resolved_metadata" ] || resolved_metadata='{}'
    jq -n \
        --argjson build "$resolved_metadata" \
        --arg image "antimage-node (binary)" \
        --arg tag "$resolved_version" \
        --arg asset_url "$asset_url" \
        --arg arch "linux-${binary_arch}" \
        --arg node_binary "$BINARY_NODE" \
        --arg installed_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
        '{
            install_mode: "binary",
            commit: ($build.commit // ""),
            sha256: ($build.sha256 // ""),
            size: ($build.size // null),
            os: ($build.os // "linux"),
            architecture: ($build.arch // ""),
            image: $image,
            tag: $tag,
            asset_url: $asset_url,
            arch: $arch,
            node_binary: $node_binary,
            installed_at: $installed_at
        }' > "$BINARY_METADATA_FILE"
}

create_binary_antimage_node_service() {
    cat > "$BINARY_SERVICE_UNIT" <<EOF
[Unit]
Description=AntiMage-node
After=network.target

[Service]
Type=simple
User=root
WorkingDirectory=$APP_DIR
Environment=ANTIMAGE_NODE_APP_NAME=$APP_NAME
Environment=ANTIMAGE_NODE_APP_DIR=$APP_DIR
Environment=ANTIMAGE_NODE_DATA_DIR=$DATA_DIR
Environment=ANTIMAGE_DATA_DIR=$DATA_DIR
EnvironmentFile=-$APP_DIR/.env
Environment=ANTIMAGE_NODE_INSTALL_MODE=binary
Environment=ANTIMAGE_NODE_BINARY_METADATA_FILE=$BINARY_METADATA_FILE
ExecStart=$BINARY_NODE
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

    systemctl daemon-reload
}

refresh_xray_installer_script() {
    local temp_script
    mkdir -p "$APP_DIR/scripts"
    temp_script=$(mktemp) || return 1
    if ! curl -fsSL "$ANTIMAGE_SCRIPT_BASE_URL/install_latest_xray.sh" -o "$temp_script"; then
        rm -f "$temp_script"
        return 1
    fi
    sed -i 's/\r$//' "$temp_script"
    install -m 755 "$temp_script" "$APP_DIR/scripts/install_latest_xray.sh"
    local result=$?
    rm -f "$temp_script"
    return "$result"
}

install_latest_xray_for_binary_node() {
    mkdir -p "$DATA_DIR/xray-core"
    colorized_echo blue "Installing Xray core ${XRAY_CORE_VERSION:-$DEFAULT_XRAY_CORE_VERSION} for binary node"
    refresh_xray_installer_script || return 1
    ANTIMAGE_DATA_DIR="$DATA_DIR" XRAY_INSTALL_DIR="$DATA_DIR/xray-core" XRAY_ASSETS_DIR="$DATA_DIR/xray-core" XRAY_CORE_VERSION="${XRAY_CORE_VERSION:-$DEFAULT_XRAY_CORE_VERSION}" bash "$APP_DIR/scripts/install_latest_xray.sh"
}

read_node_certificate_bundle() {
    local bundle_file
    local bundle_started=0
    local bundle_completed=0
    local line=""
    bundle_file=$(mktemp)
    : > "$bundle_file"

    echo -e "Paste the Node install bundle from the panel, press ENTER on a new line when finished: "
    while IFS= read -r line || [ -n "$line" ]; do
        line="${line%$'\r'}"
        if [[ -z $line ]]; then
            if [ "$bundle_started" -eq 0 ]; then
                break
            fi
            if grep -q -- "-----END CERTIFICATE-----" "$bundle_file" && grep -Eq -- "-----END( [^-]+)? PRIVATE KEY-----" "$bundle_file"; then
                bundle_completed=1
                break
            fi
            continue
        fi
        bundle_started=1
        echo "$line" >>"$bundle_file"
        if grep -q -- "-----END CERTIFICATE-----" "$bundle_file" && grep -Eq -- "-----END( [^-]+)? PRIVATE KEY-----" "$bundle_file"; then
            bundle_completed=1
            break
        fi
    done

    if [ "$bundle_completed" -ne 1 ]; then
        colorized_echo red "Node install bundle is incomplete. Paste the full bundle shown by the panel."
        rm -f "$bundle_file"
        exit 1
    fi

    awk 'BEGIN{capture=0} /-----BEGIN CERTIFICATE-----/{capture=1} capture{print} /-----END CERTIFICATE-----/{exit}' "$bundle_file" >"$CERT_FILE"
    awk 'BEGIN{capture=0} /-----BEGIN( [^-]+)? PRIVATE KEY-----/{capture=1} capture{print} /-----END( [^-]+)? PRIVATE KEY-----/{exit}' "$bundle_file" >"$CERT_KEY_FILE"
    rm -f "$bundle_file"

    if ! grep -q -- "-----END CERTIFICATE-----" "$CERT_FILE"; then
        colorized_echo red "The bundle does not contain a valid PEM certificate."
        rm -f "$CERT_FILE" "$CERT_KEY_FILE"
        exit 1
    fi
    if ! grep -Eq -- "-----END( [^-]+)? PRIVATE KEY-----" "$CERT_KEY_FILE"; then
        colorized_echo red "The bundle does not contain a valid PEM private key."
        rm -f "$CERT_FILE" "$CERT_KEY_FILE"
        exit 1
    fi

    chmod 600 "$CERT_KEY_FILE"
    colorized_echo green "Node certificate bundle saved to $CERT_FILE and $CERT_KEY_FILE"
}

configure_binary_node_env() {
    mkdir -p "$DATA_DIR" "$APP_DIR"
    echo "$BRANCH" > "$BRANCH_FILE"

    if [ ! -s "$CERT_FILE" ] || [ ! -s "$CERT_KEY_FILE" ]; then
        rm -f "$CERT_FILE" "$CERT_KEY_FILE"
        read_node_certificate_bundle
    fi

    get_occupied_ports

    SERVICE_PORT=$(prompt_node_port_setting "SERVICE_PORT" "SERVICE_PORT" "62050")
    set_env_value "SERVICE_HOST" "0.0.0.0"
    set_env_value "SERVICE_PORT" "$SERVICE_PORT"

    XRAY_API_PORT=$(prompt_node_port_setting "XRAY_API_PORT" "XRAY_API_PORT" "62051" "$SERVICE_PORT")
    set_env_value "XRAY_API_HOST" "127.0.0.1"
    set_env_value "XRAY_API_PORT" "$XRAY_API_PORT"

    set_env_value "ANTIMAGE_DATA_DIR" "$DATA_DIR"
    set_env_value "SSL_CLIENT_CERT_FILE" "$CERT_FILE"
    set_env_value "SSL_CERT_FILE" "$CERT_FILE"
    set_env_value "SSL_KEY_FILE" "$CERT_KEY_FILE"
    set_env_value "XRAY_EXECUTABLE_PATH" "$DATA_DIR/xray-core/xray"
    set_env_value "XRAY_ASSETS_PATH" "$DATA_DIR/xray-core"
}

managed_binary_install() {
    managed_binary_backup verify "$1" "$2" "$3" "$4" >/dev/null || return 1
    python3 - "$@" <<'PY'
import fcntl, hashlib, json, os, pathlib, platform, re, shutil, signal, stat, sys, tempfile, time
app, identity, kind, target = sys.argv[1:5]
app = pathlib.Path(app).resolve()
if not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_.-]{0,95}', identity): raise ValueError('Invalid transaction identity')
arguments = sys.argv[5:]
if not arguments or len(arguments) % 2: raise ValueError('Source/destination pairs required')
root = app / '.update-transactions'
if root.is_symlink(): raise ValueError('Transaction root cannot be a symlink')
root.mkdir(mode=0o700, exist_ok=True)
directory = root / identity
if directory.is_symlink(): raise ValueError('Transaction directory cannot be a symlink')
directory.mkdir(mode=0o700, exist_ok=True)
def sync_dir(path):
    descriptor = os.open(path, os.O_RDONLY | os.O_DIRECTORY)
    try: os.fsync(descriptor)
    finally: os.close(descriptor)
def digest(path):
    value = hashlib.sha256()
    with path.open('rb') as stream:
        while True:
            chunk = stream.read(1048576)
            if not chunk: break
            value.update(chunk)
    return value.hexdigest()
lock = os.open(directory / '.lock', os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
signal.signal(signal.SIGALRM, lambda *_: (_ for _ in ()).throw(TimeoutError('Install deadline exceeded')))
signal.alarm(180)
fcntl.flock(lock, fcntl.LOCK_EX)
records = []
seen = set()
for index in range(0, len(arguments), 2):
    source, destination = map(pathlib.Path, arguments[index:index + 2])
    if destination.is_symlink() or app not in destination.resolve().parents: raise ValueError('Installation escapes application directory')
    if str(destination) in seen: raise ValueError('Duplicate production destination')
    seen.add(str(destination))
    if source.is_symlink() or not source.is_file(): raise ValueError('Installation requires regular artifacts')
    with source.open('rb') as stream:
        header = stream.read(20)
        expected = {'x86_64':62,'i386':3,'i686':3,'aarch64':183,'armv7l':40,'armv6l':40,'ppc64le':21,'s390x':22,'riscv64':243}.get(platform.machine())
        if len(header)<20 or header[:4]!=b'\x7fELF' or header[5] not in (1,2) or int.from_bytes(header[18:20], 'little' if header[5]==1 else 'big')!=expected: raise ValueError('Executable artifact architecture is incompatible')
    attributes = destination.stat() if destination.exists() else source.stat()
    records.append({'source': str(source), 'destination': str(destination), 'sha256': digest(source), 'size': source.stat().st_size, 'mode': 0o755, 'uid': attributes.st_uid, 'gid': attributes.st_gid, 'reference': str(index // 2)})
state_path = directory / 'state.json'
if state_path.is_symlink(): raise ValueError('Installation state cannot be a symlink')
phases = ['artifact_ready', 'backup_verified', 'replacement_ready', 'replacement_committed', 'runtime_restart_required']
identity_record = {'operation_id': identity, 'target_type': kind, 'target_id': target, 'files': [{key:value for key,value in record.items() if key != 'source'} for record in records]}
state = json.loads(state_path.read_text()) if state_path.exists() else {**identity_record, 'phase': 'artifact_ready', 'deadline_at_unix_nanos':time.time_ns()+180_000_000_000}
if any(state.get(key) != value for key,value in identity_record.items()): raise ValueError('Immutable installation target changed')
if state.get('phase') not in phases: raise ValueError('Invalid installation phase')
if phases.index(state['phase']) < phases.index('replacement_committed'):
    remaining = int(state.get('deadline_at_unix_nanos',0))-time.time_ns()
    if remaining <= 0: raise TimeoutError('Persisted installation deadline expired')
    signal.alarm(max(1,(remaining+999_999_999)//1_000_000_000))
def persist(phase):
    if phases.index(state['phase']) > phases.index(phase): return
    state['phase'] = phase
    descriptor, temporary = tempfile.mkstemp(prefix='.state-', dir=directory)
    try:
        with os.fdopen(descriptor,'w') as stream:
            json.dump(state,stream); stream.flush(); os.fsync(stream.fileno())
        os.replace(temporary,state_path); sync_dir(directory)
    finally: pathlib.Path(temporary).unlink(missing_ok=True)
def matches(record, path):
    if not path.is_file() or path.is_symlink(): return False
    attributes = path.stat()
    return attributes.st_size == record['size'] and digest(path) == record['sha256'] and stat.S_IMODE(attributes.st_mode) == record['mode'] and attributes.st_uid == record['uid'] and attributes.st_gid == record['gid']
if phases.index(state['phase']) >= phases.index('replacement_committed'):
    if not all(matches(record,pathlib.Path(record['destination'])) for record in records): raise ValueError('Committed production identity changed; manual recovery required')
else:
    persist('artifact_ready'); persist('backup_verified')
    staged = []
    for record in records:
        destination = pathlib.Path(record['destination'])
        if matches(record,destination): continue
        temporary = destination.parent / ('.install-' + identity + '-' + record['reference'])
        if temporary.is_symlink(): raise ValueError('Staged executable cannot be a symlink')
        if not matches(record,temporary):
            temporary.unlink(missing_ok=True)
            descriptor = os.open(temporary, os.O_CREAT | os.O_EXCL | os.O_WRONLY | os.O_NOFOLLOW, 0o600)
            with os.fdopen(descriptor,'wb') as stream, pathlib.Path(record['source']).open('rb') as original:
                shutil.copyfileobj(original,stream); stream.flush(); os.fchmod(stream.fileno(),record['mode']); os.fchown(stream.fileno(),record['uid'],record['gid']); os.fsync(stream.fileno())
            sync_dir(destination.parent)
        staged.append((record,temporary,destination))
    persist('replacement_ready')
    for record,temporary,destination in staged:
        if not matches(record,temporary): raise ValueError('Staged executable identity changed')
        os.replace(temporary,destination); sync_dir(destination.parent)
    if not all(matches(record,pathlib.Path(record['destination'])) for record in records): raise ValueError('Production installation identity verification failed')
    persist('replacement_committed')
persist('runtime_restart_required')
signal.alarm(0)
os.close(lock)
print(json.dumps(state),flush=True)
PY
}

managed_binary_backup() {
    python3 - "$@" <<'PY'
import hashlib, json, os, pathlib, platform, re, shutil, stat, sys, tempfile, time, fcntl, signal
action, application, identity, target_type, target_id = sys.argv[1:6]
application = pathlib.Path(application).resolve()
if not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_.-]{0,95}', identity):
    raise SystemExit('Invalid backup identity')
root = application / '.update-backups'
if root.is_symlink(): raise SystemExit('Backup root cannot be a symlink')
root.mkdir(mode=0o700, exist_ok=True)
architecture = {'x86_64':'amd64','i386':'386','i686':'386','aarch64':'arm64','armv7l':'arm','armv6l':'arm','ppc64le':'ppc64le','s390x':'s390x','riscv64':'riscv64'}.get(platform.machine(), platform.machine())
backup = root / identity
def sync_file(path):
    with path.open('rb') as stream: os.fsync(stream.fileno())
def sync_dir(path):
    descriptor = os.open(path, os.O_RDONLY | os.O_DIRECTORY)
    try: os.fsync(descriptor)
    finally: os.close(descriptor)
def checksum(path):
    digest = hashlib.sha256()
    with path.open('rb') as stream:
        while True:
            chunk = stream.read(1048576)
            if not chunk: break
            digest.update(chunk)
    return digest.hexdigest()
def contained(path):
    path = pathlib.Path(path)
    if path.is_symlink() or application not in path.resolve().parents:
        raise ValueError('Backup destination escapes application directory')
    return path
if action == 'create':
    if backup.exists(): raise SystemExit('Backup identity already exists')
    staged = pathlib.Path(tempfile.mkdtemp(prefix='.preparing-', dir=root))
    try:
        metadata = application / '.binary-release.json'
        build = json.loads(metadata.read_text()) if metadata.exists() else {}
        manifest = {'identity': identity, 'target_type': target_type, 'target_id': target_id,
                    'version': build.get('tag', ''), 'commit': build.get('commit', ''),
                    'created_at': int(time.time()), 'source_operation_id': identity,
                    'os': platform.system().lower(), 'architecture': architecture,
                    'schema_version': build.get('schema_version'), 'files': []}
        for number, source in enumerate(sys.argv[6:]):
            source = contained(source)
            if not source.exists(): continue
            attributes = source.stat()
            if not stat.S_ISREG(attributes.st_mode): raise ValueError('Backup requires regular files')
            copy = staged / str(number)
            shutil.copy2(source, copy)
            os.chown(copy, attributes.st_uid, attributes.st_gid)
            sync_file(copy)
            manifest['files'].append({'reference': str(number), 'destination': str(source),
                                      'sha256': checksum(copy), 'size': copy.stat().st_size,
                                      'mode': stat.S_IMODE(attributes.st_mode),
                                      'uid': attributes.st_uid, 'gid': attributes.st_gid})
        if not manifest['files']: raise ValueError('No recoverable files exist')
        path = staged / 'manifest.json'
        path.write_text(json.dumps(manifest))
        sync_file(path)
        sync_dir(staged)
        os.rename(staged, backup)
        sync_dir(root)
    except BaseException:
        shutil.rmtree(staged)
        raise
elif action in ('verify', 'restore'):
    if backup.is_symlink(): raise ValueError('Backup directory cannot be a symlink')
    manifest = json.loads((backup / 'manifest.json').read_text())
    if manifest['identity'] != identity or manifest['target_type'] != target_type or str(manifest['target_id']) != target_id:
        raise ValueError('Backup identity does not match target')
    if manifest.get('os') != platform.system().lower() or manifest.get('architecture') != architecture:
        raise ValueError('Backup platform metadata is incompatible')
    lock_path = backup / '.restore.lock'
    if lock_path.is_symlink(): raise ValueError('Restore lock cannot be a symlink')
    lock = os.open(lock_path, os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    signal.signal(signal.SIGALRM, lambda *_: (_ for _ in ()).throw(TimeoutError('Restore deadline exceeded')))
    signal.alarm(180)
    fcntl.flock(lock, fcntl.LOCK_EX)
    records = []
    destinations = set()
    references = set()
    for record in manifest['files']:
        if not str(record['reference']).isdigit(): raise ValueError('Invalid backup reference')
        source = backup / record['reference']
        destination = contained(record['destination'])
        if str(destination) in destinations or str(record['reference']) in references:
            raise ValueError('Duplicate backup destination or reference')
        destinations.add(str(destination)); references.add(str(record['reference']))
        if source.is_symlink() or source.stat().st_size != record['size'] or checksum(source) != record['sha256']:
            raise ValueError('Backup integrity verification failed')
        if record['mode'] & 0o111:
            with source.open('rb') as stream: header = stream.read(20)
            expected = {'amd64':62,'386':3,'arm64':183,'arm':40,'ppc64le':21,'s390x':22,'riscv64':243}.get(architecture)
            if len(header) < 20 or header[:4] != b'\x7fELF' or header[5] not in (1,2) or int.from_bytes(header[18:20], 'little' if header[5] == 1 else 'big') != expected:
                raise ValueError('Backup executable architecture is incompatible')
        records.append((record, source, destination))
    if action == 'restore':
        transaction_path = backup / 'restore-state.json'
        if transaction_path.is_symlink(): raise ValueError('Restore transaction cannot be a symlink')
        fingerprint = checksum(backup / 'manifest.json')
        transaction = json.loads(transaction_path.read_text()) if transaction_path.exists() else {'identity':identity,'manifest_sha256':fingerprint,'phase':'rollback_prepared','deadline_at_unix_nanos':time.time_ns()+180_000_000_000}
        if transaction.get('identity') != identity or transaction.get('manifest_sha256') != fingerprint:
            raise ValueError('Restore transaction identity changed')
        phases = ['rollback_prepared','backup_verified','restore_ready','restore_committed','rollback_restart_required']
        if transaction.get('phase') not in phases: raise ValueError('Invalid restore phase')
        if phases.index(transaction['phase']) < phases.index('restore_committed'):
            remaining = int(transaction.get('deadline_at_unix_nanos',0))-time.time_ns()
            if remaining <= 0: raise TimeoutError('Persisted restore deadline expired')
            signal.alarm(max(1,(remaining+999_999_999)//1_000_000_000))
        def persist_phase(phase):
            if phases.index(transaction['phase']) > phases.index(phase): return
            transaction['phase'] = phase
            descriptor, name = tempfile.mkstemp(prefix='.restore-state-', dir=backup)
            try:
                with os.fdopen(descriptor,'w') as stream:
                    json.dump(transaction,stream); stream.flush(); os.fsync(stream.fileno())
                os.replace(name,transaction_path); sync_dir(backup)
            finally:
                pathlib.Path(name).unlink(missing_ok=True)
        def matches(record,destination):
            if not destination.is_file() or destination.is_symlink(): return False
            attributes=destination.stat()
            return attributes.st_size==record['size'] and checksum(destination)==record['sha256'] and stat.S_IMODE(attributes.st_mode)==record['mode'] and attributes.st_uid==record['uid'] and attributes.st_gid==record['gid']
        if phases.index(transaction['phase']) >= phases.index('restore_committed'):
            if not all(matches(record,destination) for record,_,destination in records):
                raise ValueError('Committed restore production identity changed; manual recovery required')
        else:
            persist_phase('rollback_prepared')
            persist_phase('backup_verified')
            prepared=[]
            for record,source,destination in records:
                if matches(record,destination): continue
                staged=destination.parent / ('.restore-'+identity+'-'+record['reference'])
                if staged.is_symlink(): raise ValueError('Restore staging cannot be a symlink')
                if not staged.exists() or not matches(record,staged):
                    if staged.exists(): staged.unlink()
                    descriptor=os.open(staged,os.O_CREAT | os.O_EXCL | os.O_WRONLY | os.O_NOFOLLOW,0o600)
                    with os.fdopen(descriptor,'wb') as stream, source.open('rb') as original:
                        shutil.copyfileobj(original,stream); stream.flush(); os.fsync(stream.fileno())
                        os.fchmod(stream.fileno(),record['mode']); os.fchown(stream.fileno(),record['uid'],record['gid']); os.fsync(stream.fileno())
                    sync_dir(destination.parent)
                prepared.append((record,staged,destination))
            persist_phase('restore_ready')
            for record,staged,destination in prepared:
                if not matches(record,staged): raise ValueError('Restore staged identity changed')
                os.replace(staged,destination); sync_dir(destination.parent)
            if not all(matches(record,destination) for record,_,destination in records): raise ValueError('Restored production identity verification failed')
            persist_phase('restore_committed')
        persist_phase('rollback_restart_required')
    signal.alarm(0)
    os.close(lock)
else:
    raise ValueError('Unsupported backup action')
print(json.dumps(manifest), flush=True)
PY
}

download_resolved_build() {
    python3 - "$1" "$2" "$3" "$4" "${5:-}" "${6:-}" <<'PY'
import fcntl, hashlib, json, os, pathlib, re, shutil, signal, struct, sys, tarfile, tempfile, time, urllib.parse, urllib.request
target = json.loads(sys.argv[1])
directory = pathlib.Path(sys.argv[2])
arch, kind = sys.argv[3:5]
operation, app = sys.argv[5:7]
if target.get('os') != 'linux' or target.get('arch') != arch:
    raise SystemExit('Resolved artifact platform mismatch')
size = target.get('size')
digest = target.get('sha256', '').lower()
if not isinstance(size, int) or size <= 0 or size > 2147483648:
    raise SystemExit('Invalid resolved artifact size')
if len(digest) != 64 or any(c not in '0123456789abcdef' for c in digest):
    raise SystemExit('Invalid resolved artifact checksum')
url = target.get('download_url', '')
parsed = urllib.parse.urlparse(url)
if parsed.scheme != 'https' or not parsed.hostname or parsed.username or parsed.fragment:
    raise SystemExit('Resolved artifact requires HTTPS')
class HTTPSRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        if urllib.parse.urlparse(newurl).scheme != 'https':
            raise ValueError('Artifact redirect must remain HTTPS')
        return super().redirect_request(req, fp, code, msg, headers, newurl)
def expire(*_): raise TimeoutError('Absolute artifact phase deadline expired')
signal.signal(signal.SIGALRM, expire)
signal.alarm(180)
def sync_directory(path):
    fd=os.open(path,os.O_RDONLY|os.O_DIRECTORY)
    try: os.fsync(fd)
    finally: os.close(fd)
def atomic_json(path,value):
    with tempfile.NamedTemporaryFile(mode='w',dir=path.parent,prefix='.artifact-state-',delete=False) as output:
        name=output.name
        json.dump(value,output);output.flush();os.fsync(output.fileno())
    try: os.replace(name,path);sync_directory(path.parent)
    finally:
        if os.path.exists(name): os.unlink(name)
cache=directory
if operation:
    if not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_.-]{0,95}',operation) or not app:
        raise ValueError('Operation-owned artifact identity is required')
    root=pathlib.Path(app).resolve()/'.maintenance-artifacts'
    if root.is_symlink(): raise ValueError('Artifact root cannot be a symlink')
    root.mkdir(mode=0o700,exist_ok=True)
    cache=root/operation
    if cache.is_symlink(): raise ValueError('Artifact operation directory cannot be a symlink')
    cache.mkdir(mode=0o700,exist_ok=True)
lock=os.open(cache/'.artifact.lock',os.O_RDWR|os.O_CREAT|os.O_NOFOLLOW,0o600)
fcntl.flock(lock,fcntl.LOCK_EX)
archive=cache/'resolved-artifact'
partial=cache/'resolved-artifact.partial'
record=cache/'target.json'
for path in (archive,partial,record):
    if path.is_symlink(): raise ValueError('Artifact state cannot be a symlink')
identity={'operation_id':operation,'target':target,'kind':kind}
if record.exists():
    state=json.loads(record.read_text())
    if any(state.get(key)!=value for key,value in identity.items()):
        raise ValueError('Persisted immutable artifact target differs')
else:
    state=dict(identity,download_deadline_at=time.time()+180)
    atomic_json(record,state)
def valid_artifact(path):
    if not path.is_file() or path.stat().st_size!=size: return False
    hasher=hashlib.sha256()
    with path.open('rb') as source:
        while True:
            chunk=source.read(1048576)
            if not chunk: break
            hasher.update(chunk)
    return hasher.hexdigest()==digest
if archive.exists():
    # A completed marker never overrides actual bytes. Reject corruption rather
    # than silently downloading and installing a replacement.
    if not valid_artifact(archive): raise ValueError('Cached artifact size or checksum mismatch')
else:
    if partial.exists():
        if valid_artifact(partial):
            os.replace(partial,archive);sync_directory(cache)
        else:
            # Only this verified operation directory and exact owned file.
            partial.unlink();sync_directory(cache)
    if not archive.exists():
        remaining=state['download_deadline_at']-time.time()
        if remaining<=0: raise TimeoutError('Persisted artifact download deadline expired')
        signal.setitimer(signal.ITIMER_REAL,remaining)
        opener=urllib.request.build_opener(HTTPSRedirect())
        count=0
        with opener.open(url,timeout=min(120,remaining)) as response, partial.open('xb') as output:
            if response.status!=200: raise ValueError('Artifact download was not successful')
            while True:
                chunk=response.read(min(1048576,size+1-count))
                if not chunk: break
                count+=len(chunk)
                if count>size: raise ValueError('Artifact exceeds resolved size')
                output.write(chunk)
            output.flush();os.fsync(output.fileno())
        if not valid_artifact(partial): raise ValueError('Resolved artifact size or checksum mismatch')
        os.replace(partial,archive);sync_directory(cache)
state['artifact_ready']=True
atomic_json(record,state)
machines = {'amd64': 62, '386': 3, 'arm64': 183, 'armv5': 40, 'armv6': 40,
            'armv7': 40, 'arm': 40, 'ppc64le': 21, 's390x': 22, 'riscv64': 243}
def check_elf(path):
    with path.open('rb') as stream: header = stream.read(20)
    if len(header) != 20 or header[:4] != b'\x7fELF' or header[5] not in (1, 2):
        raise ValueError('Artifact contains no ELF executable')
    endian = '<' if header[5] == 1 else '>'
    if struct.unpack(endian + 'H', header[18:20])[0] != machines.get(arch):
        raise ValueError('ELF architecture mismatch')
if kind == 'node':
    check_elf(archive)
    with archive.open('rb') as source, (directory/'antimage-node').open('xb') as output:
        shutil.copyfileobj(source,output);output.flush();os.fsync(output.fileno())
elif kind == 'panel':
    required = {'antimage-server', 'antimage-cli'}
    seen = set()
    with tarfile.open(archive, 'r:gz') as package:
        for member in package:
            name = member.name.removeprefix('./')
            if name not in required: continue
            if name in seen or not member.isfile() or member.size <= 0 or member.size > 1073741824:
                raise ValueError('Invalid or duplicate binary archive member')
            seen.add(name)
            path = directory / name
            with package.extractfile(member) as source, path.open('xb') as output:
                while True:
                    chunk = source.read(1048576)
                    if not chunk: break
                    output.write(chunk)
                output.flush()
                os.fsync(output.fileno())
            check_elf(path)
    if seen != required: raise ValueError('Resolved panel archive is incomplete')
else:
    raise ValueError('Unsupported target type')
print('Artifact verified: ' + target['version'], flush=True)
PY
}

install_binary_antimage_node() {
    local node_version="$1"
    local configure="${2:-1}"
    local binary_arch
    local resolved_version
    local node_asset_url
    local artifact_url
    local tmp_dir
    local rollback_binary=""
    local rollback_metadata=""
    local binary_installed=0

    detect_os
    for package in curl jq unzip; do
        if ! command -v "$package" >/dev/null 2>&1; then
            install_package "$package"
        fi
    done
    ensure_vpn_binary_prerequisites
    ensure_haproxy_prerequisites

    binary_arch=$(detect_node_binary_arch)
    tmp_dir=$(mktemp -d)

    if [ -n "${RESOLVED_BUILD_JSON:-}" ]; then
        resolved_version=$(printf '%s' "${RESOLVED_BUILD_JSON:-}" | jq -er '.version')
        [ "$resolved_version" = "$node_version" ] || { echo "Resolved version differs from requested installer target" >&2; return 1; }
        artifact_url=$(printf '%s' "${RESOLVED_BUILD_JSON:-}" | jq -er '.download_url')
        download_resolved_build "${RESOLVED_BUILD_JSON:-}" "$tmp_dir" "$binary_arch" node "${UPDATE_OPERATION_ID:-}" "$APP_DIR" || { rm -rf "$tmp_dir"; return 1; }
    elif [ -n "${ANTIMAGE_NODE_BINARY_OVERRIDE:-}" ]; then
        if [ ! -f "$ANTIMAGE_NODE_BINARY_OVERRIDE" ]; then
            colorized_echo red "ANTIMAGE_NODE_BINARY_OVERRIDE must point to an existing file." >&2
            rm -rf "$tmp_dir"
            exit 1
        fi
        ui_spinner_run "Installing AntiMage-node custom binary" install -m 755 "$ANTIMAGE_NODE_BINARY_OVERRIDE" "$tmp_dir/antimage-node"
        resolved_version="${ANTIMAGE_NODE_BINARY_OVERRIDE_VERSION:-custom}"
        artifact_url="local-override"
    elif [ "$node_version" = "dev" ]; then
        IFS='|' read -r resolved_version artifact_url < <(get_node_binary_dev_artifact_metadata "$binary_arch")
        ui_spinner_run "Downloading AntiMage-node dev release binary" curl -fL --retry 3 --retry-all-errors --retry-delay 2 --connect-timeout 15 "$artifact_url" -o "$tmp_dir/antimage-node"
        chmod +x "$tmp_dir/antimage-node"
    elif [[ "$node_version" =~ ^dev-[a-f0-9]{7,40}$ ]]; then
        local pinned_dev_metadata
        pinned_dev_metadata=$(get_node_binary_pinned_dev_artifact_metadata "$node_version" "$binary_arch") || { rm -rf "$tmp_dir"; return 1; }
        IFS='|' read -r resolved_version artifact_url <<< "$pinned_dev_metadata"
        ui_spinner_run "Downloading AntiMage-node ${node_version}" curl -fL --retry 3 --retry-all-errors --retry-delay 2 --connect-timeout 15 "$artifact_url" -o "$tmp_dir/antimage-node"
        chmod +x "$tmp_dir/antimage-node"
    else
        IFS='|' read -r resolved_version node_asset_url < <(get_node_binary_release_asset_metadata "$node_version" "$binary_arch")
        ui_spinner_run "Downloading AntiMage-node binary" curl -fL "$node_asset_url" -o "$tmp_dir/antimage-node"
    fi

    if [ ! -f "$tmp_dir/antimage-node" ]; then
        colorized_echo red "Downloaded binary package is incomplete; antimage-node is missing." >&2
        rm -rf "$tmp_dir"
        exit 1
    fi
    if [ -z "${RESOLVED_BUILD_JSON:-}" ] && [ "${artifact_url:-}" != "local-override" ]; then
        verify_node_binary_checksum "${artifact_url:-$node_asset_url}" "$tmp_dir/antimage-node" || { rm -rf "$tmp_dir"; exit 1; }
    fi

    mkdir -p "$BINARY_BIN_DIR" "$DATA_DIR" "$APP_DIR"
    if [ "$configure" != "1" ]; then
        if [ -x "$BINARY_NODE" ]; then
            rollback_binary="$tmp_dir/antimage-node.rollback"
            cp -p "$BINARY_NODE" "$rollback_binary"
        fi
        if [ -f "$BINARY_METADATA_FILE" ]; then
            rollback_metadata="$tmp_dir/binary-release.rollback.json"
            cp -p "$BINARY_METADATA_FILE" "$rollback_metadata"
        fi
        rollback_binary_update() {
            if [ "$binary_installed" -eq 1 ]; then
                if [ -n "$rollback_binary" ] && [ -f "$rollback_binary" ]; then
                    node_guarded_boundary install -m 755 "$rollback_binary" "$BINARY_NODE" || echo "Interrupted binary restoration requires recovery" >&2
                else
                    node_guarded_boundary rm -f "$BINARY_NODE" || echo "Superseded binary removal rejected" >&2
                fi
                if [ -n "$rollback_metadata" ] && [ -f "$rollback_metadata" ]; then
                    node_guarded_boundary install -m 644 "$rollback_metadata" "$BINARY_METADATA_FILE" || echo "Interrupted metadata restoration requires recovery" >&2
                else
                    node_guarded_boundary rm -f "$BINARY_METADATA_FILE" || echo "Superseded metadata removal rejected" >&2
                fi
            fi
        }
        trap 'rollback_binary_update' RETURN
    fi
    local staged_binary="${BINARY_NODE}.new.$$"
    install -m 755 "$tmp_dir/antimage-node" "$staged_binary"
    if [ -n "${UPDATE_OPERATION_ID:-}" ] && [ -f "$APP_DIR/.update-backups/$UPDATE_OPERATION_ID/manifest.json" ]; then
        node_guarded_boundary managed_binary_install "$APP_DIR" "$UPDATE_OPERATION_ID" node "$APP_NAME" "$staged_binary" "$BINARY_NODE" || return 1
        rm -f "$staged_binary"
    else
        node_guarded_boundary mv -f "$staged_binary" "$BINARY_NODE" || return 1
    fi
    binary_installed=1

    if [ "$configure" = "1" ]; then
        configure_binary_node_env
        install_latest_xray_for_binary_node
    elif [ ! -x "$DATA_DIR/xray-core/xray" ]; then
        install_latest_xray_for_binary_node
    fi

    local installed_channel="stable"
    if [[ "${resolved_version:-$node_version}" == dev-* ]]; then
        installed_channel="dev"
    fi
    commit_node_install_metadata() {
        write_node_binary_release_metadata "${resolved_version:-$node_version}" "$binary_arch" "${artifact_url:-${node_asset_url:-}}" || return 1
        set_env_value "ANTIMAGE_NODE_VERSION" "${resolved_version:-$node_version}" || return 1
        set_env_value "ANTIMAGE_NODE_UPDATE_CHANNEL" "$installed_channel" || return 1
        echo "binary" > "$INSTALL_MODE_FILE" || return 1
        create_binary_antimage_node_service
    }
    node_guarded_boundary commit_node_install_metadata || return 1
    binary_installed=0
    trap - RETURN
    rm -rf "$tmp_dir"
    colorized_echo green "AntiMage-node binary files installed successfully"
}

install_antimage_node_script() {
    TARGET_PATH="/usr/local/bin/$APP_NAME"
    TEMP_SCRIPT=$(mktemp)
    if ! ui_spinner_run "Downloading $APP_NAME command script" curl -fsSL "$SCRIPT_URL" -o "$TEMP_SCRIPT"; then
        colorized_echo red "Failed to download script from $SCRIPT_URL"
        rm -f "$TEMP_SCRIPT"
        exit 1
    fi
    if head -n 1 "$TEMP_SCRIPT" | grep -qi "<!DOCTYPE"; then
        colorized_echo red "Unexpected HTML response while downloading script"
        rm -f "$TEMP_SCRIPT"
        exit 1
    fi
    ui_spinner_run "Installing $APP_NAME command script" install -m 755 "$TEMP_SCRIPT" "$TARGET_PATH"
    rm -f "$TEMP_SCRIPT"
    colorized_echo green "$APP_NAME script installed at $TARGET_PATH"
}

# Get a list of occupied ports
get_occupied_ports() {
    if command -v ss &>/dev/null; then
        OCCUPIED_PORTS=$(ss -tuln | awk '{print $5}' | grep -Eo '[0-9]+$' | sort | uniq)
    elif command -v netstat &>/dev/null; then
        OCCUPIED_PORTS=$(netstat -tuln | awk '{print $4}' | grep -Eo '[0-9]+$' | sort | uniq)
    else
        colorized_echo yellow "Neither ss nor netstat found. Attempting to install net-tools."
        detect_os
        install_package net-tools
        if command -v netstat &>/dev/null; then
            OCCUPIED_PORTS=$(netstat -tuln | awk '{print $4}' | grep -Eo '[0-9]+$' | sort | uniq)
        else
            colorized_echo red "Failed to install net-tools. Please install it manually."
            exit 1
        fi
    fi
}

# Function to check if a port is occupied
is_port_occupied() {
    if echo "$OCCUPIED_PORTS" | grep -q -w "$1"; then
        return 0
    else
        return 1
    fi
}

prompt_node_port_setting() {
    local key="$1"
    local label="$2"
    local fallback="$3"
    local other_port="${4:-}"
    local current_port
    local value

    current_port=$(get_env_value "$key")
    fallback="${current_port:-$fallback}"

    while true; do
        printf "Enter the %s (default %s): " "$label" "$fallback" >&2
        IFS= read -r value
        value="${value:-$fallback}"
        if ! [[ "$value" =~ ^[0-9]+$ ]] || [ "$value" -lt 1 ] || [ "$value" -gt 65535 ]; then
            colorized_echo red "Invalid port. Please enter a port between 1 and 65535." >&2
        elif [ -n "$other_port" ] && [ "$value" -eq "$other_port" ]; then
            colorized_echo red "Port $value cannot be the same as SERVICE_PORT. Please enter another port." >&2
        elif is_port_occupied "$value" && [ "$value" != "$current_port" ]; then
            colorized_echo red "Port $value is already in use. Please enter another port." >&2
        else
            echo "$value"
            return 0
        fi
    done
}

install_antimage_node() {
    # Fetch releases
    mkdir -p "$DATA_DIR"
    mkdir -p "$APP_DIR"
    mkdir -p "$DATA_MAIN_DIR"
    echo "$BRANCH" > "$BRANCH_FILE"

    ensure_vpn_host_prerequisites

    rm -f "$CERT_FILE" "$CERT_KEY_FILE"
    read_node_certificate_bundle

    get_occupied_ports

    # Prompt the user to enter ports with occupation check
    while true; do
        read -p "Enter the SERVICE_PORT (default 62050): " -r SERVICE_PORT
        if [[ -z "$SERVICE_PORT" ]]; then
            SERVICE_PORT=62050
        fi
        if [[ "$SERVICE_PORT" -ge 1 && "$SERVICE_PORT" -le 65535 ]]; then
            if is_port_occupied "$SERVICE_PORT"; then
                colorized_echo red "Port $SERVICE_PORT is already in use. Please enter another port."
            else
                break
            fi
        else
            colorized_echo red "Invalid port. Please enter a port between 1 and 65535."
        fi
    done
    
    while true; do
        read -p "Enter the XRAY_API_PORT (default 62051): " -r XRAY_API_PORT
        if [[ -z "$XRAY_API_PORT" ]]; then
            XRAY_API_PORT=62051
        fi
        if [[ "$XRAY_API_PORT" -ge 1 && "$XRAY_API_PORT" -le 65535 ]]; then
            if is_port_occupied "$XRAY_API_PORT"; then
                colorized_echo red "Port $XRAY_API_PORT is already in use. Please enter another port."
            elif [[ "$XRAY_API_PORT" -eq "$SERVICE_PORT" ]]; then
                colorized_echo red "Port $XRAY_API_PORT cannot be the same as SERVICE_PORT. Please enter another port."
            else
                break
            fi
        else
            colorized_echo red "Invalid port. Please enter a port between 1 and 65535."
        fi
    done
    
    colorized_echo blue "Generating compose file"
    
    # Write content to the file
    cat > "$COMPOSE_FILE" <<EOL
services:
  antimage-node:
    container_name: $APP_NAME
    image: $DOCKER_IMAGE
    restart: always
    network_mode: host
    cap_add:
      - NET_ADMIN
    devices:
      - /dev/net/tun:/dev/net/tun
    environment:
      ANTIMAGE_DATA_DIR: "/var/lib/antimage-node"
      SSL_CLIENT_CERT_FILE: "/var/lib/antimage-node/cert.pem"
      SSL_CERT_FILE: "/var/lib/antimage-node/cert.pem"
      SSL_KEY_FILE: "/var/lib/antimage-node/cert.key"
      SERVICE_HOST: "0.0.0.0"
      SERVICE_PORT: "$SERVICE_PORT"
      XRAY_API_HOST: "127.0.0.1"
      XRAY_API_PORT: "$XRAY_API_PORT"

    volumes:
      - $DATA_DIR:/var/lib/antimage-node
EOL
    colorized_echo green "File saved in $APP_DIR/docker-compose.yml"
}


uninstall_antimage_node_script() {
    if [ -f "/usr/local/bin/$APP_NAME" ]; then
        colorized_echo yellow "Removing antimage-node script"
        rm "/usr/local/bin/$APP_NAME"
    fi
}

uninstall_antimage_node() {
    if [ -f "$BINARY_SERVICE_UNIT" ]; then
        systemctl disable --now "$APP_NAME.service" >/dev/null 2>&1 || true
        rm -f "$BINARY_SERVICE_UNIT"
        systemctl daemon-reload
    fi
    if [ -d "$APP_DIR" ]; then
        colorized_echo yellow "Removing directory: $APP_DIR"
        rm -r "$APP_DIR"
    fi
}

uninstall_antimage_node_docker_images() {
    images=$(docker images | grep antimage-node | awk '{print $3}')
    
    if [ -n "$images" ]; then
        colorized_echo yellow "Removing Docker images of AntiMage-node"
        for image in $images; do
            if docker rmi "$image" >/dev/null 2>&1; then
                colorized_echo yellow "Image $image removed"
            fi
        done
    fi
}

uninstall_antimage_node_data_files() {
    if [ -d "$DATA_DIR" ]; then
        colorized_echo yellow "Removing directory: $DATA_DIR"
        rm -r "$DATA_DIR"
    fi
}

up_antimage_node() {
    if is_binary_install; then
        systemctl enable --now "$APP_NAME.service"
        return
    fi
    $COMPOSE -f $COMPOSE_FILE -p "$APP_NAME" up -d --remove-orphans
}

down_antimage_node() {
    if is_binary_install; then
        systemctl stop "$APP_NAME.service"
        return
    fi
    $COMPOSE -f $COMPOSE_FILE -p "$APP_NAME" down
}

show_antimage_node_logs() {
    if is_binary_install; then
        journalctl -u "$APP_NAME.service" --no-pager
        return
    fi
    $COMPOSE -f $COMPOSE_FILE -p "$APP_NAME" logs
}

follow_antimage_node_logs() {
    if is_binary_install; then
        journalctl -u "$APP_NAME.service" -f
        return
    fi
    $COMPOSE -f $COMPOSE_FILE -p "$APP_NAME" logs -f
}

update_antimage_node_script() {
    colorized_echo blue "Updating $APP_NAME script from $SCRIPT_URL"
    install_antimage_node_script
}

reexec_updated_node_script() {
    local target_path="/usr/local/bin/$APP_NAME"
    local args=("update")

    if [ "${ANTIMAGE_NODE_SKIP_REEXEC:-0}" = "1" ]; then
        return
    fi
    if [ ! -x "$target_path" ]; then
        return
    fi

    if [ "$NODE_VERSION_SET" -eq 1 ]; then
        case "${NODE_VERSION_REQUESTED:-}" in
            dev)
                args+=("--dev")
            ;;
            "")
                args+=("--version" "latest")
            ;;
            *)
                args+=("--version" "$NODE_VERSION_REQUESTED")
            ;;
        esac
    fi

    colorized_echo blue "Reloading updated $APP_NAME script"
    ANTIMAGE_NODE_SKIP_REEXEC=1 exec "$target_path" "${args[@]}"
}

update_antimage_node() {
    local requested_version="${1:-}"
    if is_binary_install; then
        local node_version="${requested_version:-latest}"
        if [ -z "$requested_version" ] && [ "$BRANCH" = "dev" ]; then
            node_version="dev"
        fi
        install_binary_antimage_node "$node_version" "0"
        return
    fi

    if [ -n "$requested_version" ]; then
        case "$requested_version" in
            dev|dev-*)
                set_branch_variables dev
            ;;
            latest|"")
                set_branch_variables master
            ;;
            *)
                set_branch_variables master
                DOCKER_IMAGE="ghcr.io/devprogrmer/antimage-node:${requested_version}"
            ;;
        esac
        echo "$BRANCH" > "$BRANCH_FILE"
        if [ -f "$COMPOSE_FILE" ]; then
            sed -i -E "s|^[[:space:]]*image:.*(ghcr.io/devprogrmer|antimagepanel)/antimage-node.*|    image: $DOCKER_IMAGE|" "$COMPOSE_FILE"
        fi
    fi
    $COMPOSE -f $COMPOSE_FILE -p "$APP_NAME" pull
}

is_antimage_node_installed() {
    if [ -d "$APP_DIR" ]; then
        return 0
    else
        return 1
    fi
}

is_antimage_node_up() {
    if is_binary_install; then
        systemctl is-active --quiet "$APP_NAME.service"
        return
    fi
    if [ -z "${COMPOSE:-}" ]; then
        return 1
    fi
    if [ -z "$($COMPOSE -f $COMPOSE_FILE ps -q -a)" ]; then
        return 1
    else
        return 0
    fi
}

optimize_antimage_server() {
    local sysctl_file="/etc/sysctl.d/99-antimage-network.conf"
    local available_cc=""
    mkdir -p /etc/sysctl.d
    modprobe tcp_bbr >/dev/null 2>&1 || true
    available_cc=$(sysctl -n net.ipv4.tcp_available_congestion_control 2>/dev/null || true)
    {
        echo "net.ipv4.ip_forward=1"
        echo "net.core.default_qdisc=fq"
        echo "net.core.somaxconn=4096"
        echo "net.core.netdev_max_backlog=16384"
        echo "net.core.rmem_max=16777216"
        echo "net.core.wmem_max=16777216"
        echo "net.ipv4.udp_rmem_min=8192"
        echo "net.ipv4.udp_wmem_min=8192"
        echo "net.ipv4.tcp_mtu_probing=1"
        [[ " $available_cc " == *" bbr "* ]] && echo "net.ipv4.tcp_congestion_control=bbr"
    } > "$sysctl_file"
    sysctl -p "$sysctl_file" >/dev/null 2>&1 || colorized_echo yellow "Some network tuning values are unavailable on this kernel."
    if command -v iptables >/dev/null 2>&1; then
        iptables -t mangle -N ANTIMAGE_MSS >/dev/null 2>&1 || true
        iptables -t mangle -C ANTIMAGE_MSS -p tcp --tcp-flags SYN,RST SYN -j TCPMSS --clamp-mss-to-pmtu >/dev/null 2>&1 || iptables -t mangle -A ANTIMAGE_MSS -p tcp --tcp-flags SYN,RST SYN -j TCPMSS --clamp-mss-to-pmtu
        iptables -t mangle -C FORWARD -j ANTIMAGE_MSS >/dev/null 2>&1 || iptables -t mangle -A FORWARD -j ANTIMAGE_MSS
    fi
    if command -v swapon >/dev/null 2>&1 && [ -r /proc/meminfo ] && ! swapon --show=NAME --noheadings 2>/dev/null | grep -q .; then
        local memory_kb disk_kb swap_mb
        memory_kb=$(awk '/^MemTotal:/ {print $2}' /proc/meminfo); disk_kb=$(df -Pk / | awk 'NR==2 {print $4}')
        if [ "${memory_kb:-0}" -lt 3145728 ] && [ "${disk_kb:-0}" -gt 1572864 ]; then
            swap_mb=1024; [ "$memory_kb" -lt 1572864 ] && swap_mb=2048
            if [ ! -f /swapfile ]; then fallocate -l "${swap_mb}M" /swapfile 2>/dev/null || dd if=/dev/zero of=/swapfile bs=1M count="$swap_mb" status=none; chmod 600 /swapfile; mkswap /swapfile >/dev/null; fi
            swapon /swapfile >/dev/null 2>&1 || true
            grep -qE '^/swapfile[[:space:]]' /etc/fstab || echo '/swapfile none swap sw 0 0' >> /etc/fstab
        fi
    fi
}

install_command() {
    check_running_as_root
    local install_mode
    local node_version

    # Check if antimage is already installed
    if is_antimage_node_installed; then
        colorized_echo red "AntiMage-node is already installed at $APP_DIR"
        read -p "Do you want to override the previous installation? (y/n) "
        if [[ ! $REPLY =~ ^[Yy]$ ]]; then
            colorized_echo red "Aborted installation"
            exit 1
        fi
    fi
    install_mode=$(select_install_mode "$INSTALL_MODE_REQUESTED")
    if [ "$NODE_VERSION_SET" -eq 1 ]; then
        node_version="$NODE_VERSION_REQUESTED"
    else
        select_node_version "" "$install_mode"
        node_version="$SELECTED_NODE_VERSION"
    fi
    case "$node_version" in
        dev)
            set_branch_variables dev
        ;;
        latest|"")
            set_branch_variables master
            node_version="latest"
        ;;
        *)
            set_branch_variables master
        ;;
    esac
    colorized_echo blue "Selected install mode: $install_mode"
    colorized_echo blue "Selected release channel: $node_version"

    detect_os
    optimize_antimage_server
    if ! command -v jq >/dev/null 2>&1; then
        install_package jq
    fi
    if ! command -v curl >/dev/null 2>&1; then
        install_package curl
    fi
    if [ "$install_mode" = "docker" ]; then
        if ! command -v docker >/dev/null 2>&1; then
            install_docker
        fi
        detect_compose
    fi
    install_antimage_node_script
    if [ "$install_mode" = "binary" ]; then
        install_binary_antimage_node "$node_version" "1"
    else
        install_antimage_node
        echo "docker" > "$INSTALL_MODE_FILE"
    fi
    up_antimage_node
    SERVICE_PORT="${SERVICE_PORT:-$(get_env_value "SERVICE_PORT")}"
    XRAY_API_PORT="${XRAY_API_PORT:-$(get_env_value "XRAY_API_PORT")}"
    echo "Use your IP: $NODE_IP and control port: $SERVICE_PORT to setup your AntiMage Main Panel"
    colorized_echo yellow "Run '$APP_NAME logs' if you want to follow live node logs."
}

uninstall_command() {
    check_running_as_root
    local install_mode
    install_mode=$(get_install_mode)
    local node_exists=0
    if is_antimage_node_installed; then
        node_exists=1
    fi

    local service_exists=0
    if [ -f "$BINARY_SERVICE_UNIT" ]; then
        service_exists=1
    fi

    if [ "$node_exists" -eq 0 ] && [ "$service_exists" -eq 0 ]; then
        colorized_echo red "AntiMage-node not installed!"
        exit 1
    fi

    read -p "Do you really want to uninstall AntiMage-node? (y/n) "
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        colorized_echo red "Aborted"
        exit 1
    fi

    if [ "$node_exists" -eq 1 ]; then
        if [ "$install_mode" != "binary" ]; then
            detect_compose
        fi
        if is_antimage_node_up; then
            down_antimage_node
        fi
    fi

    uninstall_antimage_node_script

    if [ "$node_exists" -eq 1 ]; then
        uninstall_antimage_node
        if [ "$install_mode" != "binary" ]; then
            uninstall_antimage_node_docker_images
        fi

        read -p "Do you want to remove AntiMage-node data files too ($DATA_DIR)? (y/n) "
        if [[ ! $REPLY =~ ^[Yy]$ ]]; then
            colorized_echo green "AntiMage-node uninstalled successfully"
        else
            uninstall_antimage_node_data_files
            colorized_echo green "AntiMage-node uninstalled successfully"
        fi
    else
        colorized_echo green "AntiMage-node service/scripts removed"
    fi
}

up_command() {
    help() {
        colorized_echo red "Usage: antimage-node up [options]"
        echo ""
        echo "OPTIONS:"
        echo "  -h, --help        display this help message"
        echo "  -n, --no-logs     do not follow logs after starting"
    }
    
    local no_logs=false
    while [[ "$#" -gt 0 ]]; do
        case "$1" in
            -n|--no-logs)
                no_logs=true
            ;;
            -h|--help)
                help
                exit 0
            ;;
            *)
                echo "Error: Invalid option: $1" >&2
                help
                exit 0
            ;;
        esac
        shift
    done
    
    # Check if antimage-node is installed
    if ! is_antimage_node_installed; then
        colorized_echo red "AntiMage-node's not installed!"
        exit 1
    fi
    
    if ! is_binary_install; then
        detect_compose
    fi
    
    if is_antimage_node_up; then
        colorized_echo red "AntiMage-node's already up"
        exit 1
    fi
    
    up_antimage_node
    if [ "$no_logs" = false ]; then
        follow_antimage_node_logs
    fi
}

down_command() {
    # Check if antimage-node is installed
    if ! is_antimage_node_installed; then
        colorized_echo red "AntiMage-node not installed!"
        exit 1
    fi
    
    if ! is_binary_install; then
        detect_compose
    fi
    
    if ! is_antimage_node_up; then
        colorized_echo red "AntiMage-node already down"
        exit 1
    fi
    
    down_antimage_node
}

# Embedded in the installed CLI. Accepted generations and queued job checks use
# the same persistent file and execution lock, including after agent restart.
node_command_fence() {
    python3 - "$1" "$APP_DIR" "$FENCE_OPERATION_ID" "$LEASE_GENERATION" "$COMMAND_ID" "$RESOURCE_GENERATION" "$RESOURCE_ID" <<'PY'
import fcntl,json,os,pathlib,re,signal,sys,tempfile
mode,app,operation,generation,command,resource_generation,resource_id=sys.argv[1:]
pattern=r'[A-Za-z0-9][A-Za-z0-9_.-]{0,95}'
if not re.fullmatch(pattern,operation) or not re.fullmatch(pattern,command) or not re.fullmatch(pattern,resource_id):
    raise SystemExit('Invalid command fencing identity')
if not generation.isdigit() or not 0<int(generation)<2**63:
    raise SystemExit('Positive command fencing generation is required')
generation=int(generation)
if not resource_generation.isdigit() or not 0<int(resource_generation)<2**63:
    raise SystemExit('Positive resource generation is required')
resource_generation=int(resource_generation)
root=pathlib.Path(app).resolve()/'.maintenance-fences'
if root.is_symlink(): raise SystemExit('Fence root cannot be a symlink')
root.mkdir(mode=0o700,exist_ok=True)
lock=root/'.generation.lock'
fd=os.open(lock,os.O_RDWR|os.O_CREAT|os.O_NOFOLLOW,0o600)
def expired(*_): raise TimeoutError('Node execution lock deadline expired')
signal.signal(signal.SIGALRM,expired)
signal.alarm(15)
if mode.endswith('-held'):
    execution=(root/'.execution.lock').stat()
    if os.fstat(9).st_ino!=execution.st_ino or os.fstat(9).st_dev!=execution.st_dev:
        raise SystemExit('Inherited execution lock is unavailable')
    fcntl.flock(9,fcntl.LOCK_EX|fcntl.LOCK_NB)
if mode.endswith('-boundary-held'):
    if os.fstat(8).st_ino!=os.fstat(fd).st_ino or os.fstat(8).st_dev!=os.fstat(fd).st_dev:
        raise SystemExit('Destructive boundary lock is unavailable')
    fcntl.flock(8,fcntl.LOCK_EX|fcntl.LOCK_NB)
else:
    fcntl.flock(fd,fcntl.LOCK_EX)
def persist(path,state):
    temporary=None
    try:
        with tempfile.NamedTemporaryFile(mode='w',dir=root,prefix='.fence-',delete=False) as output:
            temporary=output.name
            json.dump(state,output)
            output.flush();os.fsync(output.fileno())
        os.replace(temporary,path)
        parent=os.open(root,os.O_RDONLY|os.O_DIRECTORY)
        try: os.fsync(parent)
        finally: os.close(parent)
    finally:
        if temporary and os.path.exists(temporary): os.unlink(temporary)
resource_record=root/'resource.json'
if resource_record.is_symlink(): raise SystemExit('Resource record cannot be a symlink')
resource=json.loads(resource_record.read_text()) if resource_record.exists() else {'generation':0,'owner_operation_id':'','resource_id':resource_id}
if resource['resource_id']!=resource_id: raise SystemExit('Resource identity mismatch')
if resource_generation<resource['generation']: raise SystemExit('Stale resource generation rejected')
if mode.startswith('accept'):
    if resource_generation==resource['generation'] and resource['owner_operation_id']!=operation:
        raise SystemExit('Resource generation belongs to another operation')
    if resource_generation>resource['generation']:
        resource={'generation':resource_generation,'owner_operation_id':operation,'resource_id':resource_id}
        persist(resource_record,resource)
elif resource_generation!=resource['generation'] or resource['owner_operation_id']!=operation:
    raise SystemExit('Job has been superseded by another resource owner')
record=root/('operation-'+operation+'.json')
if record.is_symlink(): raise SystemExit('Fence record cannot be a symlink')
state=json.loads(record.read_text()) if record.exists() else {'generation':0,'commands':{}}
if generation<state['generation']: raise SystemExit('Stale command generation rejected')
if mode.startswith('accept'):
    if generation>state['generation']:
        state={'generation':generation,'commands':{key:value for key,value in state['commands'].items() if value in ('completed','started')},'command_resource_generations':state.get('command_resource_generations',{})}
    if state['commands'].get(command)=='started':
        raise SystemExit('Command outcome unknown; reconcile target state before replay')
    completed=state['commands'].get(command)=='completed'
    if not completed: state['commands'][command]='accepted'
elif mode.startswith('complete'):
    if generation!=state['generation'] or state['commands'].get(command) not in ('accepted','started'):
        raise SystemExit('Completion belongs to an obsolete command')
    completed=False
    state['commands'][command]='completed'
elif mode.startswith('start'):
    if generation!=state['generation'] or state['commands'].get(command)!='accepted':
        raise SystemExit('Command already dispatched; reconcile before replay')
    completed=False
    state['commands'][command]='started'
    state.setdefault('command_resource_generations',{})[command]=resource_generation
elif mode.startswith('check'):
    if generation!=state['generation'] or state['commands'].get(command) not in ('accepted','started'):
        raise SystemExit('Queued command no longer owns accepted generation')
else: raise SystemExit('Unknown fencing mode')
if mode.startswith(('accept','complete','start')):
    persist(record,state)
    if completed: raise SystemExit('Command already executed; reconcile target state')
print('Command generation verified',flush=True)
PY
}

finish_node_command() {
    [ -n "${LEASE_GENERATION:-}" ] || return 0
    node_command_fence complete-held
}

lock_node_command() {
    # Installed destructive callers must first establish persistent ownership.
    # Bootstrap callers without a managed operation never enter this helper.
    [ -n "${LEASE_GENERATION:-}" ] || return 0
    [ ! -L "$APP_DIR/.maintenance-fences" ] || return 1
    mkdir -p -m 700 "$APP_DIR/.maintenance-fences"
    [ ! -L "$APP_DIR/.maintenance-fences/.execution.lock" ] || return 1
    exec 9>"$APP_DIR/.maintenance-fences/.execution.lock"
    flock -x -w 15 9 || return 1
    # Persist dispatch before the first destructive boundary. A crash keeps the
    # started receipt, so another process cannot execute this command again.
    node_command_fence start-held
}

require_node_command_ownership() {
    if [ -z "${LEASE_GENERATION:-}" ] || [ -z "${RESOURCE_GENERATION:-}" ] || [ -z "${COMMAND_ID:-}" ]; then
        echo "Destructive CLI command requires an operation authorized by the Panel; root does not bypass maintenance ownership" >&2
        return 1
    fi
    node_command_fence check
}

node_guarded_boundary() {
    [ -n "${LEASE_GENERATION:-}" ] || { "$@"; return $?; }
    [ ! -L "$APP_DIR/.maintenance-fences/.generation.lock" ] || return 1
    exec 8>"$APP_DIR/.maintenance-fences/.generation.lock"
    flock -x -w 15 8 || return 1
    node_command_fence check-boundary-held || { flock -u 8; return 1; }
    local result=0
    if "$@"; then result=0; else result=$?; fi
    flock -u 8
    return "$result"
}

rollback_command() {
    check_running_as_root
    lock_node_command || return 1
    is_binary_install || { echo "Rollback requires a binary installation" >&2; return 1; }
    python3 - "$APP_DIR" "${ROLLBACK_BACKUP_ID:?backup identity is required}" "$NODE_VERSION_REQUESTED" <<'PY'
import json,pathlib,re,sys
app,identity,version=sys.argv[1:]
if not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_.-]{0,95}',identity): raise SystemExit('Invalid backup identity')
manifest=json.loads((pathlib.Path(app)/'.update-backups'/identity/'manifest.json').read_text())
if not version or manifest.get('version')!=version: raise SystemExit('Backup version does not match rollback target')
PY
    [ "$?" -eq 0 ] || return 1
    node_guarded_boundary managed_binary_backup restore "$APP_DIR" "$ROLLBACK_BACKUP_ID" node "$APP_NAME" || return 1
    node_guarded_boundary systemctl restart "$APP_NAME.service" || return 1
    systemctl is-active --quiet "$APP_NAME.service" || return 1
    echo "Previous binary restored; fresh runtime verification remains required"
    finish_node_command || return 1
}

start_node_rollback_watchdog() {
    local previous_version watchdog
    local -a fence_args=()
    [[ "$UPDATE_OPERATION_ID" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]{0,95}$ ]] || return 1
    previous_version=$(jq -er '.version | select(length > 0)' "$APP_DIR/.update-backups/$UPDATE_OPERATION_ID/manifest.json") || return 1
    watchdog="antimage-node-rollback-$UPDATE_OPERATION_ID"
    if [ -n "${LEASE_GENERATION:-}" ]; then
        local watchdog_command="${COMMAND_ID}-watchdog"
        COMMAND_ID="$watchdog_command" node_command_fence accept-held || return 1
        fence_args=(--lease-generation "$LEASE_GENERATION" --fence-operation-id "$FENCE_OPERATION_ID" --command-id "$watchdog_command" --resource-generation "$RESOURCE_GENERATION" --resource-id "$RESOURCE_ID")
    fi
    systemd-run --unit="$watchdog" --on-active=300s --collect \
        --property=RuntimeMaxSec=120s --property=TimeoutStopSec=10s --property=KillMode=control-group \
        --setenv="ANTIMAGE_NODE_APP_NAME=$APP_NAME" \
        --setenv="ANTIMAGE_NODE_APP_DIR=$APP_DIR" \
        -- "/usr/local/bin/$APP_NAME" rollback --backup-id "$UPDATE_OPERATION_ID" --version "$previous_version" --operation-id "$UPDATE_OPERATION_ID-watchdog" "${fence_args[@]}"
}

commit_node_update_command() {
    check_running_as_root
    lock_node_command || return 1
    [[ "$UPDATE_OPERATION_ID" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]{0,95}$ ]] || return 1
    local watchdog="antimage-node-rollback-$UPDATE_OPERATION_ID"
    node_guarded_boundary systemctl stop "$watchdog.timer" || return 1
    # A running recovery service cannot safely be cancelled after restoration
    # may already have started. Keep the backup and let recovery finish.
    if systemctl is-active --quiet "$watchdog.service"; then
        echo "Automatic rollback is already running; cannot commit update" >&2
        return 1
    fi
    echo "Verified update accepted; recoverable backup retained"
    finish_node_command || return 1
}

restart_command() {
    help() {
        colorized_echo red "Usage: antimage-node restart [options]"
        echo
        echo "OPTIONS:"
        echo "  -h, --help        display this help message"
        echo "  -n, --no-logs     do not follow logs after starting"
    }
    
    local no_logs=false
    while [[ "$#" -gt 0 ]]; do
        case "$1" in
            -n|--no-logs)
                no_logs=true
            ;;
            -h|--help)
                help
                exit 0
            ;;
            *)
                echo "Error: Invalid option: $1" >&2
                help
                exit 0
            ;;
        esac
        shift
    done
    
    # Check if antimage-node is installed
    if ! is_antimage_node_installed; then
        colorized_echo red "AntiMage-node not installed!"
        exit 1
    fi
    
    if ! is_binary_install; then
        detect_compose
    fi
    
    down_antimage_node
    up_antimage_node
    
}

status_command() {
    # Check if antimage-node is installed
    if ! is_antimage_node_installed; then
        echo -n "Status: "
        colorized_echo red "Not Installed"
        exit 1
    fi
    
    if ! is_binary_install; then
        detect_compose
    fi
    
    if ! is_antimage_node_up; then
        echo -n "Status: "
        colorized_echo blue "Down"
        exit 1
    fi
    
    echo -n "Status: "
    colorized_echo green "Up"
    
    if is_binary_install; then
        systemctl status "$APP_NAME.service" --no-pager
        return
    fi

    json=$($COMPOSE -f $COMPOSE_FILE ps -a --format=json)
    services=$(echo "$json" | jq -r 'if type == "array" then .[] else . end | .Service')
    states=$(echo "$json" | jq -r 'if type == "array" then .[] else . end | .State')
    # Print out the service names and statuses
    for i in $(seq 0 $(expr $(echo $services | wc -w) - 1)); do
        service=$(echo $services | cut -d' ' -f $(expr $i + 1))
        state=$(echo $states | cut -d' ' -f $(expr $i + 1))
        echo -n "- $service: "
        if [ "$state" == "running" ]; then
            colorized_echo green $state
        else
            colorized_echo red $state
        fi
    done
}

logs_command() {
    help() {
        colorized_echo red "Usage: antimage-node logs [options]"
        echo ""
        echo "OPTIONS:"
        echo "  -h, --help        display this help message"
        echo "  -n, --no-follow   do not show follow logs"
    }
    
    local no_follow=false
    while [[ "$#" -gt 0 ]]; do
        case "$1" in
            -n|--no-follow)
                no_follow=true
            ;;
            -h|--help)
                help
                exit 0
            ;;
            *)
                echo "Error: Invalid option: $1" >&2
                help
                exit 0
            ;;
        esac
        shift
    done
    
    # Check if antimage is installed
    if ! is_antimage_node_installed; then
        colorized_echo red "AntiMage-node's not installed!"
        exit 1
    fi
    
    if ! is_binary_install; then
        detect_compose
    fi
    
    if ! is_antimage_node_up; then
        colorized_echo red "AntiMage-node is not up."
        exit 1
    fi
    
    if [ "$no_follow" = true ]; then
        show_antimage_node_logs
    else
        follow_antimage_node_logs
    fi
}

update_command() {
    check_running_as_root
    lock_node_command || return 1
    local node_version=""
    local service_rollback_dir=""
    local service_rollback_binary=""
    local service_rollback_metadata=""
    local service_rollback_env=""

    if ! is_antimage_node_installed; then
        colorized_echo red "AntiMage-node not installed!"
        exit 1
    fi

    if [ "$NODE_VERSION_SET" -eq 1 ]; then
        node_version="${NODE_VERSION_REQUESTED:-latest}"
        case "$node_version" in
            dev)
                set_branch_variables dev
            ;;
            latest|"")
                set_branch_variables master
                node_version="latest"
            ;;
            *)
                set_branch_variables master
                DOCKER_IMAGE="ghcr.io/devprogrmer/antimage-node:${node_version}"
            ;;
        esac
        echo "$BRANCH" > "$BRANCH_FILE"
    fi

    if ! is_binary_install; then
        detect_compose
    fi

    if [ -z "${RESOLVED_BUILD_JSON:-}" ]; then
        update_antimage_node_script
        reexec_updated_node_script
    fi

    if is_binary_install; then
        colorized_echo blue "Updating AntiMage-node binary files"
        UPDATE_OPERATION_ID="${UPDATE_OPERATION_ID:-node-$(date +%s%N)}"
        if [ -f "$APP_DIR/.update-backups/$UPDATE_OPERATION_ID/manifest.json" ]; then
            managed_binary_backup verify "$APP_DIR" "$UPDATE_OPERATION_ID" node "$APP_NAME" >/dev/null || return 1
        else
            managed_binary_backup create "$APP_DIR" "$UPDATE_OPERATION_ID" node "$APP_NAME" "$BINARY_NODE" "$BINARY_METADATA_FILE" "$APP_DIR/.env" || return 1
        fi
        service_rollback_dir="$APP_DIR/.update-backups/$UPDATE_OPERATION_ID"
    else
        colorized_echo blue "Pulling node image $DOCKER_IMAGE"
    fi
    update_antimage_node "$node_version" || return 1
    if is_binary_install && [ -n "${RESOLVED_BUILD_JSON:-}" ]; then
        start_node_rollback_watchdog || {
            node_guarded_boundary managed_binary_backup restore "$APP_DIR" "$UPDATE_OPERATION_ID" node "$APP_NAME" || { echo "Watchdog scheduling and backup restoration failed; manual recovery required" >&2; return 1; }
            return 1
        }
    fi
    if is_binary_install && [ -z "${RESOLVED_BUILD_JSON:-}" ]; then
        refresh_xray_installer_script || { colorized_echo red "Failed to refresh Xray installer"; return 1; }
    fi

    colorized_echo blue "Restarting AntiMage-node services"
    restart_fenced_node_pair() { down_antimage_node || return 1; up_antimage_node; }
    node_guarded_boundary restart_fenced_node_pair || return 1
    if ! is_antimage_node_up; then
        colorized_echo red "AntiMage-node update failed: service did not become active after restart"
        if is_binary_install && [ -n "$service_rollback_dir" ]; then
            colorized_echo yellow "Restoring previous AntiMage-node binary"
            node_guarded_boundary managed_binary_backup restore "$APP_DIR" "$UPDATE_OPERATION_ID" node "$APP_NAME" || { echo "Automatic rollback failed; manual recovery required" >&2; return 1; }
            node_guarded_boundary up_antimage_node || { echo "Previous node service could not restart; manual recovery required" >&2; return 1; }
        fi
        exit 1
    fi
    # Retain the operation-specific backup for verified or manual rollback.

    colorized_echo blue "AntiMage-node updated successfully"
    finish_node_command || return 1
}

identify_the_operating_system_and_architecture() {
    if [[ "$(uname)" == 'Linux' ]]; then
        case "$(uname -m)" in
            'i386' | 'i686')
                ARCH='32'
            ;;
            'amd64' | 'x86_64')
                ARCH='64'
            ;;
            'armv5tel')
                ARCH='arm32-v5'
            ;;
            'armv6l')
                ARCH='arm32-v6'
                grep Features /proc/cpuinfo | grep -qw 'vfp' || ARCH='arm32-v5'
            ;;
            'armv7' | 'armv7l')
                ARCH='arm32-v7a'
                grep Features /proc/cpuinfo | grep -qw 'vfp' || ARCH='arm32-v5'
            ;;
            'armv8' | 'aarch64')
                ARCH='arm64-v8a'
            ;;
            'mips')
                ARCH='mips32'
            ;;
            'mipsle')
                ARCH='mips32le'
            ;;
            'mips64')
                ARCH='mips64'
                lscpu | grep -q "Little Endian" && ARCH='mips64le'
            ;;
            'mips64le')
                ARCH='mips64le'
            ;;
            'ppc64')
                ARCH='ppc64'
            ;;
            'ppc64le')
                ARCH='ppc64le'
            ;;
            'riscv64')
                ARCH='riscv64'
            ;;
            's390x')
                ARCH='s390x'
            ;;
            *)
                echo "error: The architecture is not supported."
                exit 1
            ;;
        esac
    else
        echo "error: This operating system is not supported."
        exit 1
    fi
}

# Function to update the Xray core
get_xray_core() {
    identify_the_operating_system_and_architecture
    clear
    
    
    validate_version() {
        local version="$1"
        
        local response=$(curl -s "https://api.github.com/repos/XTLS/Xray-core/releases/tags/$version")
        if echo "$response" | grep -q '"message": "Not Found"'; then
            echo "invalid"
        else
            echo "valid"
        fi
    }
    
    
    print_menu() {
        clear
        echo -e "\033[1;32m==============================\033[0m"
        echo -e "\033[1;32m      Xray-core Installer     \033[0m"
        echo -e "\033[1;32m==============================\033[0m"
       current_version=$(get_current_xray_core_version)
        echo -e "\033[1;33m>>>> Current Xray-core version: \033[1;1m$current_version\033[0m"
        echo -e "\033[1;32m==============================\033[0m"
        echo -e "\033[1;33mAvailable Xray-core versions:\033[0m"
        for ((i=0; i<${#versions[@]}; i++)); do
            echo -e "\033[1;34m$((i + 1)):\033[0m ${versions[i]}"
        done
        echo -e "\033[1;32m==============================\033[0m"
        echo -e "\033[1;35mM:\033[0m Enter a version manually"
        echo -e "\033[1;31mQ:\033[0m Quit"
        echo -e "\033[1;32m==============================\033[0m"
    }
    
    
    latest_releases=$(curl -s "https://api.github.com/repos/XTLS/Xray-core/releases?per_page=$LAST_XRAY_CORES")
    
    
    versions=($(echo "$latest_releases" | grep -oP '"tag_name": "\K(.*?)(?=")'))
    
    while true; do
        print_menu
        read -p "Choose a version to install (1-${#versions[@]}), or press M to enter manually, Q to quit: " choice
        
        if [[ "$choice" =~ ^[1-9][0-9]*$ ]] && [ "$choice" -le "${#versions[@]}" ]; then
            
            choice=$((choice - 1))
            
            selected_version=${versions[choice]}
            break
            elif [ "$choice" == "M" ] || [ "$choice" == "m" ]; then
            while true; do
                read -p "Enter the version manually (e.g., v1.2.3): " custom_version
                if [ "$(validate_version "$custom_version")" == "valid" ]; then
                    selected_version="$custom_version"
                    break 2
                else
                    echo -e "\033[1;31mInvalid version or version does not exist. Please try again.\033[0m"
                fi
            done
            elif [ "$choice" == "Q" ] || [ "$choice" == "q" ]; then
            echo -e "\033[1;31mExiting.\033[0m"
            exit 0
        else
            echo -e "\033[1;31mInvalid choice. Please try again.\033[0m"
            sleep 2
        fi
    done
    
    echo -e "\033[1;32mSelected version $selected_version for installation.\033[0m"
    
    
if ! dpkg -s unzip >/dev/null 2>&1; then
    echo -e "\033[1;33mInstalling required packages...\033[0m"
    detect_os
    install_package unzip
fi

    
    
    mkdir -p $DATA_MAIN_DIR/xray-core
    cd $DATA_MAIN_DIR/xray-core
    
    
    
    xray_filename="Xray-linux-$ARCH.zip"
    xray_download_url="https://github.com/XTLS/Xray-core/releases/download/${selected_version}/${xray_filename}"
    
    echo -e "\033[1;33mDownloading Xray-core version ${selected_version} in the background...\033[0m"
    wget "${xray_download_url}" -q &
    wait
    
    
    echo -e "\033[1;33mExtracting Xray-core in the background...\033[0m"
    unzip -o "${xray_filename}" >/dev/null 2>&1 &
    wait
    rm "${xray_filename}"
}
get_current_xray_core_version() {
    XRAY_BINARY="$DATA_MAIN_DIR/xray-core/xray"
    if [ -f "$XRAY_BINARY" ]; then
        version_output=$("$XRAY_BINARY" -version 2>/dev/null)
        if [ $? -eq 0 ]; then
            version=$(echo "$version_output" | head -n1 | awk '{print $2}')
            echo "$version"
            return
        fi
    fi

    # If local binary is not found or failed, check in the Docker container
    CONTAINER_NAME="$APP_NAME"
    if command -v docker >/dev/null 2>&1 && docker ps --format '{{.Names}}' | grep -q "^$CONTAINER_NAME$"; then
        version_output=$(docker exec "$CONTAINER_NAME" xray -version 2>/dev/null)
        if [ $? -eq 0 ]; then
            # Extract the version number from the first line
            version=$(echo "$version_output" | head -n1 | awk '{print $2}')
            echo "$version (in container)"
            return
        fi
    fi

    echo "Not installed"
}

install_yq() {
    if command -v yq &>/dev/null; then
        colorized_echo green "yq is already installed."
        return
    fi

    identify_the_operating_system_and_architecture

    local base_url="https://github.com/mikefarah/yq/releases/latest/download"
    local yq_binary=""

    case "$ARCH" in
        '64' | 'x86_64')
            yq_binary="yq_linux_amd64"
            ;;
        'arm32-v7a' | 'arm32-v6' | 'arm32-v5' | 'armv7l')
            yq_binary="yq_linux_arm"
            ;;
        'arm64-v8a' | 'aarch64')
            yq_binary="yq_linux_arm64"
            ;;
        '32' | 'i386' | 'i686')
            yq_binary="yq_linux_386"
            ;;
        *)
            colorized_echo red "Unsupported architecture: $ARCH"
            exit 1
            ;;
    esac

    local yq_url="${base_url}/${yq_binary}"
    colorized_echo blue "Downloading yq from ${yq_url}..."

    if ! command -v curl &>/dev/null && ! command -v wget &>/dev/null; then
        colorized_echo yellow "Neither curl nor wget is installed. Attempting to install curl."
        install_package curl || {
            colorized_echo red "Failed to install curl. Please install curl or wget manually."
            exit 1
        }
    fi


    if command -v curl &>/dev/null; then
        if curl -L "$yq_url" -o /usr/local/bin/yq; then
            chmod +x /usr/local/bin/yq
            colorized_echo green "yq installed successfully!"
        else
            colorized_echo red "Failed to download yq using curl. Please check your internet connection."
            exit 1
        fi
    elif command -v wget &>/dev/null; then
        if wget -O /usr/local/bin/yq "$yq_url"; then
            chmod +x /usr/local/bin/yq
            colorized_echo green "yq installed successfully!"
        else
            colorized_echo red "Failed to download yq using wget. Please check your internet connection."
            exit 1
        fi
    fi


    if ! echo "$PATH" | grep -q "/usr/local/bin"; then
        export PATH="/usr/local/bin:$PATH"
    fi


    hash -r

    if command -v yq &>/dev/null; then
        colorized_echo green "yq is ready to use."
    elif [ -x "/usr/local/bin/yq" ]; then

        colorized_echo yellow "yq is installed at /usr/local/bin/yq but not found in PATH."
        colorized_echo yellow "You can add /usr/local/bin to your PATH environment variable."
    else
        colorized_echo red "yq installation failed. Please try again or install manually."
        exit 1
    fi
}



update_core_command() {
    check_running_as_root
    get_xray_core

    if is_binary_install; then
        set_env_value "XRAY_EXECUTABLE_PATH" "$DATA_MAIN_DIR/xray-core/xray"
        set_env_value "XRAY_ASSETS_PATH" "$DATA_MAIN_DIR/xray-core"
        colorized_echo red "Restarting AntiMage-node..."
        systemctl restart "$APP_NAME.service"
        colorized_echo blue "Installation of XRAY-CORE version $selected_version completed."
        return
    fi

    if ! command -v yq &>/dev/null; then
        echo "yq is not installed. Installing yq..."
        install_yq
    fi

    if ! grep -q 'XRAY_EXECUTABLE_PATH: "/var/lib/antimage-node/xray-core/xray"' "$COMPOSE_FILE"; then
        yq eval '.services."antimage-node".environment.XRAY_EXECUTABLE_PATH = "/var/lib/antimage-node/xray-core/xray"' -i "$COMPOSE_FILE"
    fi

    if ! yq eval ".services.\"antimage-node\".volumes[] | select(. == \"${DATA_MAIN_DIR}:/var/lib/antimage-node\")" "$COMPOSE_FILE" &>/dev/null; then
        yq eval ".services.\"antimage-node\".volumes += \"${DATA_MAIN_DIR}:/var/lib/antimage-node\"" -i "$COMPOSE_FILE"
    fi
    
    # Restart AntiMage-node
    colorized_echo red "Restarting AntiMage-node..."
    $APP_NAME restart -n
    colorized_echo blue "Installation of XRAY-CORE version $selected_version completed."
}


check_editor() {
    if [ -z "$EDITOR" ]; then
        if command -v nano >/dev/null 2>&1; then
            EDITOR="nano"
            elif command -v vi >/dev/null 2>&1; then
            EDITOR="vi"
        else
            detect_os
            install_package nano
            EDITOR="nano"
        fi
    fi
}


edit_command() {
    detect_os
    check_editor
    if is_binary_install; then
        ensure_env_file
        $EDITOR "$ENV_FILE"
        return
    fi
    if [ -f "$COMPOSE_FILE" ]; then
        $EDITOR "$COMPOSE_FILE"
    else
        colorized_echo red "Compose file not found at $COMPOSE_FILE"
        exit 1
    fi
}

get_node_current_version() {
    local version=""
    if [ -f "$BINARY_METADATA_FILE" ]; then
        version=$(sed -nE 's/.*"tag"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/p' "$BINARY_METADATA_FILE" | head -n 1)
    fi
    if [ -z "$version" ] && [ -f "$BRANCH_FILE" ]; then
        version=$(tr -d '[:space:]' < "$BRANCH_FILE")
    fi
    printf '%s\n' "${version:-unknown}"
}

get_node_service_status() {
    if is_antimage_node_up; then
        echo "running"
    else
        echo "stopped"
    fi
}

print_node_menu_status_summary() {
    local service_port xray_api_port
    service_port=$(get_env_value "SERVICE_PORT")
    xray_api_port=$(get_env_value "XRAY_API_PORT")
    service_port="${service_port:-62050}"
    xray_api_port="${xray_api_port:-62051}"
    ui_status_row "Version" "$(get_node_current_version)"
    ui_status_row "Service" "$(get_node_service_status)"
    ui_status_row "Mode" "$(get_install_mode)"
    ui_status_row "Node IP" "${NODE_IP:-unknown}"
    ui_status_row "Service port" "$service_port"
    ui_status_row "Xray API" "$xray_api_port"
    ui_status_row "Cert" "$CERT_FILE"
}

usage() {
    colorized_echo blue "================================"
    colorized_echo magenta "       $APP_NAME Node CLI Help"
    colorized_echo blue "================================"
    colorized_echo cyan "Usage:"
    echo "  $APP_NAME [command]"
    echo

    colorized_echo cyan "Commands:"
    colorized_echo yellow "  up              â€“ Start services"
    colorized_echo yellow "  down            â€“ Stop services"
    colorized_echo yellow "  restart         â€“ Restart services"
    colorized_echo yellow "  status          â€“ Show status"
    colorized_echo yellow "  logs            â€“ Show logs"
    colorized_echo yellow "  install         - Install/reinstall AntiMage-node"
    colorized_echo yellow "  update          - Update to latest/dev or a specific version"
    colorized_echo yellow "  uninstall       - Uninstall AntiMage-node"
    colorized_echo blue "  script-install  - Install AntiMage-node script"
    colorized_echo blue "  script-update   - Update AntiMage-node CLI script"
    colorized_echo blue "  script-uninstall  - Uninstall AntiMage-node script"
    colorized_echo yellow "  edit            - Edit docker-compose.yml or binary .env (via nano or vi)"
    colorized_echo yellow "  core-update     â€“ Update/Change Xray core"
    
    echo
    colorized_echo cyan "Node Information:"
    colorized_echo magenta "  Cert file path: $CERT_FILE"
    colorized_echo magenta "  Node IP: $NODE_IP"
    echo
    colorized_echo cyan "Install/update options:"
    case "$(script_install_mode)" in
        docker)
            colorized_echo magenta "  This script installs Docker node mode only."
            ;;
        binary)
            colorized_echo magenta "  This script installs binary node mode only."
            ;;
    esac
    colorized_echo magenta "  --dev or --version vX.Y.Z"
    echo
    current_version=$(get_current_xray_core_version)
    colorized_echo cyan "Current Xray-core version: " 1  # 1 for bold
    colorized_echo magenta "$current_version" 1
    echo
    DEFAULT_SERVICE_PORT="62050"
    DEFAULT_XRAY_API_PORT="62051"
    
    if [ -f "$COMPOSE_FILE" ]; then
        SERVICE_PORT=$(awk -F': ' '/SERVICE_PORT:/ {gsub(/"/, "", $2); print $2}' "$COMPOSE_FILE")
        XRAY_API_PORT=$(awk -F': ' '/XRAY_API_PORT:/ {gsub(/"/, "", $2); print $2}' "$COMPOSE_FILE")
    elif [ -f "$ENV_FILE" ]; then
        SERVICE_PORT=$(get_env_value "SERVICE_PORT")
        XRAY_API_PORT=$(get_env_value "XRAY_API_PORT")
    fi
    
    SERVICE_PORT=${SERVICE_PORT:-$DEFAULT_SERVICE_PORT}
    XRAY_API_PORT=${XRAY_API_PORT:-$DEFAULT_XRAY_API_PORT}

    colorized_echo cyan "Ports:"
    colorized_echo magenta "  Control port: $SERVICE_PORT"
    colorized_echo magenta "  Local Xray API port: $XRAY_API_PORT"
    
    colorized_echo blue "================================="
    echo
}

menu_commands() {
    echo "up down restart status logs install update uninstall script-install script-update script-uninstall core-update edit help"
}

menu_category_for() {
    case "$1" in
        up|down|restart|status|logs) echo "Node runtime" ;;
        install|update|uninstall) echo "Install and update" ;;
        script-install|script-update|script-uninstall) echo "Script management" ;;
        core-update|edit) echo "Tools" ;;
        *) echo "Help" ;;
    esac
}

menu_description_for() {
    case "$1" in
        up) echo "Start services" ;;
        down) echo "Stop services" ;;
        restart) echo "Restart services" ;;
        status) echo "Show status" ;;
        logs) echo "Show logs" ;;
        install) echo "Install/reinstall AntiMage-node" ;;
        update) echo "Update to latest version" ;;
        uninstall) echo "Uninstall AntiMage-node" ;;
        script-install) echo "Install AntiMage-node script" ;;
        script-update) echo "Update AntiMage-node CLI script" ;;
        script-uninstall) echo "Uninstall AntiMage-node script" ;;
        core-update) echo "Update/Change Xray core" ;;
        edit) echo "Edit docker-compose.yml or binary .env" ;;
        help) echo "Show this help message" ;;
        *) echo "" ;;
    esac
}

print_menu() {
    local selected="${1:-0}"
    local previous_category=""
    local idx=1
    local cmd category desc is_selected columns tip_width tip
    ui_header "$APP_NAME" "AntiMage-node control center"
    ui_section "Status"
    print_node_menu_status_summary
    ui_section "Actions"
    for cmd in $(menu_commands); do
        category=$(menu_category_for "$cmd")
        if [ "$category" != "$previous_category" ]; then
            ui_menu_category "$category"
            previous_category="$category"
        fi
        desc=$(menu_description_for "$cmd")
        is_selected=0
        [ "$idx" -eq "$selected" ] && is_selected=1
        ui_menu_item "$idx" "$cmd" "$desc" "$is_selected"
        idx=$((idx + 1))
    done
    printf "\n"
    columns=$(ui_terminal_columns)
    tip_width=$((columns - 1))
    tip="Tip: arrow keys move, Enter selects, q exits; numbers and commands also work."
    ui_color "38;5;245" "${tip:0:$tip_width}"
    printf "\n"
    echo
}

ui_menu_lines_below_item() {
    local target="$1"
    local idx=1 lines=4 previous_category="" cmd category
    for cmd in $(menu_commands); do
        category=$(menu_category_for "$cmd")
        if [ "$idx" -gt "$target" ]; then
            [ "$category" != "$previous_category" ] && lines=$((lines + 2))
            lines=$((lines + 1))
        fi
        previous_category="$category"
        idx=$((idx + 1))
    done
    printf "%s" "$lines"
}

ui_redraw_menu_item() {
    local index="$1" selected="$2" distance
    local commands=($(menu_commands))
    local command="${commands[$((index - 1))]}"
    distance=$(ui_menu_lines_below_item "$index")
    printf "\033[%sA\r\033[2K" "$distance"
    ui_menu_item "$index" "$command" "$(menu_description_for "$command")" "$selected"
    if [ "$distance" -gt 1 ]; then
        printf "\033[%sB\r" "$((distance - 1))"
    fi
}

ui_menu_prompt() {
    local columns prompt
    columns=$(ui_terminal_columns)
    if [ "$columns" -lt 30 ]; then
        prompt="Select: "
    elif [ "$columns" -lt 55 ]; then
        prompt="Select (arrows/Enter/number): "
    else
        prompt="Select option (arrow keys, Enter, number, command): "
    fi
    prompt="${prompt:0:$((columns - 1))}"
    ui_color "38;5;45;1" "$prompt"
}

map_choice_to_command() {
    local commands=($(menu_commands))
    if [[ "$1" =~ ^[0-9]+$ ]] && [ "$1" -ge 1 ] && [ "$1" -le "${#commands[@]}" ]; then
        echo "${commands[$(($1 - 1))]}"
        return
    fi
    echo "$1"
}

read_menu_command() {
    MENU_COMMAND=""
    if ! ui_supports_cursor_motion; then
        print_menu
        ui_color "38;5;45;1" "Select option"
        printf " "
        ui_color "38;5;245" "(number or command): "
        read -r user_choice
        [ -z "$user_choice" ] && return 1
        MENU_COMMAND=$(map_choice_to_command "$user_choice")
        return
    fi

    local commands=($(menu_commands))
    local selected=1
    local action kind value mapped previous_selected
    ui_clear
    print_menu "$selected"
    ui_menu_prompt
    while true; do
        action=$(ui_read_menu_choice "$selected" "${#commands[@]}") || return 1
        kind="${action%%:*}"
        value="${action#*:}"
        case "$kind" in
            move)
                previous_selected="$selected"
                selected="$value"
                if [ "$selected" -ne "$previous_selected" ]; then
                    ui_redraw_menu_item "$previous_selected" 0
                    ui_redraw_menu_item "$selected" 1
                    printf "\r\033[2K"
                    ui_menu_prompt
                fi
            ;;
            enter)
                MENU_COMMAND="${commands[$(($value - 1))]}"
                printf "\n"
                return
            ;;
            value)
                mapped=$(map_choice_to_command "$value")
                [ -n "$mapped" ] && MENU_COMMAND="$mapped"
                printf "\n"
                return
            ;;
            quit)
                printf "\n"
                return 1
            ;;
        esac
    done
}

dispatch_command() {
    local cmd="$1"
    shift || true
    case "$cmd" in
        install|install-script|script-install)
            if [ -e "$INSTALL_MODE_FILE" ] || [ -e "$BINARY_NODE" ]; then
                echo "Managed Node reinstall must use the authorized update/recovery operation" >&2
                return 1
            fi
            ;;
        update|rollback|update-commit)
            require_node_command_ownership || return 1
            ;;
        up|down|restart|core-update|edit|uninstall|update-script|script-update|uninstall-script|script-uninstall)
            require_node_command_ownership || return 1
            lock_node_command || return 1
            ;;
    esac
    case "$cmd" in
        help|install|install-script|script-install|update-script|script-update|uninstall-script|script-uninstall)
            ;;
        *)
            ensure_script_matches_installed_mode
            ;;
    esac
    case "$cmd" in
        install)
            if [ ! -t 0 ] && [ -r /dev/tty ]; then
                install_command </dev/tty
            else
                install_command
            fi
        ;;
        update) update_command ;;
        rollback) rollback_command ;;
        update-commit) commit_node_update_command ;;
        fence-accept) check_running_as_root; node_command_fence accept ;;
        fenced-restart) check_running_as_root; [ -n "${LEASE_GENERATION:-}" ] || return 1; lock_node_command && node_guarded_boundary systemctl restart "$APP_NAME.service" && finish_node_command ;;
        fenced-reboot) check_running_as_root; [ -n "${LEASE_GENERATION:-}" ] || return 1; lock_node_command && node_guarded_boundary systemctl reboot && finish_node_command ;;
        uninstall) node_guarded_boundary uninstall_command && finish_node_command ;;
        up) node_guarded_boundary up_command && finish_node_command ;;
        down) node_guarded_boundary down_command && finish_node_command ;;
        restart) node_guarded_boundary restart_command && finish_node_command ;;
        status) status_command ;;
        logs) logs_command ;;
        core-update) node_guarded_boundary update_core_command && finish_node_command ;;
        install-script|script-install) install_antimage_node_script ;;
        update-script|script-update) node_guarded_boundary install_antimage_node_script && finish_node_command ;;
        uninstall-script|script-uninstall) node_guarded_boundary uninstall_antimage_node_script && finish_node_command ;;
        edit) node_guarded_boundary edit_command && finish_node_command ;;
        help) usage ;;
        *) usage ;;
    esac
}

if [ -z "${COMMAND:-}" ]; then
    read_menu_command || exit 0
    COMMAND="$MENU_COMMAND"
fi

dispatch_command "$COMMAND"
