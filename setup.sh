#!/bin/sh
# Install Xbox Download Proxy locally without changing host networking or privileges.
set -eu

usage() {
  cat <<'EOF'
Usage: setup.sh [options]

Interactive by default. Required choices are prompted when a terminal is available.

Options:
  --mode native|compose  Install a local binary or a Docker Compose setup
  --install-dir PATH     Installation directory (default: $HOME/.local/share/xdp)
  --lan-ip IPv4          LAN IPv4 address used by the service (required)
  --web-port PORT        Web/API port (default: 8080)
  --offline yes|no       Enable or disable offline downloads and Xbox local delivery
  --binary PATH          Install this prebuilt xdp instead of compiling (low-memory hosts)
  --install-deps         Install native dependencies with apk/apt-get (requires root)
  --start                In Compose mode, start Web/API after initialization
  --yes                  Allow replacing generated files and binaries
  --help                 Show this help

Native mode builds current sources with Go, or uses an existing ./xdp if Go is unavailable. Compose mode needs
the Docker Compose plugin (`docker compose`). Compose initializes data but does
not start DNS/HTTP services; --start starts only the Web/API container.
Linux native installs also generate OpenRC/systemd definitions and install-service.sh.
Run that helper separately as root to enable a system service. No Bash is needed.
EOF
}

fail() { printf 'setup: %s\n' "$*" >&2; exit 1; }
has_tty=0
if [ -t 0 ] && [ -t 1 ]; then has_tty=1; fi
mode=
install_dir=${HOME:?HOME is not set}/.local/share/xdp
install_dir_set=0
lan_ip=
web_port=8080
web_port_set=0
yes=0
start=0
offline=
offline_set=0
prebuilt=
install_deps=0

while [ "$#" -gt 0 ]; do
  case "$1" in
    --mode) [ "$#" -ge 2 ] || fail "--mode requires a value"; mode=$2; shift 2 ;;
    --install-dir) [ "$#" -ge 2 ] || fail "--install-dir requires a value"; install_dir=$2; install_dir_set=1; shift 2 ;;
    --lan-ip) [ "$#" -ge 2 ] || fail "--lan-ip requires a value"; lan_ip=$2; shift 2 ;;
    --web-port) [ "$#" -ge 2 ] || fail "--web-port requires a value"; web_port=$2; web_port_set=1; shift 2 ;;
    --offline) [ "$#" -ge 2 ] || fail "--offline requires yes or no"; offline=$2; offline_set=1; shift 2 ;;
    --binary)
      if [ "$#" -lt 2 ] || [ -z "$2" ]; then
        fail "--binary requires a path"
      fi
      prebuilt=$2; shift 2 ;;
    --install-deps) install_deps=1; shift ;;
    --yes) yes=1; shift ;;
    --start) start=1; shift ;;
    --help|-h) usage; exit 0 ;;
    *) fail "unknown option: $1" ;;
  esac
done

if [ "$offline_set" -eq 1 ]; then
  case "$offline" in
    yes|YES|y|Y) offline=true ;;
    no|NO|n|N) offline=false ;;
    *) fail "--offline must be yes or no" ;;
  esac
fi

if [ -z "$mode" ]; then
  [ "$has_tty" -eq 1 ] || fail "--mode is required without an interactive terminal"
  printf 'Installation mode [native/compose] (native): '
  IFS= read -r answer || true
  mode=${answer:-native}
fi
case "$mode" in native|compose) ;; *) fail "mode must be native or compose" ;; esac
if [ "$mode" = compose ] && { [ -n "$prebuilt" ] || [ "$install_deps" -eq 1 ]; }; then
  fail "--binary and --install-deps are only supported in native mode"
