#!/usr/bin/env bash
set -e

INSTALL_DIR="/opt"
if [ -z "$APP_NAME" ]; then
    APP_NAME="antimage"
fi
ensure_valid_app_name() {
    local candidate="${APP_NAME:-antimage}"
    if ! [[ "$candidate" =~ ^[a-zA-Z0-9][a-zA-Z0-9_-]*$ ]]; then
        candidate="antimage"
        echo "Invalid app name detected. Falling back to default: $candidate"
    fi
    APP_NAME="$candidate"
}
ensure_valid_app_name
APP_DIR="$INSTALL_DIR/$APP_NAME"
DATA_DIR="/var/lib/$APP_NAME"
COMPOSE_FILE="$APP_DIR/docker-compose.yml"
ENV_FILE="$APP_DIR/.env"
LAST_XRAY_CORES=10
CERTS_BASE="/var/lib/$APP_NAME/certs"
ANTIMAGE_REPO="${ANTIMAGE_REPO:-antimagepanel/antimage}"
ANTIMAGE_REF="${ANTIMAGE_REF:-master}"
ANTIMAGE_RAW_BASE="${ANTIMAGE_RAW_BASE:-https://raw.githubusercontent.com/${ANTIMAGE_REPO}/${ANTIMAGE_REF}}"
ANTIMAGE_SCRIPT_BASE_URL_EXPLICIT=0
if [ -n "${ANTIMAGE_SCRIPT_BASE_URL+x}" ]; then
    ANTIMAGE_SCRIPT_BASE_URL_EXPLICIT=1
fi
ANTIMAGE_SCRIPT_BASE_URL="${ANTIMAGE_SCRIPT_BASE_URL:-${ANTIMAGE_RAW_BASE}/scripts/antimage}"
ANTIMAGE_RELEASE_REPO="${ANTIMAGE_RELEASE_REPO:-antimagepanel/antimage}"
ANTIMAGE_BINARY_DEV_BRANCH="${ANTIMAGE_BINARY_DEV_BRANCH:-dev}"
ANTIMAGE_BINARY_WORKFLOW_NAME="${ANTIMAGE_BINARY_WORKFLOW_NAME:-binary-build}"
ANTIMAGE_BINARY_DEV_MANIFEST_BRANCH="${ANTIMAGE_BINARY_DEV_MANIFEST_BRANCH:-dev-build-manifest}"
ANTIMAGE_BINARY_DEV_MANIFEST_PATH="${ANTIMAGE_BINARY_DEV_MANIFEST_PATH:-dev-builds.json}"
ANTIMAGE_BINARY_DEV_MANIFEST_URL="${ANTIMAGE_BINARY_DEV_MANIFEST_URL:-}"
ANTIMAGE_BINARY_DEV_RELEASE_TAG="${ANTIMAGE_BINARY_DEV_RELEASE_TAG:-dev-builds}"
INSTALL_MODE_FILE="$APP_DIR/.install-mode"
CHANNEL_FILE="$APP_DIR/.channel"
BINARY_BIN_DIR="$APP_DIR/bin"
BINARY_SERVER="$BINARY_BIN_DIR/antimage-server"
BINARY_CLI="$BINARY_BIN_DIR/antimage-cli"
BINARY_CLI_LAUNCHER="/usr/local/bin/antimage-cli"
BINARY_METADATA_FILE="$APP_DIR/.binary-release.json"
BINARY_ARTIFACT_PREFIX="${BINARY_ARTIFACT_PREFIX:-antimage-binaries}"
BINARY_SERVICE_UNIT="/etc/systemd/system/$APP_NAME.service"
CERTBOT_VENV_DIR="$APP_DIR/certbot-venv"
CERTBOT_BIN=""
PARSED_DOMAINS=()
ANTIMAGE_SCRIPT_FLAVOR="${ANTIMAGE_SCRIPT_FLAVOR:-binary}"
ANTIMAGE_SCRIPT_SOURCE_FILE="${ANTIMAGE_SCRIPT_SOURCE_FILE:-antimage-binary.sh}"
ANTIMAGE_SCRIPT_INSTALL_PATH="${ANTIMAGE_SCRIPT_INSTALL_PATH:-/usr/local/bin/antimage}"
ANTIMAGE_MYSQL_CONFIG_ROOT="${ANTIMAGE_MYSQL_CONFIG_ROOT:-/etc/mysql}"

colorized_echo() {
    local color=$1
    local text=$2
    
    case $color in
        "red")
        printf "\e[91m${text}\e[0m\n";;
        "green")
        printf "\e[92m${text}\e[0m\n";;
        "yellow")
        printf "\e[93m${text}\e[0m\n";;
        "blue")
        printf "\e[94m${text}\e[0m\n";;
        "magenta")
        printf "\e[95m${text}\e[0m\n";;
        "cyan")
        printf "\e[96m${text}\e[0m\n";;
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
    ui_color "38;5;39" "────────────────────────────────────────────────────────────"
    printf "\n"
}

ui_header() {
    local title="$1"
    local subtitle="${2:-}"
    printf "\n"
    ui_color "38;5;45;1" "╭──────────────────────────────────────────────────────────╮"
    printf "\n  "
    ui_color "38;5;231;1" "$title"
    printf "\n"
    if [ -n "$subtitle" ]; then
        printf "  "
        ui_color "38;5;117" "$subtitle"
        printf "\n"
    fi
    ui_color "38;5;45;1" "╰──────────────────────────────────────────────────────────╯"
    printf "\n"
}

ui_section() {
    printf "\n"
    ui_color "38;5;45;1" "◆ $1"
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

ui_read_yes_no() {
    local prompt="$1"
    local default_value="${2:-n}"
    local answer suffix
    if [ "$default_value" = "y" ]; then
        suffix="Y/n"
    else
        suffix="y/N"
    fi
    while true; do
        printf "%s [%s]: " "$prompt" "$suffix"
        IFS= read -r answer
        answer=$(echo "$answer" | tr '[:upper:]' '[:lower:]')
        if [ -z "$answer" ]; then
            answer="$default_value"
        fi
        case "$answer" in
            y|yes) return 0 ;;
            n|no) return 1 ;;
            *) colorized_echo yellow "Please answer y or n." ;;
        esac
    done
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
    local frames=("⠋" "⠙" "⠹" "⠸" "⠼" "⠴" "⠦" "⠧" "⠇" "⠏")
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
        ui_color "38;5;82;1" "✓"
        printf " %s\n" "$message"
        rm -f "$log_file"
        return 0
    fi

    ui_color "38;5;196;1" "✗"
    printf " %s\n" "$message"
    tail -n 80 "$log_file" >&2 || true
    rm -f "$log_file"
    return "$status"
}

format_ANTIMAGE_journal_logs() {
    while IFS= read -r line; do
        local log_time=""
        local message="$line"
        if [[ "$line" =~ ^[0-9-]+[[:space:]T]([0-9]{2}:[0-9]{2}:[0-9]{2})(\.[0-9]+)?([+-][0-9:]+|Z)?[[:space:]][^[:space:]]+[[:space:]][^:]+:[[:space:]](.*)$ ]]; then
            log_time="${BASH_REMATCH[1]}"
            message="${BASH_REMATCH[4]}"
        elif [[ "$line" =~ ^[A-Za-z]{3}[[:space:]][[:space:][:digit:]][[:digit:]][[:space:]]([0-9]{2}:[0-9]{2}:[0-9]{2})[[:space:]][^[:space:]]+[[:space:]][^:]+:[[:space:]](.*)$ ]]; then
            log_time="${BASH_REMATCH[1]}"
            message="${BASH_REMATCH[2]}"
        elif [[ "$line" =~ ^([0-9]{2}:[0-9]{2}:[0-9]{2})[[:space:]][^[:space:]]+[[:space:]][^:]+:[[:space:]](.*)$ ]]; then
            log_time="${BASH_REMATCH[1]}"
            message="${BASH_REMATCH[2]}"
        fi
        if [[ "$message" =~ ^[0-9]{4}/[0-9]{2}/[0-9]{2}[[:space:]][0-9]{2}:[0-9]{2}:[0-9]{2}[[:space:]](.*)$ ]]; then
            message="${BASH_REMATCH[1]}"
        fi
        if [ -z "$log_time" ]; then
            printf "%s\n" "$message"
            continue
        fi
        ui_color "38;5;208;1" "antimage"
        printf "-"
        ui_color "38;5;245" "$log_time"
        printf ": "
        if [[ "$message" =~ ^\[([^]]+)\][[:space:]](DEBUG|INFO|WARN|ERROR)[[:space:]](.*)$ ]]; then
            local component="${BASH_REMATCH[1]}"
            local level="${BASH_REMATCH[2]}"
            local text="${BASH_REMATCH[3]}"
            local component_color="38;5;45;1"
            local level_color="38;5;250"
            case "$component" in
                Admin) component_color="38;5;141;1" ;;
                Database) component_color="38;5;220;1" ;;
                Node) component_color="38;5;45;1" ;;
                Runtime) component_color="38;5;82;1" ;;
                Telegram) component_color="38;5;39;1" ;;
                User) component_color="38;5;213;1" ;;
                Webhook) component_color="38;5;214;1" ;;
            esac
            case "$level" in
                DEBUG) level_color="38;5;245" ;;
                INFO) level_color="38;5;82" ;;
                WARN) level_color="38;5;220;1" ;;
                ERROR) level_color="38;5;196;1" ;;
            esac
            ui_color "$component_color" "$component"
            printf " "
            ui_color "$level_color" "$level"
            printf " : %s\n" "$text"
        else
            printf "%s\n" "$message"
        fi
    done
}

journal_output_format() {
    if journalctl -o short-iso --no-pager -n 0 >/dev/null 2>&1; then
        echo "short-iso"
    else
        echo "short"
    fi
}

humanize_seconds() {
    local seconds="${1:-0}"
    local days hours minutes
    if ! [[ "$seconds" =~ ^[0-9]+$ ]]; then
        echo "-"
        return
    fi
    days=$((seconds / 86400))
    hours=$(((seconds % 86400) / 3600))
    minutes=$(((seconds % 3600) / 60))
    seconds=$((seconds % 60))
    if [ "$days" -gt 0 ]; then
        printf "%sd %sh %sm\n" "$days" "$hours" "$minutes"
    elif [ "$hours" -gt 0 ]; then
        printf "%sh %sm\n" "$hours" "$minutes"
    elif [ "$minutes" -gt 0 ]; then
        printf "%sm %ss\n" "$minutes" "$seconds"
    else
        printf "%ss\n" "$seconds"
    fi
}

get_summary_compose() {
    if docker compose version >/dev/null 2>&1; then
        echo "docker compose"
    elif docker-compose version >/dev/null 2>&1; then
        echo "docker-compose"
    else
        echo ""
    fi
}

get_current_ANTIMAGE_version() {
    local version=""
    if [ -f "$CHANNEL_FILE" ]; then
        version=$(tr -d '[:space:]' < "$CHANNEL_FILE")
    fi
    if [ -z "$version" ] && [ -f "$COMPOSE_FILE" ]; then
        version=$(grep -E "image:.*antimagepanel/antimage:" "$COMPOSE_FILE" | head -n 1 | sed -E 's/.*antimagepanel\/antimage:([^"[:space:]]+).*/\1/')
    fi
    printf '%s\n' "${version:-unknown}"
}

get_docker_container_id() {
    local compose_cmd
    compose_cmd=$(get_summary_compose)
    if [ -z "$compose_cmd" ] || [ ! -f "$COMPOSE_FILE" ]; then
        echo ""
        return
    fi
    $compose_cmd -f "$COMPOSE_FILE" -p "$APP_NAME" ps -q antimage 2>/dev/null | head -n 1
}

get_docker_uptime() {
    local container_id started started_epoch now
    container_id=$(get_docker_container_id)
    if [ -z "$container_id" ]; then
        echo "-"
        return
    fi
    started=$(docker inspect -f '{{.State.StartedAt}}' "$container_id" 2>/dev/null || true)
    started_epoch=$(date -d "$started" +%s 2>/dev/null || echo "")
    now=$(date +%s)
    if [ -z "$started_epoch" ]; then
        echo "-"
        return
    fi
    humanize_seconds "$((now - started_epoch))"
}

get_xray_runtime_status() {
    local container_id
    container_id=$(get_docker_container_id)
    if [ -n "$container_id" ] && docker exec "$container_id" pgrep -x xray >/dev/null 2>&1; then
        echo "running"
    else
        echo "stopped"
    fi
}

print_menu_status_summary() {
    local container_id service_status="stopped"
    local version uptime xray_status
    container_id=$(get_docker_container_id)
    if [ -n "$container_id" ] && [ "$(docker inspect -f '{{.State.Running}}' "$container_id" 2>/dev/null)" = "true" ]; then
        service_status="running"
    fi
    version=$(get_current_ANTIMAGE_version)
    uptime=$(get_docker_uptime)
    xray_status=$(get_xray_runtime_status)
    ui_status_row "Version" "${version}"
    ui_status_row "Service" "${service_status}"
    ui_status_row "Mode" "$(get_install_mode)"
    ui_status_row "Xray" "${xray_status}"
    ui_status_row "Uptime" "${uptime}"
}

set_ANTIMAGE_source_ref() {
    local ref="${1:-dev}"
    ANTIMAGE_REF="$ref"
    ANTIMAGE_RAW_BASE="https://raw.githubusercontent.com/${ANTIMAGE_REPO}/${ANTIMAGE_REF}"
    if [ "${ANTIMAGE_SCRIPT_BASE_URL_EXPLICIT:-0}" != "1" ]; then
        ANTIMAGE_SCRIPT_BASE_URL="${ANTIMAGE_RAW_BASE}/scripts/antimage"
    fi
}

set_ANTIMAGE_source_for_version() {
    case "${1:-latest}" in
        dev)
            set_ANTIMAGE_source_ref "$ANTIMAGE_BINARY_DEV_BRANCH"
            ;;
        dev-*)
            set_ANTIMAGE_source_ref "$ANTIMAGE_BINARY_DEV_BRANCH"
            ;;
        v[0-9]*)
            set_ANTIMAGE_source_ref "$1"
            ;;
        *)
            set_ANTIMAGE_source_ref "master"
            ;;
    esac
}

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
        ui_spinner_run "Updating package index" apt_update_with_repo_repair -o Acquire::AllowReleaseInfoChange=true -o Acquire::AllowReleaseInfoChange::Label=true
    elif [[ "$OS" == "CentOS"* ]] || [[ "$OS" == "AlmaLinux"* ]]; then
        PKG_MANAGER="yum"
        ui_spinner_run "Updating package index" "$PKG_MANAGER" update -y -q
        ui_spinner_run "Installing EPEL repository" "$PKG_MANAGER" install -y -q epel-release
    elif [ "$OS" == "Fedora"* ]; then
        PKG_MANAGER="dnf"
        ui_spinner_run "Updating package index" "$PKG_MANAGER" update -q -y
    elif [ "$OS" == "Arch" ]; then
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

install_package_impl() {
    local PACKAGE="$1"
    if [[ "$OS" == "Ubuntu"* ]] || [[ "$OS" == "Debian"* ]]; then
        DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=a $PKG_MANAGER -y -qq install "$PACKAGE" \
            -o Dpkg::Options::="--force-confdef" \
            -o Dpkg::Options::="--force-confold"
    elif [[ "$OS" == "CentOS"* ]] || [[ "$OS" == "AlmaLinux"* ]]; then
        $PKG_MANAGER install -y -q "$PACKAGE"
    elif [ "$OS" == "Fedora"* ]; then
        $PKG_MANAGER install -y -q "$PACKAGE"
    elif [ "$OS" == "Arch" ]; then
        $PKG_MANAGER -S --noconfirm --quiet "$PACKAGE"
    elif [[ "$OS" == "openSUSE"* ]]; then
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

ensure_python3_venv() {
    detect_os
    if [[ "$OS" == "Ubuntu"* ]] || [[ "$OS" == "Debian"* ]]; then
        PY_VER=$(python3 -c 'import sys; print(f"%s.%s" % (sys.version_info.major, sys.version_info.minor))' 2>/dev/null || echo "3")
        install_package "python${PY_VER}-venv"
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

detect_compose() {
    if is_binary_install; then
        return 0
    fi

    # Check if docker compose command exists
    if docker compose version >/dev/null 2>&1; then
        COMPOSE='docker compose'
    elif docker-compose version >/dev/null 2>&1; then
        COMPOSE='docker-compose'
    else
        colorized_echo red "docker compose not found"
        exit 1
    fi
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
    case "${ANTIMAGE_SCRIPT_FLAVOR:-docker}" in
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
            colorized_echo red "Invalid script flavor: $ANTIMAGE_SCRIPT_FLAVOR" >&2
            exit 1
            ;;
    esac
}

get_install_mode() {
    if [ -f "$INSTALL_MODE_FILE" ]; then
        mode=$(tr -d '[:space:]' < "$INSTALL_MODE_FILE")
        normalize_install_mode "$mode"
        return
    fi

    if [ -x "$BINARY_SERVER" ] || [ -f "$BINARY_SERVICE_UNIT" ]; then
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
    requested_mode=$(normalize_install_mode "${1:-${ANTIMAGE_INSTALL_MODE:-}}")

    if [ -n "$forced_mode" ]; then
        if [ -n "$requested_mode" ] && [ "$requested_mode" != "$forced_mode" ]; then
            colorized_echo red "This script is dedicated to ${forced_mode} installs. Use the matching antimage script for $requested_mode." >&2
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

    colorized_echo cyan "Select antimage installation mode:" >&2
    colorized_echo yellow "  1) Dockerized (recommended for full database support)" >&2
    colorized_echo yellow "  2) Binary (native systemd service)" >&2
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

select_ANTIMAGE_version() {
    local requested_version="${1:-}"
    local install_mode="${2:-docker}"

    if [ -n "$requested_version" ]; then
        echo "$requested_version"
        return
    fi

    if [ ! -t 0 ]; then
        echo "latest"
        return
    fi

    colorized_echo cyan "Select antimage release channel for ${install_mode} mode:" >&2
    colorized_echo yellow "  1) latest (stable release)" >&2
    if [ "$install_mode" = "binary" ]; then
        colorized_echo yellow "  2) dev (latest successful binary build from branch ${ANTIMAGE_BINARY_DEV_BRANCH})" >&2
    else
        colorized_echo yellow "  2) dev (latest Docker image from branch ${ANTIMAGE_BINARY_DEV_BRANCH})" >&2
    fi
    read -r -p "Release channel [1]: " ANTIMAGE_version_answer

    case "$ANTIMAGE_version_answer" in
        2|dev|Dev)
            echo "dev"
            ;;
        ""|1|latest|Latest|stable|Stable)
            echo "latest"
            ;;
        *)
            colorized_echo red "Invalid release channel selection."
            exit 1
            ;;
    esac
}

write_ANTIMAGE_channel() {
    local channel="${1:-latest}"
    mkdir -p "$APP_DIR"
    echo "$channel" > "$CHANNEL_FILE"
}

get_installed_ANTIMAGE_channel() {
    local channel
    local image_tag
    local metadata_tag

    if [ -f "$CHANNEL_FILE" ]; then
        channel=$(tr -d '[:space:]' < "$CHANNEL_FILE")
        if [ -n "$channel" ]; then
            echo "$channel"
            return
        fi
    fi

    if [ -f "$BINARY_METADATA_FILE" ]; then
        metadata_tag=$(sed -nE 's/.*"tag"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/p' "$BINARY_METADATA_FILE" | head -n 1)
        if [[ "$metadata_tag" == dev-* ]]; then
            echo "dev"
            return
        elif [ -n "$metadata_tag" ] && [ "$metadata_tag" != "latest" ]; then
            echo "$metadata_tag"
            return
        fi
    fi

    if [ -f "$COMPOSE_FILE" ]; then
        image_tag=$(grep -E "image:.*antimagepanel/antimage:" "$COMPOSE_FILE" | head -n 1 | sed -E 's/.*antimagepanel\/antimage:([^"[:space:]]+).*/\1/')
        if [ -n "$image_tag" ]; then
            echo "$image_tag"
            return
        fi
    fi

    echo "latest"
}

install_ANTIMAGE_script() {
    local source_version="${1:-}"
    local temp_script
    if [ -n "$source_version" ]; then
        set_ANTIMAGE_source_for_version "$source_version"
    elif is_ANTIMAGE_installed; then
        set_ANTIMAGE_source_for_version "$(get_installed_ANTIMAGE_channel)"
    fi
    SCRIPT_URL="$ANTIMAGE_SCRIPT_BASE_URL/$ANTIMAGE_SCRIPT_SOURCE_FILE"
    temp_script=$(mktemp)
    ui_spinner_run "Downloading antimage command script" curl -fsSL "$SCRIPT_URL" -o "$temp_script"
    if head -n 1 "$temp_script" | grep -qi "<!DOCTYPE"; then
        rm -f "$temp_script"
        colorized_echo red "Unexpected HTML response while downloading script"
        exit 1
    fi
    ui_spinner_run "Installing antimage command script" install -m 755 "$temp_script" "$ANTIMAGE_SCRIPT_INSTALL_PATH"
    rm -f "$temp_script"
    colorized_echo green "antimage script installed successfully"
}

