#!/usr/bin/env bash
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
  --start                In Compose mode, start Web/API after initialization
  --yes                  Allow replacing generated files and binaries
  --help                 Show this help

Native mode builds current sources with Go, or uses an existing ./xdp if Go is unavailable. Compose mode needs
the Docker Compose plugin (`docker compose`). Compose initializes data but does
not start DNS/HTTP services; --start starts only the Web/API container.
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

while [ "$#" -gt 0 ]; do
  case "$1" in
    --mode) [ "$#" -ge 2 ] || fail "--mode requires a value"; mode=$2; shift 2 ;;
    --install-dir) [ "$#" -ge 2 ] || fail "--install-dir requires a value"; install_dir=$2; install_dir_set=1; shift 2 ;;
    --lan-ip) [ "$#" -ge 2 ] || fail "--lan-ip requires a value"; lan_ip=$2; shift 2 ;;
    --web-port) [ "$#" -ge 2 ] || fail "--web-port requires a value"; web_port=$2; web_port_set=1; shift 2 ;;
    --yes) yes=1; shift ;;
    --start) start=1; shift ;;
    --help|-h) usage; exit 0 ;;
    *) fail "unknown option: $1" ;;
  esac
done

if [ -z "$mode" ]; then
  [ "$has_tty" -eq 1 ] || fail "--mode is required without an interactive terminal"
  printf 'Installation mode [native/compose] (native): '
  IFS= read -r answer || true
  mode=${answer:-native}
fi
case "$mode" in native|compose) ;; *) fail "mode must be native or compose" ;; esac
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
[ "$first_octet" -ne 0 ] && [ "$first_octet" -ne 127 ] && [ "$first_octet" -lt 224 ] || fail "LAN IPv4 address cannot be unspecified, loopback, multicast, or reserved: $lan_ip"

if [ "$has_tty" -eq 1 ] && [ "$web_port_set" -eq 0 ]; then
  printf 'Web/API port [%s]: ' "$web_port"
  IFS= read -r answer || true
  web_port=${answer:-$web_port}
fi
case "$web_port" in ''|*[!0-9]*) fail "Web port must be an integer from 1 to 65535" ;; esac
[ "${#web_port}" -le 5 ] || fail "Web port must be an integer from 1 to 65535"
[ "${#web_port}" -eq 1 ] || [ "${web_port#0}" = "$web_port" ] || fail "Web port must not contain leading zeroes"
[ "$web_port" -ge 1 ] && [ "$web_port" -le 65535 ] || fail "Web port must be an integer from 1 to 65535"
case "$web_port" in 53|80|443) fail "Web port must not conflict with DNS/HTTP/TLS port $web_port" ;; esac

if [ "$has_tty" -eq 1 ] && [ "$mode" = compose ] && [ "$start" -eq 0 ]; then
  printf 'Start the Web/API service after setup? [y/N]: '
  IFS= read -r answer || true
  case "$answer" in y|Y|yes|YES) start=1 ;; esac
fi

script_dir=$(CDPATH='' cd -- "$(dirname "$0")" && pwd -P)
[ -n "$install_dir" ] || fail "installation directory must not be empty"
case "$install_dir" in /*) ;; *) install_dir=$(pwd -P)/$install_dir ;; esac
mkdir -p "$install_dir"
install_dir=$(CDPATH='' cd -- "$install_dir" && pwd -P)
[ "$install_dir" != "$script_dir" ] || fail "installation directory cannot be the repository root"
mkdir -p "$install_dir/data" "$install_dir/data/downloads"
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
  if command -v go >/dev/null 2>&1; then
    (cd "$script_dir" && go build -o "$binary" ./cmd/xdp) || fail "Go build failed"
  elif [ -x "$script_dir/xdp" ]; then
    cp "$script_dir/xdp" "$binary"
  else
    fail "native install needs Go on PATH or an executable xdp binary in the repository root"
  fi
  chmod 755 "$binary"
  "$binary" init --data-dir "$install_dir/data"

  {
    printf '#!/usr/bin/env bash\nset -eu\nexec '
    quote_sh "$binary"
    printf ' serve --data-dir '
    quote_sh "$install_dir/data"
    printf ' --lan-ip '
    quote_sh "$lan_ip"
    printf ' --web '
    quote_sh ":$web_port"
    printf '\n'
  } > "$start_script"
  chmod 755 "$start_script"
  printf '\nInstalled native xdp.\nStart Web/API with: '
  quote_sh "$start_script"
  printf '\n'
  printf 'Web UI: http://%s:%s\n' "$lan_ip" "$web_port"
  printf 'If low-port permissions prevent starting download services, stop the running Web/API (Ctrl+C), then retry manually with: sudo '
  quote_sh "$start_script"
  printf '\nSetup never runs sudo automatically.\n'
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
if [ "$(uname -s 2>/dev/null || true)" != Linux ]; then
  printf 'Compose host networking is intended mainly for Linux NAS; Docker Desktop on macOS may behave differently.\n'
fi
compose_file=$install_dir/compose.yaml
confirm_replace "$compose_file"

# YAML single quoted scalars protect YAML syntax; doubling dollars also prevents Compose interpolation.
yaml_quote() {
  value=$1
  escaped=
  i=0
  while [ "$i" -lt "${#value}" ]; do
    char=${value:$i:1}
    case "$char" in
      "'") escaped="$escaped''" ;;
      '$') escaped="${escaped}\$\$" ;;
      *) escaped="$escaped$char" ;;
    esac
    i=$((i + 1))
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
compose run --rm xdp init --data-dir /data || fail "Compose initialization failed"
if [ "$start" -eq 1 ]; then
  compose up -d || fail "Compose Web/API startup failed"
fi
printf '\nInstalled Compose setup.\n'
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