fi
if [ "$has_tty" -eq 1 ] && [ "$offline_set" -eq 0 ]; then
  printf 'Enable offline downloads and Xbox local delivery? [y/N; Enter keeps existing setting]: '
  IFS= read -r answer || true
  case "$answer" in
    y|Y|yes|YES) offline=true; offline_set=1 ;;
    n|N|no|NO) offline=false; offline_set=1 ;;
  esac
fi
if [ "$mode" = native ] && [ "$start" -eq 1 ]; then
  fail "--start is only supported in Compose mode"
fi

if [ "$has_tty" -eq 1 ] && [ "$install_dir_set" -eq 0 ]; then
  printf 'Installation directory [%s]: ' "$install_dir"
  IFS= read -r answer || true
  install_dir=${answer:-$install_dir}
fi

if [ -z "$lan_ip" ]; then
  [ "$has_tty" -eq 1 ] || fail "--lan-ip is required without an interactive terminal"
  printf 'LAN IPv4 address: '
  IFS= read -r lan_ip || true
fi
case "$lan_ip" in
  *.*.*.*) ;;
  *) fail "invalid IPv4 address: $lan_ip" ;;
esac
old_ifs=$IFS
IFS=.
set -f
# The unquoted expansion is intentional: split the IPv4 address on dots.
# shellcheck disable=SC2086
set -- $lan_ip
set +f
IFS=$old_ifs
[ "$#" -eq 4 ] || fail "invalid IPv4 address: $lan_ip"
[ "$lan_ip" = "$1.$2.$3.$4" ] || fail "invalid IPv4 address: $lan_ip"
for octet in "$@"; do
  case "$octet" in ''|*[!0-9]*) fail "invalid IPv4 address: $lan_ip" ;; esac
  if [ "${#octet}" -gt 1 ] && [ "${octet#0}" != "$octet" ]; then
    fail "IPv4 octets must not contain leading zeroes: $lan_ip"
  fi
  [ "${#octet}" -le 3 ] || fail "invalid IPv4 address: $lan_ip"
  [ "$octet" -le 255 ] || fail "invalid IPv4 address: $lan_ip"
done
first_octet=$1
if [ "$first_octet" -eq 0 ] || [ "$first_octet" -eq 127 ] || [ "$first_octet" -ge 224 ]; then
  fail "LAN IPv4 address cannot be unspecified, loopback, multicast, or reserved: $lan_ip"
fi

if [ "$has_tty" -eq 1 ] && [ "$web_port_set" -eq 0 ]; then
  printf 'Web/API port [%s]: ' "$web_port"
  IFS= read -r answer || true
  web_port=${answer:-$web_port}
fi
case "$web_port" in ''|*[!0-9]*) fail "Web port must be an integer from 1 to 65535" ;; esac
[ "${#web_port}" -le 5 ] || fail "Web port must be an integer from 1 to 65535"
[ "${#web_port}" -eq 1 ] || [ "${web_port#0}" = "$web_port" ] || fail "Web port must not contain leading zeroes"
if [ "$web_port" -lt 1 ] || [ "$web_port" -gt 65535 ]; then
  fail "Web port must be an integer from 1 to 65535"
fi
case "$web_port" in 53|80|443) fail "Web port must not conflict with DNS/HTTP/TLS port $web_port" ;; esac

if [ "$has_tty" -eq 1 ] && [ "$mode" = compose ] && [ "$start" -eq 0 ]; then
  printf 'Start the Web/API service after setup? [y/N]: '
  IFS= read -r answer || true
  case "$answer" in y|Y|yes|YES) start=1 ;; esac
fi