trim_string() {
    local value="$1"
    value="${value#"${value%%[![:space:]]*}"}"
    value="${value%"${value##*[![:space:]]}"}"
    printf '%s' "$value"
}

validate_domain_format() {
    local domain="$1"
    if [[ ! "$domain" =~ ^[A-Za-z0-9.-]+\.[A-Za-z]{2,}$ ]]; then
        colorized_echo red "Invalid domain: $domain"
        return 1
    fi
    return 0
}

is_valid_ipv4() {
    local ip="$1"
    local IFS='.'
    read -r -a octets <<< "$ip"
    if [ ${#octets[@]} -ne 4 ]; then
        return 1
    fi
    for octet in "${octets[@]}"; do
        if [[ ! "$octet" =~ ^[0-9]+$ ]]; then
            return 1
        fi
        if [ "$octet" -lt 0 ] || [ "$octet" -gt 255 ]; then
            return 1
        fi
    done
    return 0
}

is_valid_ipv6() {
    local ip="$1"
    if [[ "$ip" =~ ^[0-9a-fA-F:]+$ ]] && [[ "$ip" == *:*:* ]]; then
        return 0
    fi
    return 1
}

is_valid_ip() {
    local value="$1"
    if is_valid_ipv4 "$value" || is_valid_ipv6 "$value"; then
        return 0
    fi
    return 1
}

ssl_cert_id_for_name() {
    local value="$1"
    value=$(echo "$value" | tr ':' '_' | tr '/' '_')
    printf '%s' "$value"
}

detect_public_ip() {
    local ip=""
    local urls=(
        "https://api.ipify.org"
        "https://ifconfig.me/ip"
        "https://checkip.amazonaws.com"
    )
    for url in "${urls[@]}"; do
        ip=$(curl -fsS4 --max-time 5 "$url" 2>/dev/null | tr -d '[:space:]' || true)
        if [ -n "$ip" ] && is_valid_ip "$ip"; then
            printf '%s' "$ip"
            return 0
        fi
    done
    return 1
}

install_ssl_dependencies() {
    detect_os
    local packages=("curl" "socat" "certbot" "openssl")
    for pkg in "${packages[@]}"; do
        if ! command -v "$pkg" >/dev/null 2>&1; then
            install_package "$pkg"
        fi
    done
}

ensure_acme_sh() {
    if [ ! -d "$HOME/.acme.sh" ]; then
        curl https://get.acme.sh | sh -s email="$1"
        if [ -f "$HOME/.bashrc" ]; then
            # shellcheck disable=SC1090
            source "$HOME/.bashrc"
        fi
    fi
}

certbot_supports_ip_certificates() {
    local certbot_bin="$1"
    "$certbot_bin" --help all 2>/dev/null | grep -q -- "--ip-address" \
        && "$certbot_bin" --help all 2>/dev/null | grep -q -- "--preferred-profile"
}

find_certbot_with_ip_support() {
    if command -v certbot >/dev/null 2>&1 && certbot_supports_ip_certificates "$(command -v certbot)"; then
        CERTBOT_BIN="$(command -v certbot)"
        return 0
    fi

    if [ -x "$CERTBOT_VENV_DIR/bin/certbot" ] && certbot_supports_ip_certificates "$CERTBOT_VENV_DIR/bin/certbot"; then
        CERTBOT_BIN="$CERTBOT_VENV_DIR/bin/certbot"
        return 0
    fi

    return 1
}

ensure_certbot_ip_support() {
    if find_certbot_with_ip_support; then
        return 0
    fi

    colorized_echo yellow "Installed certbot does not support IP certificates. Installing a modern certbot in $CERTBOT_VENV_DIR"
    detect_os
    if ! command -v python3 >/dev/null 2>&1; then
        install_package python3
    fi
    ensure_python3_venv
    python3 -m venv "$CERTBOT_VENV_DIR"
    "$CERTBOT_VENV_DIR/bin/python" -m pip install --upgrade pip >/dev/null
    "$CERTBOT_VENV_DIR/bin/python" -m pip install --upgrade "certbot>=5.4.0" >/dev/null

    if ! certbot_supports_ip_certificates "$CERTBOT_VENV_DIR/bin/certbot"; then
        colorized_echo red "The installed certbot still does not support --ip-address and --preferred-profile."
        return 1
    fi

    CERTBOT_BIN="$CERTBOT_VENV_DIR/bin/certbot"
    return 0
}

SSL_CERT_DIR=""

issue_ssl_with_acme() {
    local email="$1"
    shift
    local domains=("$@")
    ensure_acme_sh "$email"

    local args=""
    for domain in "${domains[@]}"; do
        args+=" -d $domain"
    done

    ~/.acme.sh/acme.sh --issue --standalone $args --accountemail "$email" || return 1

    local primary="${domains[0]}"
    SSL_CERT_DIR="$CERTS_BASE/$primary"
    mkdir -p "$SSL_CERT_DIR"

    ~/.acme.sh/acme.sh --install-cert -d "$primary" \
        --key-file "$SSL_CERT_DIR/privkey.pem" \
        --fullchain-file "$SSL_CERT_DIR/fullchain.pem" || return 1

    echo "provider=acme" > "$SSL_CERT_DIR/.metadata"
    echo "email=$email" >> "$SSL_CERT_DIR/.metadata"
    echo "domains=${domains[*]}" >> "$SSL_CERT_DIR/.metadata"
    echo "issued_at=$(date -u +%s)" >> "$SSL_CERT_DIR/.metadata"
    return 0
}

issue_ssl_with_certbot() {
    local email="$1"
    shift
    local domains=("$@")

    local args=""
    for domain in "${domains[@]}"; do
        args+=" -d $domain"
    done

    certbot certonly --standalone $args --non-interactive --agree-tos --email "$email" || return 1

    local primary="${domains[0]}"
    SSL_CERT_DIR="$CERTS_BASE/$primary"
    mkdir -p "$SSL_CERT_DIR"

    cat "/etc/letsencrypt/live/$primary/privkey.pem" > "$SSL_CERT_DIR/privkey.pem"
    cat "/etc/letsencrypt/live/$primary/fullchain.pem" > "$SSL_CERT_DIR/fullchain.pem"

    echo "provider=certbot" > "$SSL_CERT_DIR/.metadata"
    echo "email=$email" >> "$SSL_CERT_DIR/.metadata"
    echo "domains=${domains[*]}" >> "$SSL_CERT_DIR/.metadata"
    echo "issued_at=$(date -u +%s)" >> "$SSL_CERT_DIR/.metadata"
    return 0
}

issue_ssl_public_ip() {
    local email="$1"
    shift
    local ips=("$@")

    if [ ${#ips[@]} -eq 0 ]; then
        colorized_echo red "At least one IP address is required for Let's Encrypt IP SSL."
        return 1
    fi

    ensure_certbot_ip_support || return 1

    local primary="${ips[0]}"
    local cert_id
    cert_id=$(ssl_cert_id_for_name "$primary")
    SSL_CERT_DIR="$CERTS_BASE/$cert_id"
    mkdir -p "$SSL_CERT_DIR"

    local certbot_args=(
        certonly
        --standalone
        --non-interactive
        --agree-tos
        --email "$email"
        --preferred-profile shortlived
        --cert-name "$cert_id"
    )
    local ip
    for ip in "${ips[@]}"; do
        certbot_args+=(--ip-address "$ip")
    done

    local deploy_hook
    deploy_hook="mkdir -p '$SSL_CERT_DIR' && cp '/etc/letsencrypt/live/$cert_id/privkey.pem' '$SSL_CERT_DIR/privkey.pem' && cp '/etc/letsencrypt/live/$cert_id/fullchain.pem' '$SSL_CERT_DIR/fullchain.pem' && systemctl restart '$APP_NAME.service' >/dev/null 2>&1 || true"
    certbot_args+=(--deploy-hook "$deploy_hook")

    "$CERTBOT_BIN" "${certbot_args[@]}" || return 1

    cat "/etc/letsencrypt/live/$cert_id/privkey.pem" > "$SSL_CERT_DIR/privkey.pem"
    cat "/etc/letsencrypt/live/$cert_id/fullchain.pem" > "$SSL_CERT_DIR/fullchain.pem"

    echo "provider=letsencrypt-ip" > "$SSL_CERT_DIR/.metadata"
    echo "email=$email" >> "$SSL_CERT_DIR/.metadata"
    echo "domains=${ips[*]}" >> "$SSL_CERT_DIR/.metadata"
    echo "certbot_cert_name=$cert_id" >> "$SSL_CERT_DIR/.metadata"
    echo "validity=shortlived" >> "$SSL_CERT_DIR/.metadata"
    echo "issued_at=$(date -u +%s)" >> "$SSL_CERT_DIR/.metadata"
    return 0
}

issue_ssl_self_signed_ip() {
    local email="$1"
    shift
    local ips=("$@")

    if [ ${#ips[@]} -eq 0 ]; then
        colorized_echo red "At least one IP address is required for self-signed SSL."
        return 1
    fi

    detect_os
    if ! command -v openssl >/dev/null 2>&1; then
        install_package openssl
    fi

    local primary="${ips[0]}"
    local cert_id
    cert_id=$(ssl_cert_id_for_name "$primary")
    SSL_CERT_DIR="$CERTS_BASE/$cert_id"
    mkdir -p "$SSL_CERT_DIR"

    local openssl_conf
    openssl_conf=$(mktemp)
    {
        echo "[ req ]"
        echo "default_bits = 2048"
        echo "prompt = no"
        echo "default_md = sha256"
        echo "req_extensions = v3_req"
        echo "distinguished_name = dn"
        echo
        echo "[ dn ]"
        echo "CN = $primary"
        echo
        echo "[ v3_req ]"
        echo "subjectAltName = @alt_names"
        echo
        echo "[ alt_names ]"
        local idx=1
        for ip in "${ips[@]}"; do
            echo "IP.$idx = $ip"
            idx=$((idx + 1))
        done
    } > "$openssl_conf"

    if ! openssl req -x509 -nodes -days 825 -newkey rsa:2048 \
        -keyout "$SSL_CERT_DIR/privkey.pem" \
        -out "$SSL_CERT_DIR/fullchain.pem" \
        -config "$openssl_conf" >/dev/null 2>&1; then
        rm -f "$openssl_conf"
        colorized_echo red "Failed to generate self-signed certificate."
        return 1
    fi
    rm -f "$openssl_conf"

    echo "provider=self-signed" > "$SSL_CERT_DIR/.metadata"
    echo "email=$email" >> "$SSL_CERT_DIR/.metadata"
    echo "domains=${ips[*]}" >> "$SSL_CERT_DIR/.metadata"
    echo "issued_at=$(date -u +%s)" >> "$SSL_CERT_DIR/.metadata"
    return 0
}

set_env_value() {
    local key="$1"
    local value="$2"
    value=$(echo "$value" | sed 's/^"//;s/"$//')
    mkdir -p "$(dirname "$ENV_FILE")"
    touch "$ENV_FILE"
    if grep -qE "^[[:space:]]*#?[[:space:]]*${key}[[:space:]]*=" "$ENV_FILE" 2>/dev/null; then
        sed -i -E "s|^[[:space:]]*#?[[:space:]]*${key}[[:space:]]*=.*|${key} = \"${value}\"|" "$ENV_FILE"
    else
        echo "${key} = \"${value}\"" >> "$ENV_FILE"
    fi
}

get_env_value() {
    local key="$1"
    if [ ! -f "$ENV_FILE" ]; then
        return
    fi
    grep -E "^[[:space:]]*#?[[:space:]]*${key}[[:space:]]*=" "$ENV_FILE" 2>/dev/null \
        | tail -n 1 \
        | sed -E 's/^[^=]+=//; s/^[[:space:]]*//; s/[[:space:]]*$//; s/^"//; s/"$//'
}

escape_dotenv_double_quoted() {
    local value="$1"
    value="${value//\\/\\\\}"
    value="${value//\"/\\\"}"
    value="${value//\$/\\\$}"
    printf '%s' "$value"
}

upsert_env_assignment() {
    local key="$1"
    local value="$2"
    local escaped_value
    local tmp_env

    escaped_value=$(escape_dotenv_double_quoted "$value")
    mkdir -p "$(dirname "$ENV_FILE")"
    touch "$ENV_FILE"

    tmp_env=$(mktemp)
    grep -vE "^[[:space:]]*#?[[:space:]]*${key}[[:space:]]*=" "$ENV_FILE" > "$tmp_env" || true
    mv "$tmp_env" "$ENV_FILE"

    echo "${key}=\"${escaped_value}\"" >> "$ENV_FILE"
}

urlencode_value() {
    local value="$1"

    if command -v python3 >/dev/null 2>&1; then
        python3 -c "import sys, urllib.parse; print(urllib.parse.quote(sys.argv[1], safe=''))" "$value"
        return
    fi

    if command -v python >/dev/null 2>&1; then
        python -c "import sys, urllib.parse; print(urllib.parse.quote(sys.argv[1], safe=''))" "$value"
        return
    fi

    if command -v jq >/dev/null 2>&1; then
        printf '%s' "$value" | jq -sRr @uri
        return
    fi

    printf '%s' "$value"
}

normalize_url_path() {
    local value="${1:-}"
    local default_value="${2:-dashboard}"
    value=$(echo "$value" | xargs)
    value="${value#/}"
    value="${value%/}"
    if [ -z "$value" ]; then
        value="$default_value"
    fi
    if ! [[ "$value" =~ ^[A-Za-z0-9._~/-]+$ ]]; then
        return 1
    fi
    printf "/%s/" "$value"
}

validate_tcp_port() {
    local value="$1"
    [[ "$value" =~ ^[0-9]+$ ]] && [ "$value" -ge 1 ] && [ "$value" -le 65535 ]
}

is_tcp_port_listening() {
    local port="$1"
    if command -v ss >/dev/null 2>&1; then
        ss -tuln 2>/dev/null | awk '{print $5}' | grep -Eq "(:|\\])${port}$"
        return $?
    fi
    if command -v lsof >/dev/null 2>&1; then
        lsof -iTCP:"$port" -sTCP:LISTEN >/dev/null 2>&1
        return $?
    fi
    if command -v netstat >/dev/null 2>&1; then
        netstat -tuln 2>/dev/null | awk '{print $4}' | grep -Eq "(:|\\])${port}$"
        return $?
    fi
    return 1
}

prompt_tcp_port() {
    local label="$1"
    local default_value="$2"
    local value
    while true; do
        printf "%s [%s]: " "$label" "$default_value" >&2
        IFS= read -r value
        value="${value:-$default_value}"
        if validate_tcp_port "$value"; then
            if is_tcp_port_listening "$value"; then
                colorized_echo red "Port $value is already in use. Please choose another port." >&2
                continue
            fi
            printf "%s" "$value"
            return 0
        fi
        colorized_echo red "Port must be a number between 1 and 65535." >&2
    done
}

prompt_url_path() {
    local label="$1"
    local default_value="$2"
    local value normalized
    while true; do
        printf "%s [%s]: " "$label" "$default_value" >&2
        IFS= read -r value
        value="${value:-$default_value}"
        if normalized=$(normalize_url_path "$value" "${default_value#/}"); then
            printf "%s" "$normalized"
            return 0
        fi
        colorized_echo red "Path can contain only letters, numbers, dots, underscores, dashes, slashes, and tildes." >&2
    done
}

print_database_menu() {
    local selected="$1"
    local names=("MySQL" "SQLite" "MariaDB")
    local descriptions=("(recommended)" "" "")
    local idx
    ui_header "antimage Database" "Choose storage backend for binary install"
    for idx in 1 2 3; do
        printf "  "
        if [ "$idx" -eq "$selected" ]; then
            ui_color "38;5;16;48;5;45;1" " ▶ "
        else
            printf "   "
        fi
        ui_color "38;5;45;1" "$(printf '%2s' "$idx")"
        printf "  "
        if [ "$idx" -eq 1 ]; then
            ui_color "38;5;82;1" "$(printf '%-10s' "${names[$((idx - 1))]}")"
        else
            ui_color "38;5;231;1" "$(printf '%-10s' "${names[$((idx - 1))]}")"
        fi
        ui_color "38;5;245" "${descriptions[$((idx - 1))]}"
        printf "\n"
    done
    printf "\n"
    ui_color "38;5;245" "Use ↑/↓ and Enter, type 1-3, or press Enter for MySQL."
    printf "\n"
}

select_database_type_interactive() {
    local selected=1
    local action kind value
    if ! ui_is_tty; then
        echo "mysql"
        return
    fi
    while true; do
        ui_clear >&2
        print_database_menu "$selected" >&2
        printf "Select database: " >&2
        action=$(ui_read_menu_choice "$selected" 3) || {
            echo "mysql"
            return
        }
        kind="${action%%:*}"
        value="${action#*:}"
        case "$kind" in
            move)
                selected="$value"
            ;;
            enter)
                selected="$value"
                break
            ;;
            value)
                if [[ "$value" =~ ^[1-3]$ ]]; then
                    selected="$value"
                    break
                fi
            ;;
            quit)
                echo "mysql"
                return
            ;;
        esac
    done
    case "$selected" in
        2) echo "sqlite" ;;
        3) echo "mariadb" ;;
        *) echo "mysql" ;;
    esac
}

prompt_dashboard_bind_settings() {
    local port
    if [ ! -t 0 ]; then
        upsert_env_assignment "UVICORN_PORT" "8000"
        return
    fi
    ui_section "Dashboard"
    port=$(prompt_tcp_port "Dashboard port" "8000")
    echo
    upsert_env_assignment "UVICORN_PORT" "$port"
}

mysql_password_is_strong() {
    local password="$1"
    [ "${#password}" -ge 12 ] || return 1
    [[ "$password" =~ [A-Z] ]] || return 1
    [[ "$password" =~ [a-z] ]] || return 1
    [[ "$password" =~ [0-9] ]] || return 1
    [[ "$password" =~ [^A-Za-z0-9] ]] || return 1
    [[ "$password" != *" "* ]] || return 1
    return 0
}

generate_secure_mysql_password() {
    local candidate
    while true; do
        candidate="$(tr -dc 'A-Za-z0-9@#%_=+.-' </dev/urandom | head -c 28)"
        if mysql_password_is_strong "$candidate"; then
            printf "%s" "$candidate"
            return
        fi
    done
}

read_secret() {
    local prompt="$1"
    local value
    if [ -t 0 ]; then
        IFS= read -rsp "$prompt" value
        printf "\n" >&2
    else
        IFS= read -r value
    fi
    printf "%s" "$value"
}

prompt_confirmed_secret() {
    local label="$1"
    local first second
    while true; do
        first=$(read_secret "$label: ")
        [ -n "$first" ] || {
            colorized_echo red "Password cannot be empty." >&2
            continue
        }
        second=$(read_secret "Confirm $label: ")
        if [ "$first" = "$second" ]; then
            printf "%s" "$first"
            return
        fi
        colorized_echo red "Passwords do not match." >&2
    done
}

prompt_initial_admin() {
    INITIAL_ADMIN_CREATE=0
    INITIAL_ADMIN_USERNAME=""
    INITIAL_ADMIN_PASSWORD=""
    [ -t 0 ] || return
    ui_section "Initial admin"
    if ! ui_read_yes_no "Create a full-access admin now?" "y"; then
        return
    fi
    while true; do
        IFS= read -r -p "Admin username [admin]: " INITIAL_ADMIN_USERNAME
        INITIAL_ADMIN_USERNAME="${INITIAL_ADMIN_USERNAME:-admin}"
        if [[ "$INITIAL_ADMIN_USERNAME" =~ ^[A-Za-z0-9_.@-]{3,64}$ ]]; then
            break
        fi
        colorized_echo red "Username must be 3-64 chars and may contain letters, numbers, dot, underscore, dash, and @."
    done
    INITIAL_ADMIN_PASSWORD=$(prompt_confirmed_secret "Admin password")
    INITIAL_ADMIN_CREATE=1
}

create_initial_admin_if_requested() {
    if [ "${INITIAL_ADMIN_CREATE:-0}" != "1" ]; then
        return
    fi
    ui_spinner_run "Running database migrations" ANTIMAGE_cli migrate up
    ui_spinner_run "Creating full-access admin ${INITIAL_ADMIN_USERNAME}" ANTIMAGE_cli admin create "$INITIAL_ADMIN_USERNAME" --role full_access --password "$INITIAL_ADMIN_PASSWORD"
}

