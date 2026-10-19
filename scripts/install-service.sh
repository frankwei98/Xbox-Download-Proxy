#!/bin/sh
# Copied beside the generated unit files by setup.sh. Registration is opt-in.
set -eu

fail() { printf 'install-service: %s\n' "$*" >&2; exit 1; }
usage() {
  cat <<'EOF'
Usage: install-service.sh [--manager auto|systemd|openrc] [--start] [--yes]

Run as root to register xdp and enable it at boot. --start also starts Web/API
now; a fresh installation still leaves DNS/HTTP/TLS stopped until enabled in WebUI.
--yes permits replacing an existing service definition. Stop any foreground xdp
before starting the service. No packages, DNS settings or firewall rules are changed.
EOF
}
manager=auto
start=0
yes=0
while [ "$#" -gt 0 ]; do
  case "$1" in
    --manager) [ "$#" -ge 2 ] || fail "--manager requires a value"; manager=$2; shift 2 ;;
    --start) start=1; shift ;;
    --yes) yes=1; shift ;;
    --help|-h) usage; exit 0 ;;
    *) fail "unknown option: $1" ;;
  esac
done
case "$manager" in auto|systemd|openrc) ;; *) fail "manager must be auto, systemd or openrc" ;; esac
[ "$(uname -s)" = Linux ] || fail "system services are supported on Linux only"
[ "$(id -u)" -eq 0 ] || fail "run explicitly as root using su, sudo or doas"
dir=$(CDPATH='' cd -- "$(dirname "$0")" && pwd -P)
if [ ! -x "$dir/xdp" ] || [ ! -x "$dir/start.sh" ]; then
  fail "run setup.sh first; keep this helper beside xdp and start.sh"
fi

# A minimal Docker container may have service tools without a running init.
if [ "$manager" = auto ]; then
  if [ -d /run/systemd/system ] && command -v systemctl >/dev/null 2>&1; then
    manager=systemd
  elif [ -f /run/openrc/softlevel ] && command -v rc-service >/dev/null 2>&1; then
    manager=openrc
  elif command -v rc-service >/dev/null 2>&1; then
    manager=openrc
  elif command -v systemctl >/dev/null 2>&1; then
    manager=systemd
  else
    fail "no supported service manager found; run start.sh in the foreground, or install the distribution's init system in your VM/LXC"
  fi
fi
active=0
case "$manager" in
  systemd)
    command -v systemctl >/dev/null 2>&1 || fail "systemctl is not installed"
    [ ! -d /run/systemd/system ] || active=1
    source_file=$dir/xdp.service
    target=/etc/systemd/system/xdp.service
    ;;
  openrc)
    if ! command -v rc-service >/dev/null 2>&1 || ! command -v rc-update >/dev/null 2>&1; then fail "OpenRC tools are not installed"; fi
    [ ! -f /run/openrc/softlevel ] || active=1
    source_file=$dir/xdp.openrc
    target=/etc/init.d/xdp
    ;;
esac
[ -r "$source_file" ] || fail "missing generated definition: $source_file"
if [ "$start" -eq 1 ] && [ "$active" -eq 0 ]; then
  fail "$manager is not running in this container; use start.sh in the foreground, or boot the VM/LXC with its init system (nothing was registered)"
fi
if [ -e "$target" ] || [ -L "$target" ]; then
  if [ "$yes" -eq 0 ]; then
    if [ -t 0 ] && [ -t 1 ]; then
      printf '%s exists. Replace it? [y/N]: ' "$target"
      IFS= read -r answer || true
      case "$answer" in y|Y|yes|YES) yes=1 ;; esac
    fi
    [ "$yes" -eq 1 ] || fail "refusing to replace $target (use --yes to update)"
  fi
fi
mkdir -p "$(dirname "$target")"
staged=$(mktemp "${target}.XXXXXX")
trap 'rm -f "$staged"' EXIT
trap 'exit 1' HUP INT TERM
cp "$source_file" "$staged"
case "$manager" in systemd) chmod 644 "$staged" ;; openrc) chmod 755 "$staged" ;; esac
mv -f "$staged" "$target"
case "$manager" in
  systemd)
    [ "$active" -eq 0 ] || systemctl daemon-reload
    systemctl enable xdp.service
    if [ "$start" -eq 1 ]; then systemctl start xdp.service; fi
    printf 'Enabled xdp.service. Logs: journalctl -u xdp\nManage: systemctl start|stop|restart|status xdp\n'
    ;;
  openrc)
    rc-update add xdp default
    if [ "$start" -eq 1 ]; then rc-service xdp start; fi
    printf 'Enabled OpenRC xdp. Logs: %s/logs/xdp.log\nManage: rc-service xdp start|stop|restart|status\n' "$dir"
    ;;
esac
if [ "$active" -eq 0 ]; then printf 'Init is not running here; the service will start on a normal system boot.\n'; fi
printf 'For an already running installation, restart xdp to load an updated binary or configuration.\n'