script_dir=$(CDPATH='' cd -- "$(dirname "$0")" && pwd -P)
os=$(uname -s)
if [ "$install_deps" -eq 1 ]; then
  [ "$os" = Linux ] || fail "--install-deps supports Debian and Alpine Linux only"
  [ "$(id -u)" -eq 0 ] || fail "--install-deps requires root; rerun explicitly with su, sudo or doas"
  need_go=0
  if [ -z "$prebuilt" ] && ! command -v go >/dev/null 2>&1; then
    if [ ! -x "$script_dir/xdp" ] || ! "$script_dir/xdp" --version >/dev/null 2>&1; then need_go=1; fi
  fi
  if command -v apk >/dev/null 2>&1; then
    set -- ca-certificates
    [ "$need_go" -eq 0 ] || set -- "$@" go
    apk add --no-cache "$@" || fail "Alpine dependency installation failed"
  elif command -v apt-get >/dev/null 2>&1; then
    set -- ca-certificates
    [ "$need_go" -eq 0 ] || set -- "$@" golang-go
    apt-get update || fail "Debian package index update failed"
    DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends "$@" || fail "Debian dependency installation failed"
  else
    fail "--install-deps needs Alpine apk or Debian apt-get"
  fi
fi
[ -n "$install_dir" ] || fail "installation directory must not be empty"
case "$install_dir" in /*) ;; *) install_dir=$(pwd -P)/$install_dir ;; esac
mkdir -p "$install_dir"
install_dir=$(CDPATH='' cd -- "$install_dir" && pwd -P)
[ "$install_dir" != "$script_dir" ] || fail "installation directory cannot be the repository root"
mkdir -p "$install_dir/data"
chmod 700 "$install_dir/data" 2>/dev/null || true

quote_sh() { printf "'%s'" "$(printf '%s' "$1" | sed "s/'/'\\\\''/g")"; }

confirm_replace() {
  target=$1
  [ -e "$target" ] || [ -L "$target" ] || return 0
  if [ "$yes" -eq 1 ]; then return 0; fi
  if [ "$has_tty" -eq 1 ]; then
    printf '%s exists. Replace it? [y/N]: ' "$target"
    IFS= read -r answer || true
    case "$answer" in y|Y|yes|YES) return 0 ;; esac
  fi
  fail "refusing to replace existing generated file: $target (use --yes to update)"
}

if [ "$mode" = native ]; then
  binary=$install_dir/xdp
  start_script=$install_dir/start.sh
  confirm_replace "$binary"
  confirm_replace "$start_script"
  if [ "$os" = Linux ]; then
    [ -f "$script_dir/scripts/install-service.sh" ] || fail "missing scripts/install-service.sh; use a complete repository checkout"
    for generated in xdp.service xdp.openrc install-service.sh; do confirm_replace "$install_dir/$generated"; done
    # Service definitions are line-oriented. Shell launchers still support spaces and quotes.
    cr=$(printf '\r')
    tab=$(printf '\t')
    case "$install_dir" in *'
'*|*"$cr"*|*"$tab"*) fail "installation path cannot contain line breaks or tabs" ;; esac
  fi
  staged=$(mktemp "$install_dir/.xdp.XXXXXX")
  trap 'rm -f "$staged"' EXIT
  trap 'exit 1' HUP INT TERM
  if [ -n "$prebuilt" ]; then
    if [ ! -f "$prebuilt" ] || [ ! -r "$prebuilt" ]; then
      fail "cannot read --binary file: $prebuilt"
    fi
    cp "$prebuilt" "$staged" || fail "could not copy prebuilt binary"
  elif command -v go >/dev/null 2>&1; then
    go_version=$(GOTOOLCHAIN=local go version) || fail "could not run Go; use Go >= 1.24 or --binary PATH"
    case "$go_version" in
      *' go1.'*)
        minor=${go_version#* go1.}; minor=${minor%%[!0-9]*}
        if [ -z "$minor" ] || [ "$minor" -lt 24 ]; then
          fail "Go >= 1.24 is required ($go_version); distribution packages may be older. Use --binary PATH or install a newer Go toolchain"
        fi
        ;;
      *' go2.'*|*' devel '*) ;;
      *) fail "cannot determine Go version: $go_version" ;;
    esac
    (cd "$script_dir" && CGO_ENABLED=0 go build -trimpath -o "$staged" ./cmd/xdp) || fail "Go build failed; on low-memory containers use --binary PATH"
  elif [ -x "$script_dir/xdp" ]; then
    cp "$script_dir/xdp" "$staged"
  else
    fail "native install needs Go >= 1.24 or a prebuilt xdp; use --binary PATH (recommended on small containers), or --install-deps as root on Debian/Alpine"
  fi
  chmod 755 "$staged"
  "$staged" --version >/dev/null 2>&1 || fail "binary cannot run on $(uname -s)/$(uname -m); select the matching Linux/macOS amd64/arm64 build"
  # Replacing by rename also works while an earlier executable is running on Linux.
  mv -f "$staged" "$binary"
  if [ "$offline_set" -eq 1 ]; then
    "$binary" init --data-dir "$install_dir/data" "--offline=$offline"
  else
    "$binary" init --data-dir "$install_dir/data"
  fi

  native_args() {
    printf 'serve --data-dir '
    quote_sh "$install_dir/data"
    printf ' --lan-ip '
    quote_sh "$lan_ip"
    printf ' --web '
    quote_sh ":$web_port"
  }
  {
    printf '#!/bin/sh\nset -eu\nexec '
    quote_sh "$binary"
    printf ' '
    native_args
    printf ' "$@"\n'
  } > "$start_script"
  chmod 755 "$start_script"
  if [ "$os" = Linux ]; then
    # systemd performs its own percent/dollar substitutions even inside quoted arguments.
    systemd_path=$(printf '%s' "$start_script" | sed 's/\\/\\\\/g; s/"/\\"/g; s/%/%%/g; s/\$/$$/g')
    {
      printf '[Unit]\nDescription=Xbox Download Proxy\nWants=network-online.target\nAfter=network-online.target\n\n[Service]\nType=simple\n'
      printf 'ExecStart=/bin/sh "%s"\n' "$systemd_path"
      printf 'Restart=on-failure\nRestartSec=3\nUMask=0077\n\n[Install]\nWantedBy=multi-user.target\n'
    } > "$install_dir/xdp.service"
    # These variables are expanded by openrc-run, not by this installer.
    # shellcheck disable=SC2016
    {
      printf '#!/sbin/openrc-run\n\ndescription="Xbox Download Proxy"\numask 077\nsupervisor=supervise-daemon\ncommand=/bin/sh'
      printf '\ncommand_args='
      quote_sh "$(quote_sh "$start_script")"
      printf '\nrespawn_delay=3\npidfile="/run/${RC_SVCNAME}.pid"\nlog_path='
      quote_sh "$install_dir/logs/xdp.log"
      # OpenRC evaluates these fields as shell words when starting supervise-daemon.
      printf '\noutput_log='
      quote_sh "$(quote_sh "$install_dir/logs/xdp.log")"
      printf '\nerror_log="$output_log"\n\ndepend() {\n  use net\n  after firewall\n}\n\nstart_pre() {\n  checkpath --directory --mode 0700 '
      quote_sh "$install_dir/logs"
      printf '\n  checkpath --file --mode 0600 "$log_path"\n}\n'
    } > "$install_dir/xdp.openrc"
    cp "$script_dir/scripts/install-service.sh" "$install_dir/install-service.sh"
    chmod 755 "$install_dir/xdp.openrc" "$install_dir/install-service.sh"
  fi
  printf '\nInstalled native xdp.\nStart Web/API with: '
  quote_sh "$start_script"
  printf '\n'
  printf 'Offline downloads and Xbox local delivery: configured (new installations default to disabled).\n'
  printf 'Web UI: http://%s:%s\n' "$lan_ip" "$web_port"
  if [ "$(id -u)" -ne 0 ]; then
    printf 'Binding DNS/HTTP/TLS low ports needs permission. Stop the foreground server before restarting as root (su, sudo or doas).\n'
  fi
  if [ "$os" = Linux ]; then
    printf 'Optional systemd/OpenRC service: as root run '
    quote_sh "$install_dir/install-service.sh"
    printf ' --start\nNo system service has been installed or enabled by setup.\n'
    if [ ! -s /etc/ssl/certs/ca-certificates.crt ] && [ ! -s /etc/ssl/cert.pem ]; then
      printf 'Warning: CA certificates are missing; HTTPS candidate sources need ca-certificates. Rerun with --install-deps as root.\n'
    fi
  fi
  printf 'Setup never runs privilege escalation or changes DNS/firewall settings.\n'
  if [ -r "$install_dir/data/token" ]; then
    printf 'Management token: '
    cat "$install_dir/data/token"
    printf '\n'
  else
    fail "initialized, but could not read token at $install_dir/data/token"
  fi
  exit 0
fi

command -v docker >/dev/null 2>&1 || fail "Docker is required for Compose mode"
docker compose version >/dev/null 2>&1 || fail "Docker Compose plugin is required (docker compose version failed)"
mkdir -p "$install_dir/data/downloads"
if [ "$(uname -s 2>/dev/null || true)" != Linux ]; then
  printf 'Compose host networking is intended mainly for Linux NAS; Docker Desktop on macOS may behave differently.\n'
fi
compose_file=$install_dir/compose.yaml
confirm_replace "$compose_file"

# YAML single quoted scalars protect YAML syntax; doubling dollars also prevents Compose interpolation.
yaml_quote() {
  value=$1
  escaped=
  while [ -n "$value" ]; do
    rest=${value#?}
    char=${value%"$rest"}
    value=$rest
    case "$char" in
      "'") escaped="$escaped''" ;;
      '$') escaped="${escaped}\$\$" ;;
      *) escaped="$escaped$char" ;;
    esac
  done
  printf "'%s'" "$escaped"
}
{
  printf 'services:\n  xdp:\n    build:\n      context: '
  yaml_quote "$script_dir"
  printf '\n      dockerfile: '
  yaml_quote "$script_dir/Dockerfile"
  printf '\n    image: xdp:local\n    network_mode: host\n    cap_add:\n      - NET_BIND_SERVICE\n    volumes:\n      - type: bind\n        source: '
  yaml_quote "$install_dir/data"
  printf '\n        target: /data\n      - type: bind\n        source: '
  yaml_quote "$install_dir/data/downloads"
  printf '\n        target: /downloads\n    command:\n      - serve\n      - --data-dir\n      - /data\n      - --lan-ip\n      - '
  yaml_quote "$lan_ip"
  printf '\n      - --web\n      - '
  yaml_quote ":$web_port"
  printf '\n      - --download-dir\n      - /downloads\n'
} > "$compose_file"

compose() { docker compose -f "$compose_file" "$@"; }
compose build || fail "Docker Compose image build failed"
if [ "$offline_set" -eq 1 ]; then
  compose run --rm xdp init --data-dir /data "--offline=$offline" || fail "Compose initialization failed"
else
  compose run --rm xdp init --data-dir /data || fail "Compose initialization failed"
fi
if [ "$start" -eq 1 ]; then
  compose up -d || fail "Compose Web/API startup failed"
fi
printf '\nInstalled Compose setup.\n'
printf 'Offline downloads and Xbox local delivery: configured (new installations default to disabled).\n'
printf 'Start Web/API with: docker compose -f '
quote_sh "$compose_file"
printf ' up -d\n'
printf 'Web UI: http://%s:%s\n' "$lan_ip" "$web_port"
printf 'New installations leave DNS and HTTP services stopped by default. Existing configuration is preserved, including its autoStart setting.\n'
if token=$(compose run --rm --entrypoint cat xdp /data/token 2>/dev/null); then
  printf 'Management token: %s\n' "$token"
else
  fail "initialized, but could not read token from the Compose data volume"
fi