prompt_phpmyadmin_settings() {
    PHPMYADMIN_PATH=$(prompt_url_path "phpMyAdmin path" "phpmyadmin")
    echo
}

find_php_fpm_sock() {
    local sock
    sock=$(find /run/php -maxdepth 1 -type s -name 'php*-fpm.sock' 2>/dev/null | sort -V | tail -n 1)
    [ -n "$sock" ] && printf "%s" "$sock"
}

install_phpmyadmin_blueberry_theme() {
    local theme_dir="/usr/share/phpmyadmin/themes"
    local theme_url="https://files.phpmyadmin.net/themes/blueberry/1.1.0/blueberry-1.1.0.zip"
    local temp_zip

    if [ ! -d "$theme_dir" ]; then
        return 0
    fi
    if [ -d "$theme_dir/blueberry" ]; then
        return 0
    fi
    install_package unzip
    temp_zip=$(mktemp)
    if command -v curl >/dev/null 2>&1; then
        curl -fsSL "$theme_url" -o "$temp_zip" || {
            rm -f "$temp_zip"
            colorized_echo yellow "Could not download phpMyAdmin blueberry theme."
            return 0
        }
    elif command -v wget >/dev/null 2>&1; then
        wget -q "$theme_url" -O "$temp_zip" || {
            rm -f "$temp_zip"
            colorized_echo yellow "Could not download phpMyAdmin blueberry theme."
            return 0
        }
    else
        rm -f "$temp_zip"
        colorized_echo yellow "curl or wget is required to download phpMyAdmin blueberry theme."
        return 0
    fi
    unzip -qo "$temp_zip" -d "$theme_dir" >/dev/null 2>&1 || colorized_echo yellow "Could not extract phpMyAdmin blueberry theme."
    rm -f "$temp_zip"
}

configure_phpmyadmin_upload_limits() {
    local ini_content
    ini_content="upload_max_filesize=1024M
post_max_size=1024M
memory_limit=4096M
max_execution_time=0
max_input_time=0"
    local wrote=0
    local dir
    for dir in /etc/php/*/fpm/conf.d /etc/php/*/cli/conf.d; do
        [ -d "$dir" ] || continue
        printf "%s\n" "$ini_content" > "$dir/99-antimage-phpmyadmin-upload.ini" || true
        wrote=1
    done
    if [ "$wrote" = "1" ]; then
        systemctl reload php*-fpm >/dev/null 2>&1 || systemctl restart php*-fpm >/dev/null 2>&1 || true
    fi
}

prepare_external_app_hosting() {
    if ! is_binary_install; then
        colorized_echo red "PHP application hosting is available only in binary installations."
        return 1
    fi

    local package php_version
    if command -v php >/dev/null 2>&1 && command -v composer >/dev/null 2>&1 && command -v curl >/dev/null 2>&1; then
        php_version=$(php -r 'echo PHP_MAJOR_VERSION.".".PHP_MINOR_VERSION;')
        if php -r 'foreach (["curl","zip","mbstring","dom","gd","intl","bcmath","pdo_mysql"] as $ext) { if (!extension_loaded($ext)) exit(1); }' \
            && { systemctl is-active --quiet "php${php_version}-fpm" || systemctl is-active --quiet php-fpm; } \
            && { systemctl is-active --quiet cron || systemctl is-active --quiet crond; }; then
            colorized_echo green "PHP application hosting prerequisites are ready."
            return 0
        fi
    fi

    detect_os
    for package in php-cli php-fpm php-mysql php-curl php-zip php-mbstring php-xml php-gd php-intl php-bcmath composer unzip curl cron; do
        install_package "$package"
    done
    command -v php >/dev/null 2>&1 && command -v composer >/dev/null 2>&1 && command -v curl >/dev/null 2>&1 || {
        colorized_echo red "PHP hosting prerequisites are incomplete."
        return 1
    }
    php_version=$(php -r 'echo PHP_MAJOR_VERSION.".".PHP_MINOR_VERSION;')
    systemctl enable --now "php${php_version}-fpm" >/dev/null 2>&1 || systemctl enable --now php-fpm >/dev/null 2>&1 || {
        colorized_echo red "Could not start PHP-FPM."
        return 1
    }
    systemctl enable --now cron >/dev/null 2>&1 || systemctl enable --now crond >/dev/null 2>&1 || {
        colorized_echo red "Could not start the cron service."
        return 1
    }
    colorized_echo green "PHP application hosting prerequisites are ready (Apache was not installed)."
}

prepare_external_app_node_hosting() {
    if ! is_binary_install; then
        colorized_echo red "Node.js application hosting is available only in binary installations."
        return 1
    fi

    local install_root="/opt/antimage/node"
    local current="$install_root/current"
    if [ -x "$current/bin/node" ] && [ "$($current/bin/node -p 'Number(process.versions.node.split(".")[0])')" -ge 20 ]; then
        colorized_echo green "Node.js application hosting prerequisites are ready."
        return 0
    fi

    detect_os
    local package
    for package in curl jq tar xz-utils; do
        install_package "$package"
    done

    local node_arch
    case "$(uname -m)" in
        x86_64|amd64) node_arch="x64" ;;
        aarch64|arm64) node_arch="arm64" ;;
        *) colorized_echo red "Node.js hosting supports only x86_64 and arm64 hosts."; return 1 ;;
    esac

    local version archive tmp_dir target
    version=$(curl -fsSL https://nodejs.org/dist/index.json | jq -r '[.[] | select(.lts != false) | select((.version | ltrimstr("v") | split(".")[0] | tonumber) >= 20)][0].version // empty')
    if [ -z "$version" ]; then
        colorized_echo red "Could not resolve the current Node.js LTS release."
        return 1
    fi
    archive="node-${version}-linux-${node_arch}.tar.xz"
    target="$install_root/$version"
    tmp_dir=$(mktemp -d)
    if ! curl -fsSL "https://nodejs.org/dist/${version}/${archive}" -o "$tmp_dir/$archive" \
        || ! curl -fsSL "https://nodejs.org/dist/${version}/SHASUMS256.txt" -o "$tmp_dir/SHASUMS256.txt" \
        || ! (cd "$tmp_dir" && grep " ${archive}$" SHASUMS256.txt | sha256sum -c -) \
        || ! mkdir -p "$target" \
        || ! tar -xJf "$tmp_dir/$archive" --strip-components=1 -C "$target"; then
        rm -rf "$tmp_dir" "$target"
        colorized_echo red "Could not install the verified Node.js LTS runtime."
        return 1
    fi
    rm -rf "$tmp_dir"
    ln -sfn "$target" "$current"
    colorized_echo green "Node.js ${version} application hosting prerequisites are ready."
}

phpmyadmin_nginx_config_path() {
    printf "/etc/nginx/sites-available/%s-phpmyadmin" "$APP_NAME"
}

enable_host_phpmyadmin() {
    local database_type
    local path="${1:-}"
    local normalized_path
    local fpm_sock

    database_type=$(get_configured_database_type)
    if [ "$database_type" = "sqlite" ]; then
        colorized_echo red "phpMyAdmin is supported only with MySQL or MariaDB. Current database is SQLite."
        return 1
    fi

    detect_os
    for package in php-fpm php-mysql phpmyadmin; do
        install_package "$package"
    done
    install_phpmyadmin_blueberry_theme
    configure_phpmyadmin_upload_limits
    systemctl enable --now php*-fpm >/dev/null 2>&1 || true

    path="${path:-${PHPMYADMIN_PATH:-phpmyadmin}}"
    normalized_path=$(normalize_url_path "$path" "phpmyadmin") || {
        colorized_echo red "Invalid phpMyAdmin path."
        return 1
    }
    path="$normalized_path"
    path="${path%/}"
    fpm_sock=$(find_php_fpm_sock)
    if [ -z "$fpm_sock" ]; then
        colorized_echo red "Could not find php-fpm socket under /run/php."
        return 1
    fi

    rm -f "/etc/nginx/sites-enabled/${APP_NAME}-phpmyadmin" "$(phpmyadmin_nginx_config_path)"
    if command -v nginx >/dev/null 2>&1; then
        nginx -t >/dev/null 2>&1 && systemctl reload nginx >/dev/null 2>&1 || true
    fi
    colorized_echo green "phpMyAdmin is installed and will be served through antimage using local php-fpm."
}

disable_host_phpmyadmin() {
    rm -f "/etc/nginx/sites-enabled/${APP_NAME}-phpmyadmin" "$(phpmyadmin_nginx_config_path)"
    if command -v nginx >/dev/null 2>&1; then
        nginx -t >/dev/null 2>&1 && systemctl reload nginx >/dev/null 2>&1 || true
    fi
    colorized_echo green "phpMyAdmin has been disabled."
}

add_phpmyadmin_to_compose() {
    local compose_file="$1"

    # Check if phpMyAdmin service already exists
    if grep -q "^\s*phpmyadmin:" "$compose_file" 2>/dev/null; then
        return 0
    fi

    # Add phpMyAdmin service to docker-compose.yml
    if command -v yq >/dev/null 2>&1; then
        yq eval '.services.phpmyadmin = {
            "image": "phpmyadmin/phpmyadmin:latest",
            "restart": "always",
            "env_file": ".env",
            "network_mode": "host",
            "environment": {
                "PMA_HOST": "127.0.0.1",
                "APACHE_PORT": 8010,
                "UPLOAD_LIMIT": "1024M"
            },
            "depends_on": ["mysql"]
        }' -i "$compose_file"
    else
        cat >> "$compose_file" <<EOF

  phpmyadmin:
    image: phpmyadmin/phpmyadmin:latest
    restart: always
    env_file: .env
    network_mode: host
    environment:
      PMA_HOST: 127.0.0.1
      APACHE_PORT: 8010
      UPLOAD_LIMIT: 1024M
    depends_on:
      - mysql
EOF
    fi
}

enable_phpmyadmin() {
    check_running_as_root
    local cli_path=""

    while [ "$#" -gt 0 ]; do
        case "$1" in
            --port)
                shift 2
                ;;
            --path)
                cli_path="${2:-}"
                shift 2
                ;;
            --yes|-y)
                shift
                ;;
            *)
                shift
                ;;
        esac
    done

    if ! is_ANTIMAGE_installed; then
        colorized_echo red "antimage is not installed. Please install antimage first."
        exit 1
    fi

    if ! is_binary_install; then
        colorized_echo red "phpMyAdmin management from this TUI is available for binary installations only."
        exit 1
    fi

    if [ "$(get_configured_database_type)" = "sqlite" ]; then
        colorized_echo red "phpMyAdmin is not supported for SQLite installations."
        return 0
    fi

    if [ -n "$cli_path" ]; then
        PHPMYADMIN_PATH="$cli_path"
    else
        prompt_phpmyadmin_settings
    fi
    enable_host_phpmyadmin "$PHPMYADMIN_PATH"
}

disable_phpmyadmin() {
    check_running_as_root
    if ! is_binary_install; then
        colorized_echo red "phpMyAdmin management from this TUI is available for binary installations only."
        exit 1
    fi
    disable_host_phpmyadmin
}

sync_ssl_env_paths() {
    local cert_dir="$1"
    local ca_type="${2:-public}"
    set_env_value "UVICORN_SSL_CERTFILE" "$cert_dir/fullchain.pem"
    set_env_value "UVICORN_SSL_KEYFILE" "$cert_dir/privkey.pem"
    set_env_value "UVICORN_SSL_CA_TYPE" "$ca_type"
}

perform_ssl_issue() {
    local email="$1"
    local preferred="${2:-auto}"
    shift 2
    local domains=("$@")
    local provider_used=""
    local has_ip=0
    local has_domain=0

    if [ ${#domains[@]} -eq 0 ]; then
        colorized_echo red "At least one domain is required for SSL issuance."
        return 1
    fi

    for d in "${domains[@]}"; do
        if is_valid_ip "$d"; then
            has_ip=1
        else
            has_domain=1
        fi
    done

    if [ "$has_ip" -eq 1 ] && [ "$has_domain" -eq 1 ]; then
        colorized_echo red "Mixing IP addresses and domains is not supported in one certificate request."
        return 1
    fi

    install_ssl_dependencies
    mkdir -p "$CERTS_BASE"

    if [ "$has_ip" -eq 1 ]; then
        if [ "$has_domain" -eq 1 ]; then
            colorized_echo red "IP certificates cannot be mixed with domain names."
            return 1
        fi
        case "$preferred" in
            letsencrypt-ip|ip|public-ip|shortlived|certbot-ip)
                issue_ssl_public_ip "$email" "${domains[@]}" || return 1
                provider_used="letsencrypt-ip"
                sync_ssl_env_paths "$SSL_CERT_DIR" "public"
                colorized_echo green "Public short-lived IP SSL certificate installed at $SSL_CERT_DIR for IP(s): ${domains[*]}"
                ;;
            auto|self-signed)
                issue_ssl_self_signed_ip "$email" "${domains[@]}" || return 1
                provider_used="self-signed"
                sync_ssl_env_paths "$SSL_CERT_DIR" "self-signed"
                colorized_echo green "Self-signed SSL certificate generated at $SSL_CERT_DIR for IP(s): ${domains[*]}"
                ;;
            *)
                colorized_echo red "IP SSL requires --provider letsencrypt-ip or --provider self-signed."
                return 1
                ;;
        esac
        
        if is_ANTIMAGE_installed; then
            detect_compose
            if is_ANTIMAGE_up; then
                colorized_echo blue "Restarting antimage to apply SSL configuration..."
                down_antimage
                up_antimage
                colorized_echo green "antimage restarted with SSL configuration"
            fi
        fi
        
        return 0
    fi

    if [ "$preferred" = "self-signed" ] || [ "$preferred" = "letsencrypt-ip" ] || [ "$preferred" = "ip" ] || [ "$preferred" = "public-ip" ] || [ "$preferred" = "shortlived" ] || [ "$preferred" = "certbot-ip" ]; then
        colorized_echo red "Provider $preferred is only valid for IP address certificates."
        return 1
    fi

    if [ "$preferred" = "acme" ]; then
        issue_ssl_with_acme "$email" "${domains[@]}" || return 1
        provider_used="acme"
    elif [ "$preferred" = "certbot" ]; then
        issue_ssl_with_certbot "$email" "${domains[@]}" || return 1
        provider_used="certbot"
    else
        if issue_ssl_with_acme "$email" "${domains[@]}"; then
            provider_used="acme"
        else
            colorized_echo yellow "acme.sh issuance failed, falling back to certbot..."
            issue_ssl_with_certbot "$email" "${domains[@]}" || return 1
            provider_used="certbot"
        fi
    fi

    sync_ssl_env_paths "$SSL_CERT_DIR"
    colorized_echo green "SSL certificate installed at $SSL_CERT_DIR using $provider_used"
    
    # Check if antimage is installed and running, then restart to apply SSL changes
    if is_ANTIMAGE_installed; then
        detect_compose
        if is_ANTIMAGE_up; then
            colorized_echo blue "Restarting antimage to apply SSL configuration..."
            down_antimage
            up_antimage
            colorized_echo green "antimage restarted with SSL configuration"
        fi
    fi
    
    return 0
}

parse_domains_input() {
    local input="$1"
    PARSED_DOMAINS=()
    PARSED_IS_IP=0
    local has_ip=0
    local has_domain=0
    IFS=',' read -ra raw_domains <<< "$input"
    for entry in "${raw_domains[@]}"; do
        local domain
        domain=$(trim_string "$entry")
        if [ -z "$domain" ]; then
            continue
        fi
        if is_valid_ip "$domain"; then
            has_ip=1
        else
            validate_domain_format "$domain" || return 1
            has_domain=1
        fi
        PARSED_DOMAINS+=("$domain")
    done
    if [ ${#PARSED_DOMAINS[@]} -eq 0 ]; then
        colorized_echo red "No valid domains provided."
        return 1
    fi
    if [ "$has_ip" -eq 1 ] && [ "$has_domain" -eq 1 ]; then
        colorized_echo red "Cannot mix IP addresses and domains in one request."
        return 1
    fi
    if [ "$has_ip" -eq 1 ]; then
        PARSED_IS_IP=1
    fi
}

prompt_ssl_setup() {
    read -p "Do you want to configure SSL certificates now? (y/N): " ssl_answer
    if [[ ! "$ssl_answer" =~ ^[Yy]$ ]]; then
        return
    fi

    colorized_echo cyan "Select SSL certificate type:"
    echo "  1) Domain certificate (Let's Encrypt, regular public SSL)"
    echo "  2) Temporary public IP certificate (Let's Encrypt short-lived, about 6 days)"
    echo "  3) Self-signed IP certificate (browser warning, local fallback)"
    read -p "Select option [1]: " ssl_mode
    ssl_mode="${ssl_mode:-1}"

    read -p "Enter email for certificate notifications: " ssl_email

    local ssl_domains=""
    local ssl_provider="auto"
    case "$ssl_mode" in
        2)
            local detected_ip=""
            detected_ip=$(detect_public_ip || true)
            if [ -n "$detected_ip" ]; then
                read -p "Enter server public IP [$detected_ip]: " ssl_domains
                ssl_domains="${ssl_domains:-$detected_ip}"
            else
                read -p "Enter server public IP: " ssl_domains
            fi
            ssl_provider="letsencrypt-ip"
            ;;
        3)
            local detected_self_ip=""
            detected_self_ip=$(detect_public_ip || true)
            if [ -n "$detected_self_ip" ]; then
                read -p "Enter server IP [$detected_self_ip]: " ssl_domains
                ssl_domains="${ssl_domains:-$detected_self_ip}"
            else
                read -p "Enter server IP: " ssl_domains
            fi
            ssl_provider="self-signed"
            ;;
        *)
            read -p "Enter domain(s) separated by comma: " ssl_domains
            ssl_provider="auto"
            ;;
    esac

    if ! ssl_command issue --email "$ssl_email" --domains "$ssl_domains" --provider "$ssl_provider" --non-interactive; then
        colorized_echo yellow "SSL setup skipped due to input/issuance error. You can retry with: antimage ssl issue"
    fi
}

ssl_issue() {
    local email=""
    local domains_input=""
    local ip_input=""
    local provider="auto"
    local interactive=true

    while [[ $# -gt 0 ]]; do
        case "$1" in
            --email=*)
                email="${1#*=}"
                shift
                ;;
            --email)
                email="$2"
                shift 2
                ;;
            --domains=*)
                domains_input="${1#*=}"
                shift
                ;;
            --domains)
                domains_input="$2"
                shift 2
                ;;
            --ip-address=*|--ip=*)
                ip_input="${1#*=}"
                if [ "$provider" = "auto" ]; then
                    provider="letsencrypt-ip"
                fi
                shift
                ;;
            --ip-address|--ip)
                ip_input="$2"
                if [ "$provider" = "auto" ]; then
                    provider="letsencrypt-ip"
                fi
                shift 2
                ;;
            --provider=*)
                provider="${1#*=}"
                shift
                ;;
            --provider)
                provider="$2"
                shift 2
                ;;
            --non-interactive)
                interactive=false
                shift
                ;;
            *)
                colorized_echo red "Unknown option: $1"
                return 1
                ;;
        esac
    done

    if [ -n "$ip_input" ]; then
        domains_input="$ip_input"
    fi

    if [ "$interactive" = true ]; then
        if [ -z "$email" ]; then
            read -p "Enter email address: " email
        fi
        if [ -z "$domains_input" ]; then
            read -p "Enter domain(s) or IP address(es) separated by comma: " domains_input
        fi
    else
        if [ -z "$email" ] || [ -z "$domains_input" ]; then
            colorized_echo red "Email and domains/IP addresses are required when using non-interactive mode."
            return 1
        fi
    fi

    parse_domains_input "$domains_input" || return 1
    perform_ssl_issue "$email" "$provider" "${PARSED_DOMAINS[@]}"
}

get_domain_from_env() {
    if [ ! -f "$ENV_FILE" ]; then
        return
    fi
    local line
    line=$(grep "^UVICORN_SSL_CERTFILE" "$ENV_FILE" | tail -n 1 | cut -d'=' -f2-)
    line=$(echo "$line" | tr -d ' "')
    if [ -z "$line" ]; then
        return
    fi
    basename "$(dirname "$line")"
}

ssl_renew() {
    local target_domain=""

    while [[ $# -gt 0 ]]; do
        case "$1" in
            --domain=*)
                target_domain="${1#*=}"
                shift
                ;;
            --domain)
                target_domain="$2"
                shift 2
                ;;
            *)
                colorized_echo red "Unknown option: $1"
                return 1
                ;;
        esac
    done

    if [ -z "$target_domain" ]; then
        target_domain=$(get_domain_from_env)
    fi

    if [ -z "$target_domain" ]; then
        colorized_echo red "Unable to detect domain. Please specify --domain example.com"
        return 1
    fi

    local metadata="$CERTS_BASE/$target_domain/.metadata"
    if [ ! -f "$metadata" ]; then
        colorized_echo red "Metadata not found for domain $target_domain"
        return 1
    fi

    local provider email domains_line
    provider=$(grep '^provider=' "$metadata" | cut -d'=' -f2-)
    email=$(grep '^email=' "$metadata" | cut -d'=' -f2-)
    domains_line=$(grep '^domains=' "$metadata" | cut -d'=' -f2-)

    if [ -z "$email" ] || [ -z "$domains_line" ]; then
        colorized_echo red "Metadata is incomplete for $target_domain"
        return 1
    fi

    read -ra stored_domains <<< "$domains_line"
    perform_ssl_issue "$email" "$provider" "${stored_domains[@]}" || return 1
    colorized_echo green "SSL certificate renewed for $target_domain"
    
    # Note: perform_ssl_issue already restarts antimage if needed
}

ssl_command() {
    local action="$1"
    shift || true

    case "$action" in
        issue)
            ssl_issue "$@"
            ;;
        renew)
            ssl_renew "$@"
            ;;
        *)
            colorized_echo blue "Usage: antimage ssl <issue|renew> [options]"
            colorized_echo magenta "  Issue domain SSL: antimage ssl issue --email you@example.com --domains example.com"
            colorized_echo magenta "  Issue public IP SSL: antimage ssl issue --email you@example.com --ip-address 203.0.113.10"
            colorized_echo magenta "  Issue self-signed IP SSL: antimage ssl issue --email you@example.com --domains 203.0.113.10 --provider self-signed"
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
        colorized_echo red "This antimage installation is in ${installed_mode} mode, but ${0##*/} is the ${forced_mode} script."
        if [ "$installed_mode" = "binary" ]; then
            colorized_echo yellow "Use the antimage binary script."
        else
            colorized_echo yellow "Use the antimage Docker script."
        fi
        exit 1
    fi
}

is_ANTIMAGE_installed() {
    if [ -d $APP_DIR ]; then
        return 0
    else
        return 1
    fi
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

send_backup_to_telegram() {
    if [ -f "$ENV_FILE" ]; then
        while IFS='=' read -r key value; do
            if [[ -z "$key" || "$key" =~ ^# ]]; then
                continue
            fi
            key=$(echo "$key" | xargs)
            value=$(echo "$value" | xargs)
            if [[ "$key" =~ ^[a-zA-Z_][a-zA-Z0-9_]*$ ]]; then
                export "$key"="$value"
            else
                colorized_echo yellow "Skipping invalid line in .env: $key=$value"
            fi
        done < "$ENV_FILE"
    else
        colorized_echo red "Environment file (.env) not found."
        exit 1
    fi

    if [ "$BACKUP_SERVICE_ENABLED" != "true" ]; then
        colorized_echo yellow "Backup service is not enabled. Skipping Telegram upload."
        return
    fi

    local server_ip=$(curl -s ifconfig.me || echo "Unknown IP")
    local latest_backup=$(ls -t "$APP_DIR/backup" | head -n 1)
    local backup_path="$APP_DIR/backup/$latest_backup"

    if [ ! -f "$backup_path" ]; then
        colorized_echo red "No backups found to send."
        return
    fi

    local backup_size=$(du -m "$backup_path" | cut -f1)
    local split_dir="/tmp/ANTIMAGE_backup_split"
    local is_single_file=true

    mkdir -p "$split_dir"

    if [ "$backup_size" -gt 49 ]; then
        colorized_echo yellow "Backup is larger than 49MB. Splitting the archive..."
        split -b 49M "$backup_path" "$split_dir/part_"
        is_single_file=false
    else
        cp "$backup_path" "$split_dir/part_aa"
    fi


    local backup_time=$(date "+%Y-%m-%d %H:%M:%S %Z")


    for part in "$split_dir"/*; do
        local part_name=$(basename "$part")
        local custom_filename="backup_${part_name}.tar.gz"
        local caption="📦 *Backup Information*\n🌐 *Server IP*: \`${server_ip}\`\n📁 *Backup File*: \`${custom_filename}\`\n⏰ *Backup Time*: \`${backup_time}\`"
        curl -s -F chat_id="$BACKUP_TELEGRAM_CHAT_ID" \
            -F document=@"$part;filename=$custom_filename" \
            -F caption="$(echo -e "$caption" | sed 's/-/\\-/g;s/\./\\./g;s/_/\\_/g')" \
            -F parse_mode="MarkdownV2" \
            "https://api.telegram.org/bot$BACKUP_TELEGRAM_BOT_KEY/sendDocument" >/dev/null 2>&1 && \
        colorized_echo green "Backup part $custom_filename successfully sent to Telegram." || \
        colorized_echo red "Failed to send backup part $custom_filename to Telegram."
    done

    rm -rf "$split_dir"
}

send_backup_error_to_telegram() {
    local error_messages=$1
    local log_file=$2
    local server_ip=$(curl -s ifconfig.me || echo "Unknown IP")
    local error_time=$(date "+%Y-%m-%d %H:%M:%S %Z")
    local message="⚠️ *Backup Error Notification*\n"
    message+="🌐 *Server IP*: \`${server_ip}\`\n"
    message+="❌ *Errors*:\n\`${error_messages//_/\\_}\`\n"
    message+="⏰ *Time*: \`${error_time}\`"


    message=$(echo -e "$message" | sed 's/-/\\-/g;s/\./\\./g;s/_/\\_/g;s/(/\\(/g;s/)/\\)/g')

    local max_length=1000
    if [ ${#message} -gt $max_length ]; then
        message="${message:0:$((max_length - 50))}...\n\`[Message truncated]\`"
    fi


    curl -s -X POST "https://api.telegram.org/bot$BACKUP_TELEGRAM_BOT_KEY/sendMessage" \
        -d chat_id="$BACKUP_TELEGRAM_CHAT_ID" \
        -d parse_mode="MarkdownV2" \
        -d text="$message" >/dev/null 2>&1 && \
    colorized_echo green "Backup error notification sent to Telegram." || \
    colorized_echo red "Failed to send error notification to Telegram."


    if [ -f "$log_file" ]; then
        response=$(curl -s -w "%{http_code}" -o /tmp/tg_response.json \
            -F chat_id="$BACKUP_TELEGRAM_CHAT_ID" \
            -F document=@"$log_file;filename=backup_error.log" \
            -F caption="📜 *Backup Error Log* - ${error_time}" \
            "https://api.telegram.org/bot$BACKUP_TELEGRAM_BOT_KEY/sendDocument")

        http_code="${response:(-3)}"
        if [ "$http_code" -eq 200 ]; then
            colorized_echo green "Backup error log sent to Telegram."
        else
            colorized_echo red "Failed to send backup error log to Telegram. HTTP code: $http_code"
            cat /tmp/tg_response.json
        fi
    else
        colorized_echo red "Log file not found: $log_file"
    fi
}





backup_service() {
    local telegram_bot_key=""
    local telegram_chat_id=""
    local cron_schedule=""
    local interval_hours=""

    colorized_echo blue "====================================="
    colorized_echo blue "      Welcome to Backup Service      "
    colorized_echo blue "====================================="

    if grep -q "BACKUP_SERVICE_ENABLED=true" "$ENV_FILE"; then
        telegram_bot_key=$(awk -F'=' '/^BACKUP_TELEGRAM_BOT_KEY=/ {print $2}' "$ENV_FILE")
        telegram_chat_id=$(awk -F'=' '/^BACKUP_TELEGRAM_CHAT_ID=/ {print $2}' "$ENV_FILE")
        cron_schedule=$(awk -F'=' '/^BACKUP_CRON_SCHEDULE=/ {print $2}' "$ENV_FILE" | tr -d '"')

        if [[ "$cron_schedule" == "0 0 * * *" ]]; then
            interval_hours=24
        else
            interval_hours=$(echo "$cron_schedule" | grep -oP '(?<=\*/)[0-9]+')
        fi

        colorized_echo green "====================================="
        colorized_echo green "Current Backup Configuration:"
        colorized_echo cyan "Telegram Bot API Key: $telegram_bot_key"
        colorized_echo cyan "Telegram Chat ID: $telegram_chat_id"
        colorized_echo cyan "Backup Interval: Every $interval_hours hour(s)"
        colorized_echo green "====================================="
        echo "Choose an option:"
        echo "1. Reconfigure Backup Service"
        echo "2. Remove Backup Service"
        echo "3. Exit"
        read -p "Enter your choice (1-3): " user_choice

        case $user_choice in
            1)
                colorized_echo yellow "Starting reconfiguration..."
                remove_backup_service
                ;;
            2)
                colorized_echo yellow "Removing Backup Service..."
                remove_backup_service
                return
                ;;
            3)
                colorized_echo yellow "Exiting..."
                return
                ;;
            *)
                colorized_echo red "Invalid choice. Exiting."
                return
                ;;
        esac
    else
        colorized_echo yellow "No backup service is currently configured."
    fi

    while true; do
        printf "Enter your Telegram bot API key: "
        read telegram_bot_key
        if [[ -n "$telegram_bot_key" ]]; then
            break
        else
            colorized_echo red "API key cannot be empty. Please try again."
        fi
    done

    while true; do
        printf "Enter your Telegram chat ID: "
        read telegram_chat_id
        if [[ -n "$telegram_chat_id" ]]; then
            break
        else
            colorized_echo red "Chat ID cannot be empty. Please try again."
        fi
    done

    while true; do
        printf "Set up the backup interval in hours (1-24):\n"
        read interval_hours

        if ! [[ "$interval_hours" =~ ^[0-9]+$ ]]; then
            colorized_echo red "Invalid input. Please enter a valid number."
            continue
        fi

        if [[ "$interval_hours" -eq 24 ]]; then
            cron_schedule="0 0 * * *"
            colorized_echo green "Setting backup to run daily at midnight."
            break
        fi

        if [[ "$interval_hours" -ge 1 && "$interval_hours" -le 23 ]]; then
            cron_schedule="0 */$interval_hours * * *"
            colorized_echo green "Setting backup to run every $interval_hours hour(s)."
            break
        else
            colorized_echo red "Invalid input. Please enter a number between 1-24."
        fi
    done

    sed -i '/^BACKUP_SERVICE_ENABLED/d' "$ENV_FILE"
    sed -i '/^BACKUP_TELEGRAM_BOT_KEY/d' "$ENV_FILE"
    sed -i '/^BACKUP_TELEGRAM_CHAT_ID/d' "$ENV_FILE"
    sed -i '/^BACKUP_CRON_SCHEDULE/d' "$ENV_FILE"

    {
        echo ""
        echo "# Backup service configuration"
        echo "BACKUP_SERVICE_ENABLED=true"
        echo "BACKUP_TELEGRAM_BOT_KEY=$telegram_bot_key"
        echo "BACKUP_TELEGRAM_CHAT_ID=$telegram_chat_id"
        echo "BACKUP_CRON_SCHEDULE=\"$cron_schedule\""
    } >> "$ENV_FILE"

    colorized_echo green "Backup service configuration saved in $ENV_FILE."

    local backup_command
    backup_command="$(backup_cron_command)"
    add_cron_job "$cron_schedule" "$backup_command"

    colorized_echo green "Backup service successfully configured."
    if [[ "$interval_hours" -eq 24 ]]; then
        colorized_echo cyan "Backups will be sent to Telegram daily (every 24 hours at midnight)."
    else
        colorized_echo cyan "Backups will be sent to Telegram every $interval_hours hour(s)."
    fi
    colorized_echo green "====================================="
}


add_cron_job() {
    local schedule="$1"
    local command="$2"
    local temp_cron=$(mktemp)

    crontab -l 2>/dev/null > "$temp_cron" || true
    sed -i '/# antimage-backup-service/d' "$temp_cron"
    echo "$schedule $command # antimage-backup-service" >> "$temp_cron"
    
    if crontab "$temp_cron"; then
        colorized_echo green "Cron job successfully added."
    else
        colorized_echo red "Failed to add cron job. Please check manually."
    fi
    rm -f "$temp_cron"
}

remove_backup_service() {
    colorized_echo red "in process..."


    sed -i '/^# Backup service configuration/d' "$ENV_FILE"
    sed -i '/BACKUP_SERVICE_ENABLED/d' "$ENV_FILE"
    sed -i '/BACKUP_TELEGRAM_BOT_KEY/d' "$ENV_FILE"
    sed -i '/BACKUP_TELEGRAM_CHAT_ID/d' "$ENV_FILE"
    sed -i '/BACKUP_CRON_SCHEDULE/d' "$ENV_FILE"

    local temp_cron=$(mktemp)
    crontab -l 2>/dev/null > "$temp_cron"

    sed -i '/# antimage-backup-service/d' "$temp_cron"

    if crontab "$temp_cron"; then
        colorized_echo green "Backup service task removed from crontab."
    else
        colorized_echo red "Failed to update crontab. Please check manually."
    fi

    rm -f "$temp_cron"

    colorized_echo green "Backup service has been removed."
}

backup_cron_command() {
    local script_path="${ANTIMAGE_SCRIPT_INSTALL_PATH:-}"
    if [ -z "$script_path" ] || [ ! -x "$script_path" ]; then
        script_path="$(command -v "$APP_NAME" 2>/dev/null || true)"
    fi
    if [ -z "$script_path" ]; then
        script_path="/usr/local/bin/$APP_NAME"
    fi
    printf '%s backup' "$script_path"
}

backup_strip_quotes() {
    local value="$1"
    value="${value#\"}"
    value="${value%\"}"
    value="${value#\'}"
    value="${value%\'}"
    printf '%s' "$value"
}

backup_url_decode() {
    local value="$1"
    value="${value//%/\\x}"
    printf '%b' "$value"
}

backup_parse_database_url() {
    local raw
    raw="$(backup_strip_quotes "$1")"
    BACKUP_DB_TYPE=""
    BACKUP_SQLITE_FILE=""
    BACKUP_DB_USER=""
    BACKUP_DB_PASSWORD=""
    BACKUP_DB_HOST=""
    BACKUP_DB_PORT=""
    BACKUP_DB_NAME=""
    BACKUP_DB_SOCKET=""

    case "$raw" in
        sqlite:///*)
            BACKUP_DB_TYPE="sqlite"
            BACKUP_SQLITE_FILE="${raw#sqlite:///}"
            if [[ ! "$BACKUP_SQLITE_FILE" =~ ^/ ]]; then
                BACKUP_SQLITE_FILE="/$BACKUP_SQLITE_FILE"
            fi
            return 0
            ;;
        mysql*://*|mariadb*://*)
            BACKUP_DB_TYPE="mysql"
            local rest="${raw#*://}"
            local authority="${rest%%/*}"
            local path_query="${rest#*/}"
            local query=""
            BACKUP_DB_NAME="${path_query%%\?*}"
            if [[ "$path_query" == *"?"* ]]; then
                query="${path_query#*\?}"
            fi
            if [[ "$authority" == *"@"* ]]; then
                local credentials="${authority%@*}"
                local hostport="${authority##*@}"
                BACKUP_DB_USER="$(backup_url_decode "${credentials%%:*}")"
                if [[ "$credentials" == *":"* ]]; then
                    BACKUP_DB_PASSWORD="$(backup_url_decode "${credentials#*:}")"
                fi
                authority="$hostport"
            fi
            if [[ "$authority" == *":"* ]]; then
                BACKUP_DB_HOST="${authority%%:*}"
                BACKUP_DB_PORT="${authority##*:}"
            else
                BACKUP_DB_HOST="$authority"
                BACKUP_DB_PORT="3306"
            fi
            BACKUP_DB_HOST="${BACKUP_DB_HOST:-127.0.0.1}"
            BACKUP_DB_PORT="${BACKUP_DB_PORT:-3306}"
            BACKUP_DB_NAME="$(backup_url_decode "$BACKUP_DB_NAME")"
            if [ -n "$query" ]; then
                IFS='&' read -ra query_parts <<< "$query"
                for query_part in "${query_parts[@]}"; do
                    case "$query_part" in
                        unix_socket=*|socket=*)
                            BACKUP_DB_SOCKET="$(backup_url_decode "${query_part#*=}")"
                            ;;
                    esac
                done
            fi
            [ -n "$BACKUP_DB_NAME" ]
            return
            ;;
    esac
    return 1
}

write_mysql_backup_defaults() {
    local defaults_file="$1"
    {
        echo "[client]"
        [ -n "${BACKUP_DB_USER:-}" ] && printf 'user="%s"\n' "${BACKUP_DB_USER//\"/\\\"}"
        [ -n "${BACKUP_DB_PASSWORD:-}" ] && printf 'password="%s"\n' "${BACKUP_DB_PASSWORD//\"/\\\"}"
        if [ -n "${BACKUP_DB_SOCKET:-}" ]; then
            printf 'socket="%s"\n' "${BACKUP_DB_SOCKET//\"/\\\"}"
        else
            printf 'host="%s"\n' "${BACKUP_DB_HOST:-127.0.0.1}"
            printf 'port=%s\n' "${BACKUP_DB_PORT:-3306}"
            echo "protocol=tcp"
        fi
    } > "$defaults_file"
    chmod 600 "$defaults_file"
}

backup_command() {
    local backup_dir="$APP_DIR/backup"
    local temp_dir="/tmp/ANTIMAGE_backup"
    local timestamp=$(date +"%Y%m%d%H%M%S")
    local backup_file="$backup_dir/backup_$timestamp.tar.gz"
    local error_messages=()
    local log_file="/var/log/ANTIMAGE_backup_error.log"
    > "$log_file"
    echo "Backup Log - $(date)" > "$log_file"

    if ! command -v rsync >/dev/null 2>&1; then
        detect_os
        install_package rsync
    fi

    rm -rf "$backup_dir"
    mkdir -p "$backup_dir"
    rm -rf "$temp_dir"
    mkdir -p "$temp_dir"

    if [ -f "$ENV_FILE" ]; then
        while IFS='=' read -r key value; do
            if [[ -z "$key" || "$key" =~ ^# ]]; then
                continue
            fi
            key=$(echo "$key" | xargs)
            value=$(echo "$value" | xargs)
            if [[ "$key" =~ ^[a-zA-Z_][a-zA-Z0-9_]*$ ]]; then
                export "$key"="$value"
            else
                echo "Skipping invalid line in .env: $key=$value" >> "$log_file"
            fi
        done < "$ENV_FILE"
    else
        error_messages+=("Environment file (.env) not found.")
        echo "Environment file (.env) not found." >> "$log_file"
        send_backup_error_to_telegram "${error_messages[*]}" "$log_file"
        exit 1
    fi

    local db_type=""
    local sqlite_file=""
    local container_name=""
    if [ -n "${SQLALCHEMY_DATABASE_URL:-}" ] && backup_parse_database_url "$SQLALCHEMY_DATABASE_URL"; then
        db_type="$BACKUP_DB_TYPE"
        sqlite_file="$BACKUP_SQLITE_FILE"
    elif grep -q "image: mariadb" "$COMPOSE_FILE" 2>/dev/null; then
        db_type="mariadb"
        container_name=$(docker compose -f "$COMPOSE_FILE" ps -q mariadb 2>/dev/null || true)

    elif grep -q "image: mysql" "$COMPOSE_FILE" 2>/dev/null; then
        db_type="mysql"
        container_name=$(docker compose -f "$COMPOSE_FILE" ps -q mysql 2>/dev/null || true)

    elif grep -q "SQLALCHEMY_DATABASE_URL = .*sqlite" "$ENV_FILE"; then
        db_type="sqlite"
        sqlite_file=$(grep -Po '(?<=SQLALCHEMY_DATABASE_URL = "sqlite:////).*"' "$ENV_FILE" | tr -d '"')
        if [[ ! "$sqlite_file" =~ ^/ ]]; then
            sqlite_file="/$sqlite_file"
        fi

    fi

    if [ -n "$db_type" ]; then
        echo "Database detected: $db_type" >> "$log_file"
        case $db_type in
            mariadb)
                if [ -n "$container_name" ]; then
                    if ! docker exec "$container_name" mariadb-dump -u root -p"$MYSQL_ROOT_PASSWORD" --all-databases --ignore-database=mysql --ignore-database=performance_schema --ignore-database=information_schema --ignore-database=sys --events --triggers > "$temp_dir/db_backup.sql" 2>>"$log_file"; then
                        error_messages+=("MariaDB dump failed.")
                    fi
                else
                    local dump_bin
                    dump_bin="$(command -v mariadb-dump 2>/dev/null || command -v mysqldump 2>/dev/null || true)"
                    if [ -z "$dump_bin" ]; then
                        error_messages+=("mariadb-dump or mysqldump is not installed.")
                    else
                        local defaults_file="$temp_dir/mysql-client.cnf"
                        write_mysql_backup_defaults "$defaults_file"
                        if ! "$dump_bin" --defaults-extra-file="$defaults_file" --single-transaction --quick --routines --triggers --events --hex-blob --default-character-set=utf8mb4 "$BACKUP_DB_NAME" > "$temp_dir/db_backup.sql" 2>>"$log_file"; then
                            error_messages+=("MariaDB dump failed.")
                        fi
                    fi
                fi
                ;;
            mysql)
                if [ -n "$container_name" ]; then
                    if ! docker exec "$container_name" mysqldump -u root -p"$MYSQL_ROOT_PASSWORD" antimage --events --triggers  > "$temp_dir/db_backup.sql" 2>>"$log_file"; then
                        error_messages+=("MySQL dump failed.")
                    fi
                else
                    local dump_bin
                    dump_bin="$(command -v mysqldump 2>/dev/null || command -v mariadb-dump 2>/dev/null || true)"
                    if [ -z "$dump_bin" ]; then
                        error_messages+=("mysqldump or mariadb-dump is not installed.")
                    else
                        local defaults_file="$temp_dir/mysql-client.cnf"
                        write_mysql_backup_defaults "$defaults_file"
                        if ! "$dump_bin" --defaults-extra-file="$defaults_file" --single-transaction --quick --routines --triggers --events --hex-blob --default-character-set=utf8mb4 "$BACKUP_DB_NAME" > "$temp_dir/db_backup.sql" 2>>"$log_file"; then
                            error_messages+=("MySQL dump failed.")
                        fi
                    fi
                fi
                ;;
            sqlite)
                if [ -f "$sqlite_file" ]; then
                    if ! cp "$sqlite_file" "$temp_dir/db_backup.sqlite" 2>>"$log_file"; then
                        error_messages+=("Failed to copy SQLite database.")
                    fi
                else
                    error_messages+=("SQLite database file not found at $sqlite_file.")
                fi
                ;;
        esac
    fi

    cp "$APP_DIR/.env" "$temp_dir/" 2>>"$log_file" || true
    cp "$APP_DIR/docker-compose.yml" "$temp_dir/" 2>>"$log_file" || true
    if ! rsync -a --delete --exclude 'xray-core' --exclude 'mysql' --exclude 'logs' "$DATA_DIR/" "$temp_dir/ANTIMAGE_data/" >>"$log_file" 2>&1; then
        error_messages+=("Failed to copy antimage data files.")
    fi

    if ! tar -C "$temp_dir" -cf - . | gzip -1 > "$backup_file"; then
        error_messages+=("Failed to create backup archive.")
        echo "Failed to create backup archive." >> "$log_file"
    fi

    rm -rf "$temp_dir"

    if [ ${#error_messages[@]} -gt 0 ]; then
        send_backup_error_to_telegram "${error_messages[*]}" "$log_file"
        return
    fi
    colorized_echo green "Backup created: $backup_file"
    send_backup_to_telegram "$backup_file"
}



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

    # Check if the required packages are installed
    if ! command -v unzip >/dev/null 2>&1; then
        echo -e "\033[1;33mInstalling required packages...\033[0m"
        detect_os
        install_package unzip
    fi
    if ! command -v wget >/dev/null 2>&1; then
        echo -e "\033[1;33mInstalling required packages...\033[0m"
        detect_os
        install_package wget
    fi

    mkdir -p $DATA_DIR/xray-core
    cd $DATA_DIR/xray-core

    xray_filename="Xray-linux-$ARCH.zip"
    xray_download_url="https://github.com/XTLS/Xray-core/releases/download/${selected_version}/${xray_filename}"

    echo -e "\033[1;33mDownloading Xray-core version ${selected_version}...\033[0m"
    wget -q -O "${xray_filename}" "${xray_download_url}"

    echo -e "\033[1;33mExtracting Xray-core...\033[0m"
    unzip -o "${xray_filename}" >/dev/null 2>&1
    rm "${xray_filename}"
}

get_current_xray_core_version() {
    XRAY_BINARY="$DATA_DIR/xray-core/xray"
    if [ -f "$XRAY_BINARY" ]; then
        version_output=$("$XRAY_BINARY" -version 2>/dev/null)
        if [ $? -eq 0 ]; then
            version=$(echo "$version_output" | head -n1 | awk '{print $2}')
            echo "$version"
            return
        fi
    fi

    CONTAINER_NAME="$APP_NAME"
    if command -v docker >/dev/null 2>&1 && docker ps --format '{{.Names}}' | grep -q "^$CONTAINER_NAME$"; then
        version_output=$(docker exec "$CONTAINER_NAME" xray -version 2>/dev/null)
        if [ $? -eq 0 ]; then
            version=$(echo "$version_output" | head -n1 | awk '{print $2}')
            echo "$version (in container)"
            return
        fi
    fi

    echo "Not installed"
}

# Function kept for legacy CLI compatibility. Xray core is managed by nodes now.
update_core_command() {
    colorized_echo yellow "Master no longer runs a local Xray core. Update Xray from the Nodes page or the antimage-node installer."
}

install_antimage() {
    local ANTIMAGE_version=$1
    local database_type=$2
    set_ANTIMAGE_source_for_version "$ANTIMAGE_version"
    # Fetch releases
    FILES_URL_PREFIX="$ANTIMAGE_RAW_BASE"
    
    mkdir -p "$DATA_DIR"
    mkdir -p "$APP_DIR"
    
    colorized_echo blue "Setting up docker-compose.yml"
    docker_file_path="$APP_DIR/docker-compose.yml"
    
    if [ "$database_type" == "mariadb" ]; then
        # Ensure .env file exists before creating docker-compose.yml
        if [ ! -f "$ENV_FILE" ]; then
            colorized_echo blue "Fetching .env file"
            curl -sL "$FILES_URL_PREFIX/.env.example" -o "$APP_DIR/.env" || {
                mkdir -p "$(dirname "$ENV_FILE")"
                touch "$ENV_FILE"
            }
        fi
        
        # Ensure .env file exists before creating docker-compose.yml
        if [ ! -f "$ENV_FILE" ]; then
            mkdir -p "$(dirname "$ENV_FILE")"
            colorized_echo blue "Fetching .env file"
            curl -sL "$FILES_URL_PREFIX/.env.example" -o "$APP_DIR/.env" || touch "$APP_DIR/.env"
        fi
        
        # Generate docker-compose.yml with MariaDB content
        cat > "$docker_file_path" <<EOF
services:
  antimage:
    image: antimagepanel/antimage:${ANTIMAGE_version}
    restart: always
    env_file: .env
    network_mode: host
    volumes:
      - /var/lib/antimage:/var/lib/antimage
      - /var/lib/antimage/logs:/var/lib/antimage-node
    depends_on:
      mariadb:
        condition: service_started

  mariadb:
    image: mariadb:lts
    env_file: .env
    network_mode: host
    restart: always
    environment:
      MYSQL_ROOT_PASSWORD: \${MYSQL_ROOT_PASSWORD}
      MYSQL_ROOT_HOST: '%'
      MYSQL_DATABASE: \${MYSQL_DATABASE}
      MYSQL_USER: \${MYSQL_USER}
      MYSQL_PASSWORD: \${MYSQL_PASSWORD}
    command:
      - --bind-address=127.0.0.1                  # Restricts access to localhost for increased security
      - --character_set_server=utf8mb4            # Sets UTF-8 character set for full Unicode support
      - --collation_server=utf8mb4_unicode_ci     # Defines collation for Unicode
      - --host-cache-size=0                       # Disables host cache to prevent DNS issues
      - --innodb-open-files=1024                  # Sets the limit for InnoDB open files
      - --innodb-buffer-pool-size=256M            # Allocates buffer pool size for InnoDB
      - --binlog_expire_logs_seconds=1209600      # Sets binary log expiration to 14 days (2 weeks)
      - --innodb-log-file-size=64M                # Sets InnoDB log file size to balance log retention and performance
      - --innodb-log-files-in-group=2             # Uses two log files to balance recovery and disk I/O
      - --innodb-doublewrite=0                    # Disables doublewrite buffer (reduces disk I/O; may increase data loss risk)
      - --general_log=0                           # Disables general query log to reduce disk usage
      - --slow_query_log=1                        # Enables slow query log for identifying performance issues
      - --slow_query_log_file=/var/lib/mysql/slow.log # Logs slow queries for troubleshooting
      - --long_query_time=2                       # Defines slow query threshold as 2 seconds
    volumes:
      - /var/lib/antimage/mysql:/var/lib/mysql
    healthcheck:
      test: ["CMD-SHELL", "mariadb-admin ping -h 127.0.0.1 --protocol=tcp --silent || mysqladmin ping -h 127.0.0.1 --protocol=tcp --silent || exit 1"]
      start_period: 30s
      interval: 10s
      timeout: 5s
      retries: 30
EOF
        echo "----------------------------"
        colorized_echo red "Using MariaDB as database"
        echo "----------------------------"
        colorized_echo green "File generated at $APP_DIR/docker-compose.yml"

        # Modify .env file (if not already fetched)
        if [ ! -f "$ENV_FILE" ] || [ ! -s "$ENV_FILE" ]; then
            colorized_echo blue "Fetching .env file"
            curl -sL "$FILES_URL_PREFIX/.env.example" -o "$APP_DIR/.env"
        fi

        # Comment out the SQLite line
        sed -i 's~^\(SQLALCHEMY_DATABASE_URL = "sqlite:////var/lib/antimage/db.sqlite3"\)~#\1~' "$APP_DIR/.env"


        # Add the MySQL connection string
        #echo -e '\nSQLALCHEMY_DATABASE_URL = "mysql+pymysql://antimage:password@127.0.0.1:3306/antimage"' >> "$APP_DIR/.env"

        sed -i 's/^# \(XRAY_JSON = .*\)$/\1/' "$APP_DIR/.env"
        sed -i 's~\(XRAY_JSON = \).*~\1"/var/lib/antimage/xray_config.json"~' "$APP_DIR/.env"


        ensure_docker_mysql_credentials
        MYSQL_PASSWORD_URL_ENCODED=$(urlencode_value "$MYSQL_PASSWORD")
        
        echo "" >> "$ENV_FILE"
        echo "" >> "$ENV_FILE"
        echo "# Database configuration" >> "$ENV_FILE"
        upsert_env_assignment "MYSQL_ROOT_PASSWORD" "$MYSQL_ROOT_PASSWORD"
        upsert_env_assignment "MYSQL_DATABASE" "antimage"
        upsert_env_assignment "MYSQL_USER" "antimage"
        upsert_env_assignment "MYSQL_PASSWORD" "$MYSQL_PASSWORD"
        
        SQLALCHEMY_DATABASE_URL="mysql+pymysql://antimage:${MYSQL_PASSWORD_URL_ENCODED}@127.0.0.1:3306/antimage"
        
        echo "" >> "$ENV_FILE"
        echo "# SQLAlchemy Database URL" >> "$ENV_FILE"
        upsert_env_assignment "SQLALCHEMY_DATABASE_URL" "$SQLALCHEMY_DATABASE_URL"
        
        colorized_echo green "File saved in $APP_DIR/.env"

    elif [ "$database_type" == "mysql" ]; then
        # Ensure .env file exists before creating docker-compose.yml
        if [ ! -f "$ENV_FILE" ]; then
            mkdir -p "$(dirname "$ENV_FILE")"
            colorized_echo blue "Fetching .env file"
            curl -sL "$FILES_URL_PREFIX/.env.example" -o "$APP_DIR/.env" || touch "$APP_DIR/.env"
        fi
        
        # Generate docker-compose.yml with MySQL content
        cat > "$docker_file_path" <<EOF
services:
  antimage:
    image: antimagepanel/antimage:${ANTIMAGE_version}
    restart: always
    env_file: .env
    network_mode: host
    volumes:
      - /var/lib/antimage:/var/lib/antimage
      - /var/lib/antimage/logs:/var/lib/antimage-node
    depends_on:
      mysql:
        condition: service_started

  mysql:
    image: mysql:8.4
    env_file: .env
    network_mode: host
    restart: always
    environment:
      MYSQL_ROOT_PASSWORD: \${MYSQL_ROOT_PASSWORD}
      MYSQL_ROOT_HOST: '%'
      MYSQL_DATABASE: \${MYSQL_DATABASE}
      MYSQL_USER: \${MYSQL_USER}
      MYSQL_PASSWORD: \${MYSQL_PASSWORD}
    command:
      - --mysqlx=OFF                             # Disables MySQL X Plugin to save resources if X Protocol isn't used
      - --bind-address=127.0.0.1                  # Restricts access to localhost for increased security
      - --character_set_server=utf8mb4            # Sets UTF-8 character set for full Unicode support
      - --collation_server=utf8mb4_unicode_ci     # Defines collation for Unicode
      - --log-bin=mysql-bin                       # Enables binary logging for point-in-time recovery
      - --binlog_expire_logs_seconds=1209600      # Sets binary log expiration to 14 days
      - --host-cache-size=0                       # Disables host cache to prevent DNS issues
      - --innodb-open-files=1024                  # Sets the limit for InnoDB open files
      - --innodb-buffer-pool-size=256M            # Allocates buffer pool size for InnoDB
      - --innodb-redo-log-capacity=128M           # Sets redo log capacity to balance recovery and disk I/O
      - --general_log=0                           # Disables general query log for lower disk usage
      - --slow_query_log=1                        # Enables slow query log for performance analysis
      - --slow_query_log_file=/var/lib/mysql/slow.log # Logs slow queries for troubleshooting
      - --long_query_time=2                       # Defines slow query threshold as 2 seconds
    volumes:
      - /var/lib/antimage/mysql:/var/lib/mysql
    healthcheck:
      test: ["CMD-SHELL", "mysqladmin ping -h 127.0.0.1 --protocol=tcp --silent || exit 1"]
      start_period: 30s
      interval: 10s
      timeout: 5s
      retries: 30
      
EOF
        echo "----------------------------"
        colorized_echo red "Using MySQL as database"
        echo "----------------------------"
        colorized_echo green "File generated at $APP_DIR/docker-compose.yml"

        # Modify .env file (if not already fetched)
        if [ ! -f "$ENV_FILE" ] || [ ! -s "$ENV_FILE" ]; then
            colorized_echo blue "Fetching .env file"
            curl -sL "$FILES_URL_PREFIX/.env.example" -o "$APP_DIR/.env"
        fi

        # Comment out the SQLite line
        sed -i 's~^\(SQLALCHEMY_DATABASE_URL = "sqlite:////var/lib/antimage/db.sqlite3"\)~#\1~' "$APP_DIR/.env"


        # Add the MySQL connection string
        #echo -e '\nSQLALCHEMY_DATABASE_URL = "mysql+pymysql://antimage:password@127.0.0.1:3306/antimage"' >> "$APP_DIR/.env"

        sed -i 's/^# \(XRAY_JSON = .*\)$/\1/' "$APP_DIR/.env"
        sed -i 's~\(XRAY_JSON = \).*~\1"/var/lib/antimage/xray_config.json"~' "$APP_DIR/.env"


        ensure_docker_mysql_credentials
        MYSQL_PASSWORD_URL_ENCODED=$(urlencode_value "$MYSQL_PASSWORD")
        
        echo "" >> "$ENV_FILE"
        echo "" >> "$ENV_FILE"
        echo "# Database configuration" >> "$ENV_FILE"
        upsert_env_assignment "MYSQL_ROOT_PASSWORD" "$MYSQL_ROOT_PASSWORD"
        upsert_env_assignment "MYSQL_DATABASE" "antimage"
        upsert_env_assignment "MYSQL_USER" "antimage"
        upsert_env_assignment "MYSQL_PASSWORD" "$MYSQL_PASSWORD"
        
        SQLALCHEMY_DATABASE_URL="mysql+pymysql://antimage:${MYSQL_PASSWORD_URL_ENCODED}@127.0.0.1:3306/antimage"
        
        echo "" >> "$ENV_FILE"
        echo "# SQLAlchemy Database URL" >> "$ENV_FILE"
        upsert_env_assignment "SQLALCHEMY_DATABASE_URL" "$SQLALCHEMY_DATABASE_URL"
        
        colorized_echo green "File saved in $APP_DIR/.env"

    else
        echo "----------------------------"
        colorized_echo red "Using SQLite as database"
        echo "----------------------------"
        
        # Ensure .env file exists before fetching docker-compose.yml
        if [ ! -f "$ENV_FILE" ]; then
            mkdir -p "$(dirname "$ENV_FILE")"
            touch "$APP_DIR/.env"
        fi
        
        colorized_echo blue "Fetching compose file"
        curl -sL "$FILES_URL_PREFIX/docker-compose.yml" -o "$docker_file_path"

        # Install requested version
        if [ "$ANTIMAGE_version" == "latest" ]; then
            yq -i '.services.antimage.image = "antimagepanel/antimage:latest"' "$docker_file_path"
        else
            yq -i ".services.antimage.image = \"antimagepanel/antimage:${ANTIMAGE_version}\"" "$docker_file_path"
        fi
        echo "Installing $ANTIMAGE_version version"
        colorized_echo green "File saved in $APP_DIR/docker-compose.yml"


        colorized_echo blue "Fetching .env file"
        curl -sL "$FILES_URL_PREFIX/.env.example" -o "$APP_DIR/.env"

        sed -i 's/^# \(XRAY_JSON = .*\)$/\1/' "$APP_DIR/.env"
        sed -i 's/^# \(SQLALCHEMY_DATABASE_URL = .*\)$/\1/' "$APP_DIR/.env"
        sed -i 's~\(XRAY_JSON = \).*~\1"/var/lib/antimage/xray_config.json"~' "$APP_DIR/.env"
        sed -i 's~\(SQLALCHEMY_DATABASE_URL = \).*~\1"sqlite:////var/lib/antimage/db.sqlite3"~' "$APP_DIR/.env"







        
        colorized_echo green "File saved in $APP_DIR/.env"
    fi
    
    colorized_echo blue "Fetching xray config file"
    curl -sL "$FILES_URL_PREFIX/xray_config.json" -o "$DATA_DIR/xray_config.json"
    colorized_echo green "File saved in $DATA_DIR/xray_config.json"
    
    colorized_echo green "antimage's files downloaded successfully"
}

detect_binary_arch() {
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
        ppc64le)
            echo "ppc64le"
            ;;
        s390x)
            echo "s390x"
            ;;
        *)
            colorized_echo red "Binary install is not available for architecture: $(uname -m)" >&2
            colorized_echo yellow "Use Dockerized install for this server." >&2
            exit 1
            ;;
    esac
}

get_binary_release_asset_metadata() {
    local ANTIMAGE_version="$1"
    local binary_arch="$2"
    local release_api
    local release_payload
    local resolved_tag
    local server_asset_url
    local cli_asset_url
    local package_asset_url
    local package_asset_name
    local server_asset_name
    local cli_asset_name

    if [ "$ANTIMAGE_version" = "latest" ]; then
        release_api="https://api.github.com/repos/${ANTIMAGE_RELEASE_REPO}/releases/latest"
    else
        release_api="https://api.github.com/repos/${ANTIMAGE_RELEASE_REPO}/releases/tags/${ANTIMAGE_version}"
    fi

    release_payload=$(curl -fsSL "$release_api") || {
        colorized_echo red "Unable to read antimage release metadata: $release_api" >&2
        exit 1
    }

    resolved_tag=$(echo "$release_payload" | jq -r '.tag_name // empty')
    package_asset_name="antimage-linux-${binary_arch}.tar.gz"
    server_asset_name="antimage-server-${resolved_tag}-linux-${binary_arch}"
    cli_asset_name="antimage-cli-${resolved_tag}-linux-${binary_arch}"

    package_asset_url=$(echo "$release_payload" | jq -r --arg name "$package_asset_name" '
        .assets[]?
        | select(.name == $name)
        | .browser_download_url
    ' | head -n 1)

    if [ -n "$package_asset_url" ] && [ "$package_asset_url" != "null" ]; then
        printf 'archive|%s|%s|\n' "${resolved_tag:-$ANTIMAGE_version}" "$package_asset_url"
        return
    fi

    server_asset_url=$(echo "$release_payload" | jq -r --arg name "$server_asset_name" '
        .assets[]?
        | select(.name == $name)
        | .browser_download_url
    ' | head -n 1)

    cli_asset_url=$(echo "$release_payload" | jq -r --arg name "$cli_asset_name" '
        .assets[]?
        | select(.name == $name)
        | .browser_download_url
    ' | head -n 1)

    if [ -n "$server_asset_url" ] && [ "$server_asset_url" != "null" ] && [ -n "$cli_asset_url" ] && [ "$cli_asset_url" != "null" ]; then
        printf 'split|%s|%s|%s\n' "${resolved_tag:-$ANTIMAGE_version}" "$server_asset_url" "$cli_asset_url"
        return
    fi

    colorized_echo red "No Go binary release assets found for linux-${binary_arch}." >&2
    colorized_echo yellow "Publish antimage-linux-${binary_arch}.tar.gz or split antimage-server/antimage-cli assets for this release." >&2
    exit 1
}

get_binary_dev_manifest_url() {
    if [ -n "$ANTIMAGE_BINARY_DEV_MANIFEST_URL" ]; then
        printf '%s\n' "$ANTIMAGE_BINARY_DEV_MANIFEST_URL"
        return
    fi
    printf 'https://raw.githubusercontent.com/%s/%s/%s\n' \
        "$ANTIMAGE_RELEASE_REPO" \
        "$ANTIMAGE_BINARY_DEV_MANIFEST_BRANCH" \
        "$ANTIMAGE_BINARY_DEV_MANIFEST_PATH"
}

get_binary_dev_manifest_metadata() {
    local binary_arch="$1"
    local requested_version="${2:-dev}"
    local manifest_url
    local manifest_payload
    local selected

    manifest_url=$(get_binary_dev_manifest_url)
    manifest_payload=$(curl -fsSL "$manifest_url") || return 1

    selected=$(echo "$manifest_payload" | jq -r \
        --arg arch "linux-${binary_arch}" \
        --arg requested "$requested_version" \
        --arg repo "$ANTIMAGE_RELEASE_REPO" \
        --arg release_tag "$ANTIMAGE_BINARY_DEV_RELEASE_TAG" '
        def legacy_build:
            .latest? as $latest
            | if ($latest | type) == "object" then
                {
                    tag: ($latest.build_tag // $latest.tag // ""),
                    assets: (
                        reduce ($latest.assets[]? | strings) as $name
                          ({};
                            if ($name | startswith("antimage-" + $arch + "-")) then
                              .[$arch] = {
                                name: $name,
                                url: ("https://github.com/" + $repo + "/releases/download/" + $release_tag + "/" + $name)
                              }
                            else
                              .
                            end)
                    )
                }
              else
                empty
              end;
        def builds:
            ([.builds[]? | select(type == "object")] + [legacy_build]);
        . as $root
        | builds as $builds
        | (if ($requested != "" and $requested != "dev") then
              ($builds[]? | select(.tag == $requested))
           else
              (if ($root.latest | type) == "string" then $root.latest else "" end) as $latest_tag
              | (($builds[]? | select(.tag == $latest_tag)) // $builds[0]?)
           end) as $build
        | ($build.assets[$arch] // empty) as $asset
        | ($asset.name // "") as $asset_name
        | ($asset.url // "") as $asset_url
        | select(($build.tag // "") != "" and $asset_name != "" and $asset_url != "")
        | [$build.tag, $asset_url, $asset_name] | @tsv
    ' | head -n 1)

    if [ -z "$selected" ]; then
        return 1
    fi

    printf '%s\n' "$selected" | awk -F '\t' '{ printf "%s|%s|%s\n", $1, $2, $3 }'
}

get_binary_dev_artifact_metadata() {
    local binary_arch="$1"
    local requested_version="${2:-dev}"
    local workflow_runs_api
    local workflow_runs_payload
    local latest_run_json
    local run_id
    local head_sha
    local artifact_name
    local artifacts_api
    local artifacts_payload
    local artifact_url
    local nightly_workflow

    if get_binary_dev_manifest_metadata "$binary_arch" "$requested_version"; then
        return
    fi

    if [ "$requested_version" != "dev" ]; then
        colorized_echo red "Dev binary build ${requested_version} was not found in $(get_binary_dev_manifest_url)." >&2
        exit 1
    fi

    nightly_workflow="$ANTIMAGE_BINARY_WORKFLOW_NAME"
    case "$nightly_workflow" in
        *.yml|*.yaml) ;;
        *) nightly_workflow="${nightly_workflow}.yml" ;;
    esac
    workflow_runs_api="https://api.github.com/repos/${ANTIMAGE_RELEASE_REPO}/actions/workflows/${nightly_workflow}/runs"
    workflow_runs_payload=$(curl -fsSLG "$workflow_runs_api" \
        --data-urlencode "branch=${ANTIMAGE_BINARY_DEV_BRANCH}" \
        --data-urlencode "event=push" \
        --data-urlencode "status=success" \
        --data-urlencode "per_page=100") || {
        colorized_echo red "Unable to read binary dev workflow metadata: $workflow_runs_api" >&2
        exit 1
    }

    latest_run_json=$(echo "$workflow_runs_payload" | jq -c --arg branch "$ANTIMAGE_BINARY_DEV_BRANCH" '
        .workflow_runs[]?
        | select(.head_branch == $branch and .event == "push" and .conclusion == "success")
    ' | head -n 1)

    if [ -z "$latest_run_json" ]; then
        colorized_echo red "No successful binary dev workflow run was found on branch ${ANTIMAGE_BINARY_DEV_BRANCH}." >&2
        exit 1
    fi

    run_id=$(echo "$latest_run_json" | jq -r '.id // empty')
    head_sha=$(echo "$latest_run_json" | jq -r '.head_sha // empty')
    artifacts_api="https://api.github.com/repos/${ANTIMAGE_RELEASE_REPO}/actions/runs/${run_id}/artifacts"
    artifacts_payload=$(curl -fsSL "$artifacts_api") || {
        colorized_echo red "Unable to read binary dev workflow artifacts: $artifacts_api" >&2
        exit 1
    }

    artifact_name=$(echo "$artifacts_payload" | jq -r --arg preferred "${BINARY_ARTIFACT_PREFIX}-linux-${binary_arch}" --arg arch "linux-${binary_arch}" '
        [
            .artifacts[]?
            | select((.expired | not) and (.name == $preferred or (.name | startswith("antimage")) and (.name | contains($arch))))
        ]
        | sort_by(if .name == $preferred then 0 else 1 end, .created_at)
        | .[0].name // empty
    ')

    if [ -z "$artifact_name" ]; then
        colorized_echo red "No usable binary dev artifact was found for workflow run ${run_id}." >&2
        exit 1
    fi

    artifact_url="https://nightly.link/${ANTIMAGE_RELEASE_REPO}/workflows/${nightly_workflow}/${ANTIMAGE_BINARY_DEV_BRANCH}/${artifact_name}.zip"
    printf '%s|%s|%s.zip\n' "dev-${head_sha:0:7}" "$artifact_url" "$artifact_name"
}

install_binary_cli_launcher() {
    cat > "$BINARY_CLI_LAUNCHER" <<EOF
#!/usr/bin/env bash
set -e
export ANTIMAGE_ENV_FILE="$ENV_FILE"
export ANTIMAGE_APP_DIR="$APP_DIR"
export ANTIMAGE_DATA_DIR="$DATA_DIR"
exec "$BINARY_CLI" "\$@"
EOF

    chmod 755 "$BINARY_CLI_LAUNCHER"
}

write_binary_release_metadata() {
    local resolved_version="$1"
    local binary_arch="$2"
    local asset_url="$3"

    jq -n \
        --arg image "antimage-server (binary)" \
        --arg tag "$resolved_version" \
        --arg asset_url "$asset_url" \
        --arg arch "linux-${binary_arch}" \
        --arg server_binary "$BINARY_SERVER" \
        --arg cli_binary "$BINARY_CLI" \
        --arg installed_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
        '{
            install_mode: "binary",
            image: $image,
            tag: $tag,
            asset_url: $asset_url,
            arch: $arch,
            server_binary: $server_binary,
            cli_binary: $cli_binary,
            installed_at: $installed_at
        }' > "$BINARY_METADATA_FILE"
}

create_binary_service() {
    cat > "$BINARY_SERVICE_UNIT" <<EOF
[Unit]
Description=antimage Panel
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
WorkingDirectory=$APP_DIR
Environment=ANTIMAGE_APP_DIR=$APP_DIR
Environment=ANTIMAGE_ENV_FILE=$ENV_FILE
Environment=ANTIMAGE_INSTALL_MODE=binary
Environment=ANTIMAGE_BINARY_METADATA_FILE=$BINARY_METADATA_FILE
Environment=ANTIMAGE_DATA_DIR=$DATA_DIR
ExecStart=$BINARY_SERVER
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

    systemctl daemon-reload
}

install_binary_antimage() {
    local ANTIMAGE_version="$1"
    local database_type="$2"
    local configure_database="${3:-1}"
    local binary_arch
    local binary_source_type
    local resolved_version
    local server_asset_url
    local cli_asset_url
    local artifact_url
    local artifact_name
    local tmp_dir
    local package_path=""
    local dev_package_path=""

    set_ANTIMAGE_source_for_version "$ANTIMAGE_version"

    detect_os
    for package in curl jq tar gzip unzip certbot; do
        if ! command -v "$package" >/dev/null 2>&1; then
            install_package "$package"
        fi
    done

    binary_arch=$(detect_binary_arch)
    tmp_dir=$(mktemp -d)

    if [ -n "${ANTIMAGE_BINARY_SERVER_OVERRIDE:-}" ] || [ -n "${ANTIMAGE_BINARY_CLI_OVERRIDE:-}" ]; then
        if [ ! -f "${ANTIMAGE_BINARY_SERVER_OVERRIDE:-}" ] || [ ! -f "${ANTIMAGE_BINARY_CLI_OVERRIDE:-}" ]; then
            colorized_echo red "Both ANTIMAGE_BINARY_SERVER_OVERRIDE and ANTIMAGE_BINARY_CLI_OVERRIDE must point to existing files." >&2
            rm -rf "$tmp_dir"
            exit 1
        fi
        ui_spinner_run "Installing antimage custom server binary" install -m 755 "$ANTIMAGE_BINARY_SERVER_OVERRIDE" "$tmp_dir/antimage-server"
        ui_spinner_run "Installing antimage custom CLI binary" install -m 755 "$ANTIMAGE_BINARY_CLI_OVERRIDE" "$tmp_dir/antimage-cli"
        resolved_version="${ANTIMAGE_BINARY_OVERRIDE_VERSION:-custom}"
        artifact_url="local-override"
    elif [[ "$ANTIMAGE_version" = "dev" || "$ANTIMAGE_version" == dev-* ]]; then
        IFS='|' read -r resolved_version artifact_url artifact_name < <(get_binary_dev_artifact_metadata "$binary_arch" "$ANTIMAGE_version")
        artifact_name="${artifact_name:-antimage-binaries.zip}"
        package_path="$tmp_dir/$artifact_name"
        ui_spinner_run "Downloading antimage dev binary artifact" curl -fL "$artifact_url" -o "$package_path"
        if [[ "$package_path" == *.zip ]]; then
            ui_spinner_run "Extracting antimage dev artifact" unzip -j -o "$package_path" -d "$tmp_dir"
            dev_package_path="$tmp_dir/antimage-linux-${binary_arch}.tar.gz"
            if [ -f "$dev_package_path" ]; then
                ui_spinner_run "Unpacking antimage binary package" tar -xzf "$dev_package_path" -C "$tmp_dir"
            elif ls "$tmp_dir"/antimage-*.tar.gz >/dev/null 2>&1; then
                ui_spinner_run "Unpacking antimage binary package" tar -xzf "$(ls "$tmp_dir"/antimage-*.tar.gz | head -n 1)" -C "$tmp_dir"
            fi
        elif [[ "$package_path" == *.tar.gz ]]; then
            ui_spinner_run "Unpacking antimage binary package" tar -xzf "$package_path" -C "$tmp_dir"
        else
            colorized_echo red "Unsupported dev binary asset format: $artifact_name" >&2
            rm -rf "$tmp_dir"
            exit 1
        fi
    else
        IFS='|' read -r binary_source_type resolved_version server_asset_url cli_asset_url < <(get_binary_release_asset_metadata "$ANTIMAGE_version" "$binary_arch")
        if [ "$binary_source_type" = "split" ]; then
            ui_spinner_run "Downloading antimage server binary" curl -fL "$server_asset_url" -o "$tmp_dir/antimage-server"
            ui_spinner_run "Downloading antimage CLI binary" curl -fL "$cli_asset_url" -o "$tmp_dir/antimage-cli"
        else
            package_path="$tmp_dir/antimage-binary.tar.gz"
            ui_spinner_run "Downloading antimage binary package" curl -fL "$server_asset_url" -o "$package_path"
            ui_spinner_run "Unpacking antimage binary package" tar -xzf "$package_path" -C "$tmp_dir"
        fi
    fi

    if [ ! -f "$tmp_dir/antimage-server" ] || [ ! -f "$tmp_dir/antimage-cli" ]; then
        colorized_echo red "Downloaded binary package is incomplete; antimage-server or antimage-cli is missing." >&2
        rm -rf "$tmp_dir"
        exit 1
    fi

    mkdir -p "$BINARY_BIN_DIR" "$DATA_DIR" "$APP_DIR/scripts"
    install -m 755 "$tmp_dir/antimage-server" "$BINARY_SERVER"
    install -m 755 "$tmp_dir/antimage-cli" "$BINARY_CLI"
    install_binary_cli_launcher

    if [ ! -f "$ENV_FILE" ]; then
        ui_spinner_run "Fetching default .env file" curl -fsSL "$ANTIMAGE_RAW_BASE/.env.example" -o "$ENV_FILE"
    fi

    upsert_env_assignment "ANTIMAGE_DATA_DIR" "$DATA_DIR"
    upsert_env_assignment "XRAY_JSON" "$DATA_DIR/xray_config.json"
    if [ "$configure_database" = "1" ]; then
        configure_binary_database "$database_type"
    fi

    if [ ! -f "$DATA_DIR/xray_config.json" ]; then
        curl -fsSL --retry 3 --retry-delay 2 --retry-all-errors "$ANTIMAGE_RAW_BASE/xray_config.json" -o "$DATA_DIR/xray_config.json" 2>/dev/null || {
            rm -f "$DATA_DIR/xray_config.json"
            colorized_echo yellow "No bundled xray_config.json found; antimage will use its built-in default."
        }
    fi

    write_binary_release_metadata "${resolved_version:-$ANTIMAGE_version}" "$binary_arch" "${artifact_url:-${server_asset_url:-}}"
    echo "binary" > "$INSTALL_MODE_FILE"
    create_binary_service
    rm -rf "$tmp_dir"
    colorized_echo green "antimage binary files installed successfully"
}

up_antimage() {
    if is_binary_install; then
        systemctl enable --now "$APP_NAME.service"
        return
    fi

    repair_docker_compose_startup_gates
    $COMPOSE -f $COMPOSE_FILE -p "$APP_NAME" up -d --remove-orphans
}

schedule_binary_service_restart() {
    local delay_seconds="${1:-1}"
    local unit_name="${APP_NAME}-delayed-restart-$(date +%s%N)"
    local restart_script="sleep ${delay_seconds}; systemctl restart ${APP_NAME}.service"

    if command -v systemd-run >/dev/null 2>&1; then
        systemd-run \
            --unit "$unit_name" \
            --collect \
            --description "antimage delayed service restart" \
            -- /bin/sh -c "$restart_script" >/dev/null
        return
    fi

    nohup /bin/sh -c "$restart_script" >/dev/null 2>&1 &
}

restart_binary_service_now() {
    systemctl restart "$APP_NAME.service"
}

repair_docker_compose_startup_gates() {
    if [ ! -f "$COMPOSE_FILE" ]; then
        return
    fi

    if grep -q "condition:[[:space:]]*service_healthy" "$COMPOSE_FILE" 2>/dev/null; then
        sed -i -E 's/condition:[[:space:]]*service_healthy/condition: service_started/g' "$COMPOSE_FILE"
    fi
}

follow_ANTIMAGE_logs() {
    if is_binary_install; then
        journalctl -u "$APP_NAME.service" -f -o "$(journal_output_format)" --no-pager | format_ANTIMAGE_journal_logs
        return
    fi

    $COMPOSE -f $COMPOSE_FILE -p "$APP_NAME" logs -f
}

status_command() {
    
    # Check if antimage is installed
    if ! is_ANTIMAGE_installed; then
        echo -n "Status: "
        colorized_echo red "Not Installed"
        exit 1
    fi
    
    if ! is_binary_install; then
        detect_compose
    fi
    
    if ! is_ANTIMAGE_up; then
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


prompt_for_ANTIMAGE_password() {
    if [ -n "${MYSQL_PASSWORD:-}" ]; then
        if ! mysql_password_is_strong "$MYSQL_PASSWORD"; then
            colorized_echo red "MYSQL_PASSWORD is not strong enough. Use at least 12 chars with uppercase, lowercase, digit, and symbol."
            exit 1
        fi
        return
    fi
    MYSQL_PASSWORD=$(get_env_value "MYSQL_PASSWORD")
    if [ -n "${MYSQL_PASSWORD:-}" ]; then
        if ! mysql_password_is_strong "$MYSQL_PASSWORD"; then
            colorized_echo red "MYSQL_PASSWORD in .env is not strong enough. Use at least 12 chars with uppercase, lowercase, digit, and symbol."
            exit 1
        fi
        return
    fi
    if [ ! -t 0 ]; then
        MYSQL_PASSWORD=$(generate_secure_mysql_password)
        colorized_echo green "A secure database password has been generated automatically."
        return
    fi
    colorized_echo cyan "This password will be used to access the database and should be strong."
    colorized_echo cyan "Leave it empty to generate a secure password automatically."
    while true; do
        MYSQL_PASSWORD=$(read_secret "Database password: ")
        if [ -z "$MYSQL_PASSWORD" ]; then
            MYSQL_PASSWORD=$(generate_secure_mysql_password)
            colorized_echo green "A secure password has been generated automatically."
            break
        fi
        if mysql_password_is_strong "$MYSQL_PASSWORD"; then
            local confirm_password
            confirm_password=$(read_secret "Confirm database password: ")
            if [ "$MYSQL_PASSWORD" = "$confirm_password" ]; then
                break
            fi
            colorized_echo red "Passwords do not match."
        else
            colorized_echo red "Password must be at least 12 chars and include uppercase, lowercase, digit, and symbol. Press Enter for auto-generation."
        fi
    done
    colorized_echo green "This password will be recorded in the .env file for future use."
}

ensure_docker_mysql_credentials() {
    local existing_root_password

    prompt_for_ANTIMAGE_password

    existing_root_password=$(get_env_value "MYSQL_ROOT_PASSWORD")
    if [ -n "${MYSQL_ROOT_PASSWORD:-}" ]; then
        return
    fi
    if [ -n "$existing_root_password" ]; then
        MYSQL_ROOT_PASSWORD="$existing_root_password"
    else
        MYSQL_ROOT_PASSWORD=$(generate_secure_mysql_password)
    fi
}

sql_escape_literal() {
    printf "%s" "$1" | sed "s/'/''/g"
}

get_configured_database_type() {
    local flavor
    local db_url
    flavor=$(get_env_value "ANTIMAGE_DATABASE_FLAVOR")
    case "$flavor" in
        mysql|mariadb|sqlite)
            echo "$flavor"
            return
        ;;
    esac

    db_url=$(get_env_value "SQLALCHEMY_DATABASE_URL")
    if [[ "$db_url" == sqlite* ]]; then
        echo "sqlite"
    elif [[ "$db_url" == mysql* ]]; then
        if [ -d "/var/lib/mysql" ] && command -v mariadb >/dev/null 2>&1 && ! command -v mysqld >/dev/null 2>&1; then
            echo "mariadb"
        else
            echo "mysql"
        fi
    else
        echo "sqlite"
    fi
}

mysql_root_command() {
    if command -v mysql >/dev/null 2>&1; then
        mysql --protocol=socket -uroot "$@"
    elif command -v mariadb >/dev/null 2>&1; then
        mariadb --protocol=socket -uroot "$@"
    else
        return 1
    fi
}

managed_database_url_is_local() {
    local db_url authority host_port
    db_url=$(get_env_value "SQLALCHEMY_DATABASE_URL")
    [[ "$db_url" == mysql*://* ]] || return 1
    authority="${db_url#*://}"
    authority="${authority%%/*}"
    host_port="${authority##*@}"
    case "$host_port" in
        127.0.0.1|127.0.0.1:*|localhost|localhost:*|\[::1\]|\[::1\]:*) return 0 ;;
        *) return 1 ;;
    esac
}

managed_database_has_replication() {
    local status gtid
    if grep -RhsEi '^[[:space:]]*(log[-_]bin|server[-_]id|gtid[-_]mode|relay[-_]log|replicate[-_]|binlog[-_](do|ignore)[-_]db)[[:space:]]*=' "$ANTIMAGE_MYSQL_CONFIG_ROOT" 2>/dev/null | grep -q .; then
        return 0
    fi

    status=$(mysql_root_command --batch --skip-column-names -e "SHOW REPLICA STATUS" 2>/dev/null || true)
    [ -n "$status" ] && return 0
    status=$(mysql_root_command --batch --skip-column-names -e "SHOW SLAVE STATUS" 2>/dev/null || true)
    [ -n "$status" ] && return 0
    status=$(mysql_root_command --batch --skip-column-names -e "SHOW REPLICAS" 2>/dev/null || true)
    [ -n "$status" ] && return 0
    status=$(mysql_root_command --batch --skip-column-names -e "SHOW SLAVE HOSTS" 2>/dev/null || true)
    [ -n "$status" ] && return 0

    gtid=$(mysql_root_command --batch --skip-column-names -e "SELECT @@GLOBAL.gtid_mode" 2>/dev/null || true)
    [ -n "$gtid" ] && [ "${gtid^^}" != "OFF" ]
}

restart_managed_database() {
    local service_name="$1"
    systemctl restart "$service_name" >/dev/null 2>&1
}

wait_for_managed_database() {
    local attempts=30
    while [ "$attempts" -gt 0 ]; do
        if mysql_root_command --batch --skip-column-names -e "SELECT 1" >/dev/null 2>&1; then
            return 0
        fi
        attempts=$((attempts - 1))
        sleep 1
    done
    return 1
}

restart_binary_database_if_running() {
    local database_type service_name

    is_binary_install || return 0
    managed_database_url_is_local || return 0
    database_type=$(get_configured_database_type)
    case "$database_type" in
        mysql) service_name="mysql" ;;
        mariadb) service_name="mariadb" ;;
        *) return 0 ;;
    esac
    systemctl is-active --quiet "$service_name" || return 0

    colorized_echo blue "Restarting $database_type database service"
    if ! restart_managed_database "$service_name" || ! wait_for_managed_database; then
        colorized_echo red "$database_type did not become ready after restart."
        return 1
    fi
    colorized_echo green "$database_type database service restarted successfully."
}

disable_managed_database_binary_log() {
    local database_type config_file service_name extra_databases log_bin backup_file setting_added=0

    is_binary_install || return 0
    managed_database_url_is_local || return 0
    database_type=$(get_configured_database_type)
    case "$database_type" in
        mysql)
            config_file="$ANTIMAGE_MYSQL_CONFIG_ROOT/mysql.conf.d/antimage.cnf"
            service_name="mysql"
        ;;
        mariadb)
            config_file="$ANTIMAGE_MYSQL_CONFIG_ROOT/mariadb.conf.d/60-antimage.cnf"
            service_name="mariadb"
        ;;
        *) return 0 ;;
    esac
    [ -f "$config_file" ] || return 0

    extra_databases=$(mysql_root_command --batch --skip-column-names -e "SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name NOT IN ('information_schema','mysql','performance_schema','sys','antimage','phpmyadmin')" 2>/dev/null) || {
        colorized_echo yellow "Could not inspect the managed database; binary logging was left unchanged."
        return 1
    }
    if [ "$extra_databases" != "0" ]; then
        colorized_echo yellow "Binary logging was left unchanged because this MySQL/MariaDB instance contains databases not managed by antimage."
        return 0
    fi
    if managed_database_has_replication; then
        colorized_echo yellow "Binary logging was left unchanged because replication or explicit binlog settings were detected."
        return 0
    fi

    log_bin=$(mysql_root_command --batch --skip-column-names -e "SELECT @@GLOBAL.log_bin" 2>/dev/null) || {
        colorized_echo yellow "Could not read the managed database binary-log status."
        return 1
    }
    if ! grep -Eq '^[[:space:]]*skip-log-bin([[:space:]]*=.*)?[[:space:]]*$' "$config_file"; then
        backup_file=$(mktemp)
        cp -p "$config_file" "$backup_file"
        printf '\nskip-log-bin\n' >> "$config_file"
        setting_added=1
    fi
    if [ "$log_bin" = "0" ]; then
        [ -n "${backup_file:-}" ] && rm -f "$backup_file"
        return 0
    fi

    if ! mysql_root_command -e "PURGE BINARY LOGS BEFORE NOW() - INTERVAL 1 HOUR" >/dev/null 2>&1; then
        [ "$setting_added" = "1" ] && cp -p "$backup_file" "$config_file"
        [ -n "${backup_file:-}" ] && rm -f "$backup_file"
        colorized_echo yellow "Could not safely purge old MySQL/MariaDB binary logs; configuration was left unchanged."
        return 1
    fi
    if ! restart_managed_database "$service_name" || ! wait_for_managed_database; then
        [ "$setting_added" = "1" ] && cp -p "$backup_file" "$config_file"
        restart_managed_database "$service_name" || true
        [ -n "${backup_file:-}" ] && rm -f "$backup_file"
        colorized_echo yellow "MySQL/MariaDB did not restart with binary logging disabled; the previous configuration was restored."
        return 1
    fi
    log_bin=$(mysql_root_command --batch --skip-column-names -e "SELECT @@GLOBAL.log_bin" 2>/dev/null || true)
    if [ "$log_bin" != "0" ]; then
        [ "$setting_added" = "1" ] && cp -p "$backup_file" "$config_file"
        restart_managed_database "$service_name" || true
        [ -n "${backup_file:-}" ] && rm -f "$backup_file"
        colorized_echo yellow "Binary logging remained enabled; the previous configuration was restored."
        return 1
    fi

    [ -n "${backup_file:-}" ] && rm -f "$backup_file"
    colorized_echo green "Binary logging disabled for antimage's dedicated local MySQL/MariaDB service."
}

database_maintenance_command() {
    check_running_as_root
    disable_managed_database_binary_log
}

install_host_database() {
    local database_type="$1"
    local package_name
    local service_name
    local config_file

    case "$database_type" in
        mysql)
            package_name="mysql-server"
            service_name="mysql"
            config_file="$ANTIMAGE_MYSQL_CONFIG_ROOT/mysql.conf.d/antimage.cnf"
        ;;
        mariadb)
            package_name="mariadb-server"
            service_name="mariadb"
            config_file="$ANTIMAGE_MYSQL_CONFIG_ROOT/mariadb.conf.d/60-antimage.cnf"
        ;;
        *)
            return 0
        ;;
    esac

    detect_os
    if ! command -v mysql >/dev/null 2>&1 && ! command -v mariadb >/dev/null 2>&1; then
        install_package "$package_name" || {
            if [ "$database_type" = "mysql" ]; then
                install_package default-mysql-server
            else
                return 1
            fi
        }
    fi

    systemctl enable --now "$service_name" >/dev/null 2>&1 || systemctl enable --now mysql >/dev/null 2>&1 || true

    mkdir -p "$(dirname "$config_file")"
    cat > "$config_file" <<EOF
[mysqld]
bind-address=127.0.0.1
skip-name-resolve=ON
local-infile=0
symbolic-links=0
character-set-server=utf8mb4
collation-server=utf8mb4_unicode_ci
max_connections=200
EOF
    systemctl restart "$service_name" >/dev/null 2>&1 || systemctl restart mysql >/dev/null 2>&1 || true

    if [ -z "${MYSQL_PASSWORD:-}" ]; then
        prompt_for_ANTIMAGE_password
    fi
    MYSQL_ROOT_PASSWORD="${MYSQL_ROOT_PASSWORD:-$(generate_secure_mysql_password)}"
    MYSQL_PASSWORD="${MYSQL_PASSWORD:-$(generate_secure_mysql_password)}"
    local escaped_password
    escaped_password=$(sql_escape_literal "$MYSQL_PASSWORD")

    local sql_file
    sql_file=$(mktemp)
    cat > "$sql_file" <<EOF
CREATE DATABASE IF NOT EXISTS \`antimage\` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE USER IF NOT EXISTS 'antimage'@'127.0.0.1' IDENTIFIED BY '${escaped_password}';
CREATE USER IF NOT EXISTS 'antimage'@'localhost' IDENTIFIED BY '${escaped_password}';
ALTER USER 'antimage'@'127.0.0.1' IDENTIFIED BY '${escaped_password}';
ALTER USER 'antimage'@'localhost' IDENTIFIED BY '${escaped_password}';
GRANT ALL PRIVILEGES ON \`antimage\`.* TO 'antimage'@'127.0.0.1';
GRANT ALL PRIVILEGES ON \`antimage\`.* TO 'antimage'@'localhost';
DELETE FROM mysql.user WHERE User='';
DROP DATABASE IF EXISTS test;
FLUSH PRIVILEGES;
EOF
    if ! mysql_root_command < "$sql_file"; then
        rm -f "$sql_file"
        colorized_echo red "Failed to configure local $database_type. Make sure root can access MySQL/MariaDB through the local socket."
        exit 1
    fi
    rm -f "$sql_file"

    local mysql_password_url_encoded
    mysql_password_url_encoded=$(urlencode_value "$MYSQL_PASSWORD")
    upsert_env_assignment "ANTIMAGE_DATABASE_FLAVOR" "$database_type"
    upsert_env_assignment "MYSQL_DATABASE" "antimage"
    upsert_env_assignment "MYSQL_USER" "antimage"
    upsert_env_assignment "MYSQL_PASSWORD" "$MYSQL_PASSWORD"
    upsert_env_assignment "MYSQL_ROOT_PASSWORD" "$MYSQL_ROOT_PASSWORD"
    upsert_env_assignment "SQLALCHEMY_DATABASE_URL" "mysql+pymysql://antimage:${mysql_password_url_encoded}@127.0.0.1:3306/antimage"
    disable_managed_database_binary_log
}

configure_binary_database() {
    local database_type="${1:-mysql}"
    case "$database_type" in
        sqlite|"")
            upsert_env_assignment "ANTIMAGE_DATABASE_FLAVOR" "sqlite"
            upsert_env_assignment "SQLALCHEMY_DATABASE_URL" "sqlite:///${DATA_DIR}/db.sqlite3"
        ;;
        mysql|mariadb)
            install_host_database "$database_type"
        ;;
        *)
            colorized_echo red "Unsupported database type for binary install: $database_type"
            exit 1
        ;;
    esac
}

install_command() {
    check_running_as_root

    # Default values
    database_type=""
    database_type_set="false"
    ANTIMAGE_version="latest"
    ANTIMAGE_version_set="false"
    install_mode=""
    install_phpmyadmin="false"

    # Parse options
    while [[ $# -gt 0 ]]; do
        key="$1"
        case $key in
            --database)
                database_type="$2"
                database_type_set="true"
                shift 2
            ;;
            --dev)
                if [[ "$ANTIMAGE_version_set" == "true" ]]; then
                    colorized_echo red "Error: Cannot use --dev and --version options simultaneously."
                    exit 1
                fi
                ANTIMAGE_version="dev"
                ANTIMAGE_version_set="true"
                shift
            ;;
            --version)
                if [[ "$ANTIMAGE_version_set" == "true" ]]; then
                    colorized_echo red "Error: Cannot use --dev and --version options simultaneously."
                    exit 1
                fi
                if [ -z "${2:-}" ]; then
                    colorized_echo red "Error: --version requires a value."
                    exit 1
                fi
                ANTIMAGE_version="$2"
                ANTIMAGE_version_set="true"
                shift 2
            ;;
            --mode)
                if [ -z "${2:-}" ]; then
                    colorized_echo red "Error: --mode requires docker or binary."
                    exit 1
                fi
                install_mode=$(normalize_install_mode "$2")
                shift 2
            ;;
            --docker|--dockerized)
                install_mode="docker"
                shift
            ;;
            --binary)
                install_mode="binary"
                shift
            ;;
            *)
                echo "Unknown option: $1"
                exit 1
            ;;
        esac
    done

    # Check if antimage is already installed
    if is_ANTIMAGE_installed; then
        colorized_echo red "antimage is already installed at $APP_DIR"
        read -p "Do you want to override the previous installation? (y/n) "
        if [[ ! $REPLY =~ ^[Yy]$ ]]; then
            colorized_echo red "Aborted installation"
            exit 1
        fi
    fi
    install_mode=$(select_install_mode "$install_mode")
    if [ "$install_mode" = "binary" ]; then
        if [ "$database_type_set" != "true" ]; then
            database_type=$(select_database_type_interactive)
        fi
        database_type="${database_type:-mysql}"
        case "$database_type" in
            mysql|mariadb)
                if [ -t 0 ] && ui_read_yes_no "Install phpMyAdmin for this ${database_type} database?" "n"; then
                    install_phpmyadmin="true"
                fi
            ;;
        esac
    else
        database_type="${database_type:-sqlite}"
    fi
    if [[ "$ANTIMAGE_version_set" != "true" ]]; then
        ANTIMAGE_version=$(select_ANTIMAGE_version "" "$install_mode")
    fi
    set_ANTIMAGE_source_for_version "$ANTIMAGE_version"
    detect_os
    if ! command -v jq >/dev/null 2>&1; then
        install_package jq
    fi
    if ! command -v curl >/dev/null 2>&1; then
        install_package curl
    fi
    install_ANTIMAGE_script "$ANTIMAGE_version"

    if [ "$install_mode" = "docker" ]; then
        if ! command -v docker >/dev/null 2>&1; then
            install_docker
        fi
        if ! command -v yq >/dev/null 2>&1; then
            install_yq
        fi
        detect_compose
    fi

    # Function to check if a version exists in the GitHub releases
    check_version_exists() {
        local version=$1
        repo_url="https://api.github.com/repos/${ANTIMAGE_RELEASE_REPO}/releases"
        if [ "$version" == "latest" ] || [ "$version" == "dev" ]; then
            return 0
        fi
        if [[ "$version" =~ ^dev-[0-9a-fA-F]{7,40}$ ]]; then
            [ "$install_mode" = "binary" ]
            return
        fi
        
        # Fetch the release data from GitHub API
        response=$(curl -s "$repo_url")
        
        # Check if the response contains the version tag
        if echo "$response" | jq -e ".[] | select(.tag_name == \"${version}\")" > /dev/null; then
            return 0
        else
            return 1
        fi
    }
    # Check if the version is valid and exists
    if [[ "$ANTIMAGE_version" == "latest" || "$ANTIMAGE_version" == "dev" || "$ANTIMAGE_version" =~ ^dev-[0-9a-fA-F]{7,40}$ || "$ANTIMAGE_version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
        if check_version_exists "$ANTIMAGE_version"; then
            if [ "$install_mode" = "binary" ]; then
                install_binary_antimage "$ANTIMAGE_version" "$database_type"
                prompt_dashboard_bind_settings
                prompt_initial_admin
                if [ "$install_phpmyadmin" = "true" ]; then
                    ui_section "phpMyAdmin"
                    prompt_phpmyadmin_settings
                    enable_host_phpmyadmin "$PHPMYADMIN_PATH"
                fi
            else
                install_antimage "$ANTIMAGE_version" "$database_type"
                echo "docker" > "$INSTALL_MODE_FILE"
            fi
            write_ANTIMAGE_channel "$ANTIMAGE_version"
            echo "Installing $ANTIMAGE_version version"
        else
            echo "Version $ANTIMAGE_version does not exist. Please enter a valid version (e.g. v0.5.2)"
            exit 1
        fi
    else
        echo "Invalid version format. Please enter a valid version (e.g. v0.5.2)"
        exit 1
    fi
    prompt_ssl_setup
    if [ "$install_mode" = "binary" ]; then
        create_initial_admin_if_requested
    fi
    up_antimage
    follow_ANTIMAGE_logs
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


down_antimage() {
    if is_binary_install; then
        systemctl stop "$APP_NAME.service"
        return
    fi

    $COMPOSE -f $COMPOSE_FILE -p "$APP_NAME" down
}



show_ANTIMAGE_logs() {
    if is_binary_install; then
        journalctl -u "$APP_NAME.service" -o "$(journal_output_format)" --no-pager | format_ANTIMAGE_journal_logs
        return
    fi

    $COMPOSE -f $COMPOSE_FILE -p "$APP_NAME" logs
}

ANTIMAGE_cli() {
    if is_binary_install; then
        ANTIMAGE_ENV_FILE="$ENV_FILE" ANTIMAGE_APP_DIR="$APP_DIR" ANTIMAGE_DATA_DIR="$DATA_DIR" CLI_PROG_NAME="antimage cli" "$BINARY_CLI" "$@"
        return
    fi

    $COMPOSE -f $COMPOSE_FILE -p "$APP_NAME" exec -e CLI_PROG_NAME="antimage cli" antimage antimage-cli "$@"
}


is_ANTIMAGE_up() {
    if is_binary_install; then
        systemctl is-active --quiet "$APP_NAME.service"
        return
    fi

    if [ -z "$($COMPOSE -f $COMPOSE_FILE ps -q -a)" ]; then
        return 1
    else
        return 0
    fi
}

uninstall_command() {
    check_running_as_root
    local install_mode
    install_mode=$(get_install_mode)
    local app_exists=0
    if is_ANTIMAGE_installed; then
        app_exists=1
    fi

    if [ "$app_exists" -eq 0 ]; then
        colorized_echo red "antimage's not installed!"
        exit 1
    fi

    read -p "Do you really want to uninstall antimage? (y/n) "
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        colorized_echo red "Aborted"
        exit 1
    fi

    if [ "$app_exists" -eq 1 ]; then
        if [ "$install_mode" != "binary" ]; then
            detect_compose
        fi
        if is_ANTIMAGE_up; then
            down_antimage
        fi
    fi
    uninstall_ANTIMAGE_script

    if [ "$app_exists" -eq 1 ]; then
        uninstall_antimage
        if [ "$install_mode" != "binary" ]; then
            uninstall_ANTIMAGE_docker_images
        fi

        read -p "Do you want to remove antimage's data files too ($DATA_DIR)? (y/n) "
        if [[ ! $REPLY =~ ^[Yy]$ ]]; then
            colorized_echo green "antimage uninstalled successfully"
        else
            uninstall_ANTIMAGE_data_files
            colorized_echo green "antimage uninstalled successfully"
        fi
    else
        colorized_echo green "Legacy antimage script removed"
    fi
}

uninstall_ANTIMAGE_script() {
    if [ -f "/usr/local/bin/antimage" ]; then
        colorized_echo yellow "Removing antimage script"
        rm "/usr/local/bin/antimage"
    fi
}

uninstall_antimage() {
    if [ -f "$BINARY_SERVICE_UNIT" ]; then
        systemctl disable --now "$APP_NAME.service" >/dev/null 2>&1 || true
        rm -f "$BINARY_SERVICE_UNIT"
        systemctl daemon-reload
    fi
    if [ -f "$BINARY_CLI_LAUNCHER" ] || [ -L "$BINARY_CLI_LAUNCHER" ]; then
        rm -f "$BINARY_CLI_LAUNCHER"
    fi
    if [ -d "$APP_DIR" ]; then
        colorized_echo yellow "Removing directory: $APP_DIR"
        rm -r "$APP_DIR"
    fi
}

uninstall_ANTIMAGE_docker_images() {
    if ! command -v docker >/dev/null 2>&1; then
        return
    fi

    images=$(docker images | grep antimage | awk '{print $3}')
    
    if [ -n "$images" ]; then
        colorized_echo yellow "Removing Docker images of antimage"
        for image in $images; do
            if docker rmi "$image" >/dev/null 2>&1; then
                colorized_echo yellow "Image $image removed"
            fi
        done
    fi
}

uninstall_ANTIMAGE_data_files() {
    if [ -d "$DATA_DIR" ]; then
        colorized_echo yellow "Removing directory: $DATA_DIR"
        rm -r "$DATA_DIR"
    fi
}

restart_command() {
    help() {
        colorized_echo red "Usage: antimage restart [options]"
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
    
    # Check if antimage is installed
    if ! is_ANTIMAGE_installed; then
        colorized_echo red "antimage's not installed!"
        exit 1
    fi
    
    if ! is_binary_install; then
        detect_compose
    fi
    
    if is_binary_install; then
        if ! restart_binary_database_if_running; then
            return 1
        fi
        if [ "$no_logs" = true ]; then
            schedule_binary_service_restart 1
            colorized_echo green "antimage restart scheduled."
            return
        fi
        restart_binary_service_now
        follow_ANTIMAGE_logs
        return
    fi

    down_antimage
    up_antimage
    if [ "$no_logs" = false ]; then
        follow_ANTIMAGE_logs
    fi
    colorized_echo green "antimage successfully restarted!"
}
logs_command() {
    help() {
        colorized_echo red "Usage: antimage logs [options]"
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
    if ! is_ANTIMAGE_installed; then
        colorized_echo red "antimage's not installed!"
        exit 1
    fi
    
    if ! is_binary_install; then
        detect_compose
    fi
    
    if ! is_ANTIMAGE_up; then
        colorized_echo red "antimage is not up."
        exit 1
    fi
    
    if [ "$no_follow" = true ]; then
        show_ANTIMAGE_logs
    else
        follow_ANTIMAGE_logs
    fi
}

down_command() {
    
    # Check if antimage is installed
    if ! is_ANTIMAGE_installed; then
        colorized_echo red "antimage's not installed!"
        exit 1
    fi
    
    if ! is_binary_install; then
        detect_compose
    fi
    
    if ! is_ANTIMAGE_up; then
        colorized_echo red "antimage's already down"
        exit 1
    fi
    
    down_antimage
}

cli_command() {
    # Check if antimage is installed
    if ! is_ANTIMAGE_installed; then
        colorized_echo red "antimage's not installed!"
        exit 1
    fi
    
    if ! is_binary_install; then
        detect_compose
    fi
    
    if ! is_ANTIMAGE_up; then
        colorized_echo red "antimage is not up."
        exit 1
    fi
    
    ANTIMAGE_cli "$@"
}

up_command() {
    help() {
        colorized_echo red "Usage: antimage up [options]"
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
    
    # Check if antimage is installed
    if ! is_ANTIMAGE_installed; then
        colorized_echo red "antimage's not installed!"
        exit 1
    fi
    
    if ! is_binary_install; then
        detect_compose
    fi
    
    if is_ANTIMAGE_up; then
        colorized_echo red "antimage's already up"
        exit 1
    fi
    
    up_antimage
    if [ "$no_logs" = false ]; then
        follow_ANTIMAGE_logs
    fi
}

update_command() {
    check_running_as_root
    local ANTIMAGE_version=""
    local ANTIMAGE_version_set="false"

    while [[ $# -gt 0 ]]; do
        case "$1" in
            --dev)
                if [[ "$ANTIMAGE_version_set" == "true" ]]; then
                    colorized_echo red "Error: Cannot use --dev and --version options simultaneously."
                    exit 1
                fi
                ANTIMAGE_version="dev"
                ANTIMAGE_version_set="true"
                shift
                ;;
            --version)
                if [[ "$ANTIMAGE_version_set" == "true" ]]; then
                    colorized_echo red "Error: Cannot use --dev and --version options simultaneously."
                    exit 1
                fi
                if [ -z "${2:-}" ]; then
                    colorized_echo red "Error: --version requires a value."
                    exit 1
                fi
                ANTIMAGE_version="$2"
                ANTIMAGE_version_set="true"
                shift 2
                ;;
            -h|--help)
                colorized_echo red "Usage: antimage update [--dev | --version vX.Y.Z|dev-abcdef0]"
                exit 0
                ;;
            *)
                colorized_echo red "Unknown option: $1"
                exit 1
                ;;
        esac
    done

    # Check if antimage is installed
    if ! is_ANTIMAGE_installed; then
        colorized_echo red "antimage's not installed!"
        exit 1
    fi

    if [[ "$ANTIMAGE_version_set" != "true" ]]; then
        ANTIMAGE_version=$(get_installed_ANTIMAGE_channel)
    fi
    set_ANTIMAGE_source_for_version "$ANTIMAGE_version"

    if ! is_binary_install; then
        if [[ "$ANTIMAGE_version" =~ ^dev-[0-9a-fA-F]{7,40}$ ]]; then
            colorized_echo red "Specific dev binary versions are available only for binary installations."
            exit 1
        fi
        detect_compose
    fi
    
    colorized_echo blue "Updating antimage CLI..."
    update_ANTIMAGE_script "$ANTIMAGE_version"

    colorized_echo blue "Updating requested version: $ANTIMAGE_version"
    update_antimage "$ANTIMAGE_version"
    if is_binary_install && [ -d /usr/share/phpmyadmin ]; then
        configure_phpmyadmin_upload_limits
    fi
    write_ANTIMAGE_channel "$ANTIMAGE_version"
    
    colorized_echo blue "Restarting antimage's services"
    if is_binary_install; then
        if ! restart_binary_database_if_running; then
            return 1
        fi
        schedule_binary_service_restart 1
        colorized_echo blue "antimage updated successfully; restart scheduled."
        return
    fi

    down_antimage
    prune_unused_docker_images
    up_antimage
    
    colorized_echo blue "antimage updated successfully"
}

update_ANTIMAGE_script() {
    local source_version="${1:-}"
    local temp_script
    if [ -n "$source_version" ]; then
        set_ANTIMAGE_source_for_version "$source_version"
    elif is_ANTIMAGE_installed; then
        set_ANTIMAGE_source_for_version "$(get_installed_ANTIMAGE_channel)"
    fi
    SCRIPT_URL="$ANTIMAGE_SCRIPT_BASE_URL/$ANTIMAGE_SCRIPT_SOURCE_FILE"
    colorized_echo blue "Updating antimage script"
    temp_script=$(mktemp)
    curl -fsSL "$SCRIPT_URL" -o "$temp_script"
    if head -n 1 "$temp_script" | grep -qi "<!DOCTYPE"; then
        rm -f "$temp_script"
        colorized_echo red "Unexpected HTML response while downloading script"
        exit 1
    fi
    install -m 755 "$temp_script" "$ANTIMAGE_SCRIPT_INSTALL_PATH"
    rm -f "$temp_script"
    colorized_echo green "antimage script updated successfully"
}

set_compose_ANTIMAGE_image_tag() {
    local ANTIMAGE_version="$1"

    if ! command -v yq >/dev/null 2>&1; then
        install_yq
    fi

    if [ "$ANTIMAGE_version" = "latest" ]; then
        yq -i '.services.antimage.image = "antimagepanel/antimage:latest"' "$COMPOSE_FILE"
    else
        yq -i ".services.antimage.image = \"antimagepanel/antimage:${ANTIMAGE_version}\"" "$COMPOSE_FILE"
    fi
}

update_antimage() {
    local ANTIMAGE_version="${1:-latest}"

    if is_binary_install; then
        install_binary_antimage "$ANTIMAGE_version" "$(get_configured_database_type)" "0"
        return
    fi

    set_compose_ANTIMAGE_image_tag "$ANTIMAGE_version"
    $COMPOSE -f $COMPOSE_FILE -p "$APP_NAME" pull
}

migration_sqlite_path() {
    local db_url
    db_url=$(get_env_value "SQLALCHEMY_DATABASE_URL")
    case "$db_url" in
        sqlite:////*)
            printf "/%s\n" "${db_url#sqlite:////}"
        ;;
        sqlite:///*)
            printf "%s\n" "${db_url#sqlite:///}"
        ;;
        *)
            printf "%s/db.sqlite3\n" "$DATA_DIR"
        ;;
    esac
}

detect_docker_database_type() {
    if [ -f "$COMPOSE_FILE" ] && grep -qi "image:.*mariadb" "$COMPOSE_FILE"; then
        echo "mariadb"
    elif [ -f "$COMPOSE_FILE" ] && grep -qi "image:.*mysql" "$COMPOSE_FILE"; then
        echo "mysql"
    else
        local db_url
        db_url=$(get_env_value "SQLALCHEMY_DATABASE_URL")
        if [[ "$db_url" == mysql* ]]; then
            echo "mysql"
        else
            echo "sqlite"
        fi
    fi
}

dump_docker_database() {
    local database_type="$1"
    local backup_dir="$2"
    local root_password
    local container_id

    case "$database_type" in
        mysql)
            root_password=$(get_env_value "MYSQL_ROOT_PASSWORD")
            container_id=$($COMPOSE -f "$COMPOSE_FILE" -p "$APP_NAME" ps -q mysql)
            if [ -z "$container_id" ]; then
                colorized_echo red "MySQL container not found."
                exit 1
            fi
            docker exec "$container_id" mysqldump -uroot -p"$root_password" --single-transaction --routines --events --triggers antimage > "$backup_dir/db.sql"
        ;;
        mariadb)
            root_password=$(get_env_value "MYSQL_ROOT_PASSWORD")
            container_id=$($COMPOSE -f "$COMPOSE_FILE" -p "$APP_NAME" ps -q mariadb)
            if [ -z "$container_id" ]; then
                colorized_echo red "MariaDB container not found."
                exit 1
            fi
            docker exec "$container_id" sh -c 'command -v mariadb-dump >/dev/null 2>&1 && exec mariadb-dump "$@" || exec mysqldump "$@"' sh -uroot -p"$root_password" --single-transaction --routines --events --triggers antimage > "$backup_dir/db.sql"
        ;;
        sqlite)
            local sqlite_path
            sqlite_path=$(migration_sqlite_path)
            if [ ! -f "$sqlite_path" ]; then
                colorized_echo red "SQLite database not found at $sqlite_path"
                exit 1
            fi
            cp "$sqlite_path" "$backup_dir/db.sqlite3"
        ;;
    esac
}

import_binary_database_backup() {
    local database_type="$1"
    local backup_dir="$2"
    case "$database_type" in
        sqlite)
            if [ -f "$backup_dir/db.sqlite3" ]; then
                mkdir -p "$DATA_DIR"
                install -m 600 "$backup_dir/db.sqlite3" "$DATA_DIR/db.sqlite3"
            fi
        ;;
        mysql|mariadb)
            if [ -f "$backup_dir/db.sql" ]; then
                mysql_root_command -e "DROP DATABASE IF EXISTS \`antimage\`; CREATE DATABASE \`antimage\` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;"
                mysql_root_command antimage < "$backup_dir/db.sql"
            fi
        ;;
    esac
}

migrate_docker_to_binary_command() {
    check_running_as_root
    local ANTIMAGE_version=""
    local ANTIMAGE_version_set="false"
    local yes="false"
    local database_type=""

    while [[ $# -gt 0 ]]; do
        case "$1" in
            --dev)
                ANTIMAGE_version="dev"
                ANTIMAGE_version_set="true"
                shift
            ;;
            --version)
                ANTIMAGE_version="$2"
                ANTIMAGE_version_set="true"
                shift 2
            ;;
            --database)
                database_type="$2"
                shift 2
            ;;
            -y|--yes)
                yes="true"
                shift
            ;;
            -h|--help)
                colorized_echo red "Usage: antimage migrate-binary [--database sqlite|mysql|mariadb] [--dev | --version vX.Y.Z] [-y]"
                exit 0
            ;;
            *)
                colorized_echo red "Unknown option: $1"
                exit 1
            ;;
        esac
    done

    if ! is_ANTIMAGE_installed || [ ! -f "$COMPOSE_FILE" ]; then
        colorized_echo red "Docker installation not found at $APP_DIR"
        exit 1
    fi
    if is_binary_install; then
        colorized_echo yellow "antimage is already in binary mode."
        exit 0
    fi
    if [ "$ANTIMAGE_version_set" != "true" ]; then
        ANTIMAGE_version=$(get_installed_ANTIMAGE_channel)
    fi
    database_type="${database_type:-$(detect_docker_database_type)}"
    case "$database_type" in
        sqlite|mysql|mariadb) ;;
        *)
            colorized_echo red "Unsupported database type: $database_type"
            exit 1
        ;;
    esac

    if [ "$yes" != "true" ]; then
        colorized_echo yellow "This will migrate antimage from Docker to binary mode using $database_type."
        read -p "Continue? (y/n) "
        if [[ ! $REPLY =~ ^[Yy]$ ]]; then
            colorized_echo red "Aborted"
            exit 1
        fi
    fi

    detect_compose
    local backup_dir="/opt/antimage-docker-to-binary-backups/$(date -u +%Y%m%d%H%M%S)"
    mkdir -p "$backup_dir"
    cp "$ENV_FILE" "$backup_dir/.env" 2>/dev/null || true
    cp "$COMPOSE_FILE" "$backup_dir/docker-compose.yml" 2>/dev/null || true
    if command -v rsync >/dev/null 2>&1; then
        rsync -a --exclude mysql --exclude xray-core "$DATA_DIR/" "$backup_dir/antimage-data/" 2>/dev/null || true
    else
        mkdir -p "$backup_dir/antimage-data"
        cp -a "$DATA_DIR/." "$backup_dir/antimage-data/" 2>/dev/null || true
        rm -rf "$backup_dir/antimage-data/mysql" "$backup_dir/antimage-data/xray-core" 2>/dev/null || true
    fi

    if [ "$database_type" = "sqlite" ] && is_ANTIMAGE_up; then
        down_antimage
    fi
    if [ "$database_type" != "sqlite" ] && ! is_ANTIMAGE_up; then
        up_antimage
        sleep 8
    fi

    colorized_echo blue "Dumping Docker database to $backup_dir"
    dump_docker_database "$database_type" "$backup_dir"

    if is_ANTIMAGE_up; then
        down_antimage
    fi
    mv "$COMPOSE_FILE" "$COMPOSE_FILE.docker-migrated" 2>/dev/null || true

    colorized_echo blue "Installing antimage binary files"
    MYSQL_PASSWORD="${MYSQL_PASSWORD:-$(get_env_value "MYSQL_PASSWORD")}"
    MYSQL_ROOT_PASSWORD="${MYSQL_ROOT_PASSWORD:-$(get_env_value "MYSQL_ROOT_PASSWORD")}"
    install_binary_antimage "$ANTIMAGE_version" "$database_type"

    colorized_echo blue "Importing database backup into binary installation"
    import_binary_database_backup "$database_type" "$backup_dir"

    write_ANTIMAGE_channel "$ANTIMAGE_version"
    echo "binary" > "$INSTALL_MODE_FILE"
    up_antimage
    colorized_echo green "Migration to binary mode completed. Backup kept at $backup_dir"
}

prune_unused_docker_images() {
    colorized_echo blue "Removing old and unused Docker images"
    if docker image prune -a -f >/dev/null 2>&1; then
        colorized_echo green "Unused Docker images removed successfully"
    else
        colorized_echo yellow "Unable to prune Docker images; continuing update"
    fi
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
        mkdir -p "$(dirname "$ENV_FILE")"
        touch "$ENV_FILE"
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

edit_env_command() {
    detect_os
    check_editor
    if [ -f "$ENV_FILE" ]; then
        $EDITOR "$ENV_FILE"
    else
        colorized_echo red "Environment file not found at $ENV_FILE"
        exit 1
    fi
}

menu_commands() {
    echo "up down restart status logs cli migrate backup backup-service install update uninstall script-install script-update script-uninstall core-update enable-phpmyadmin disable-phpmyadmin edit edit-env ssl help"
}

menu_category_for() {
    case "$1" in
        up|down|restart|status|logs) echo "Panel runtime" ;;
        cli|migrate|backup|backup-service) echo "Administration and data" ;;
        install|update|uninstall) echo "Install and update" ;;
        script-install|script-update|script-uninstall) echo "Script management" ;;
        core-update|enable-phpmyadmin|disable-phpmyadmin|edit|edit-env|ssl) echo "Tools and legacy" ;;
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
        cli) echo "antimage CLI" ;;
        migrate) echo "Run database migrations" ;;
        backup) echo "Manual backup launch" ;;
        backup-service) echo "Backup service (Telegram + cron job)" ;;
        install) echo "Install antimage" ;;
        update) echo "Update to latest version" ;;
        uninstall) echo "Uninstall antimage" ;;
        script-install) echo "Install antimage script" ;;
        script-update) echo "Update antimage CLI script" ;;
        script-uninstall) echo "Uninstall antimage script" ;;
        core-update) echo "Deprecated; Xray is managed by nodes" ;;
        enable-phpmyadmin) echo "Enable phpMyAdmin on local MySQL/MariaDB" ;;
        disable-phpmyadmin) echo "Disable phpMyAdmin panel bridge" ;;
        edit) echo "Edit docker-compose.yml" ;;
        edit-env) echo "Edit environment file" ;;
        ssl) echo "Issue or renew SSL certificates" ;;
        help) echo "Show this help message" ;;
        *) echo "" ;;
    esac
}

print_menu() {
    local selected="${1:-0}"
    local previous_category=""
    local idx=1
    local cmd category desc is_selected columns tip_width tip
    ui_header "antimage Panel" "Control center"
    ui_section "Status"
    print_menu_status_summary
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

usage() {
    local script_name="${0##*/}"
    colorized_echo blue "=============================="
    colorized_echo magenta "           antimage Help"
    colorized_echo blue "=============================="
    colorized_echo cyan "Usage:"
    echo "  ${script_name} [command]"
    echo

    colorized_echo cyan "Commands:"
    colorized_echo yellow "  up              – Start services"
    colorized_echo yellow "  down            – Stop services"
    colorized_echo yellow "  restart         – Restart services"
    colorized_echo yellow "  status          – Show status"
    colorized_echo yellow "  logs            - Show logs"
    colorized_echo yellow "  cli             - antimage CLI"
    colorized_echo yellow "  migrate         - Run database migrations"
    colorized_echo yellow "  install         - Install antimage"
    colorized_echo yellow "  update          - Update to latest/dev or a specific release"
    colorized_echo yellow "  uninstall       - Uninstall antimage"
    colorized_echo yellow "  script-install  - Install antimage script"
    colorized_echo yellow "  script-update   - Update antimage CLI script"
    colorized_echo yellow "  script-uninstall  - Uninstall antimage script"
    colorized_echo yellow "  backup          - Manual backup launch"
    colorized_echo yellow "  backup-service  - antimage Backupservice to backup to TG, and a new job in crontab"
    colorized_echo yellow "  core-update     - Deprecated; Xray is managed by nodes"
    colorized_echo yellow "  enable-phpmyadmin - Enable phpMyAdmin for local MySQL/MariaDB"
    colorized_echo yellow "  disable-phpmyadmin - Disable phpMyAdmin"
    colorized_echo yellow "  prepare-external-app-hosting - Install PHP-FPM hosting prerequisites without Apache"
    colorized_echo yellow "  prepare-external-app-node-hosting - Install an isolated Node.js LTS runtime"
    colorized_echo yellow "  edit            - Edit docker-compose.yml (via nano or vi editor)"
    colorized_echo yellow "  edit-env        - Edit environment file (via nano or vi editor)"
    colorized_echo yellow "  ssl             - Issue or renew SSL certificates"
    colorized_echo yellow "  help            - Show this help message"
    
    
    echo
    colorized_echo cyan "Directories:"
    colorized_echo magenta "  App directory: $APP_DIR"
    colorized_echo magenta "  Data directory: $DATA_DIR"
    echo
    colorized_echo cyan "Install options:"
    case "$(script_install_mode)" in
        docker)
            colorized_echo magenta "  This script installs Docker mode only."
            ;;
        binary)
            colorized_echo magenta "  This script installs binary mode only."
            ;;
    esac
    colorized_echo magenta "  --database sqlite|mysql|mariadb"
    colorized_echo magenta "  --dev or --version vX.Y.Z (install/update)"
    echo
    current_version=$(get_current_xray_core_version)
    colorized_echo cyan "Current Xray-core version: $current_version"
    colorized_echo blue "================================"
    echo
}

dispatch_command() {
    local cmd="$1"
    shift || true
    case "$cmd" in
        help|install|script-install|install-script|script-update|update-script|script-uninstall|uninstall-script)
            ;;
        *)
            ensure_script_matches_installed_mode
            ;;
    esac
    case "$cmd" in
        up) up_command "$@" ;;
        down) down_command "$@" ;;
        restart) restart_command "$@" ;;
        status) status_command "$@" ;;
        logs) logs_command "$@" ;;
        cli) cli_command "$@" ;;
        migrate) cli_command migrate "$@" ;;
        backup) backup_command "$@" ;;
        backup-service) backup_service "$@" ;;
        database-maintenance) database_maintenance_command "$@" ;;
        install) install_command "$@" ;;
        update) update_command "$@" ;;
        uninstall) uninstall_command "$@" ;;
        script-install|install-script) install_ANTIMAGE_script "$@" ;;
        script-update|update-script) install_ANTIMAGE_script "$@" ;;
        script-uninstall|uninstall-script) uninstall_ANTIMAGE_script "$@" ;;
        core-update) update_core_command "$@" ;;
        enable-phpmyadmin) enable_phpmyadmin "$@" ;;
        disable-phpmyadmin) disable_phpmyadmin "$@" ;;
        prepare-external-app-hosting) prepare_external_app_hosting "$@" ;;
        prepare-external-app-node-hosting) prepare_external_app_node_hosting "$@" ;;
        ssl) ssl_command "$@" ;;
        edit) edit_command "$@" ;;
        edit-env) edit_env_command "$@" ;;
        help) usage ;;
        *) usage ;;
    esac
}

if [ "${ANTIMAGE_SOURCE_ONLY:-0}" != "1" ]; then
    if [ $# -eq 0 ]; then
        read_menu_command || exit 0
        set -- $MENU_COMMAND
    fi

    dispatch_command "$@"
fi
