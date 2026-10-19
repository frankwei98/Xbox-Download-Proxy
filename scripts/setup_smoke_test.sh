#!/bin/sh
# Run only in a disposable Debian/Alpine container: this test registers xdp in /etc.
set -eu
[ "${XDP_SETUP_TEST_CONTAINER:-}" = 1 ] || { printf 'Run this test in a disposable container with XDP_SETUP_TEST_CONTAINER=1\n' >&2; exit 1; }
if [ "${XDP_TEST_SOURCE_BUILD:-0}" != 1 ]; then
  : "${XDP_TEST_BINARY:?Set XDP_TEST_BINARY to a matching Linux xdp binary}"
fi
repo=$(CDPATH='' cd -- "$(dirname "$0")/.." && pwd -P)
tmp=$(mktemp -d /tmp/xdp-setup-smoke.XXXXXX)
foreground=
cleanup() {
  if [ -n "$foreground" ]; then kill "$foreground" 2>/dev/null || true; wait "$foreground" 2>/dev/null || true; fi
  if [ -f /etc/init.d/xdp ]; then rc-service xdp stop >/dev/null 2>&1 || true; fi
  rm -rf "$tmp"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM
install_dir="$tmp/install 'quoted' %n \"double\" \$literal"
set --
if [ "${XDP_TEST_SOURCE_BUILD:-0}" != 1 ]; then set -- --binary "$XDP_TEST_BINARY"; fi
sh "$repo/setup.sh" --mode native "$@" --install-deps \
  --install-dir "$install_dir" --lan-ip 192.168.1.20 --web-port 18080 --offline no > "$tmp/install.log"
# Later updates also exercise copying over a live executable from its own path.
XDP_TEST_BINARY=$install_dir/xdp
test -s "$install_dir/data/token"
test ! -e "$install_dir/data/downloads"
test -x "$install_dir/install-service.sh"
sh -n "$install_dir/start.sh" "$install_dir/xdp.openrc"
cp "$install_dir/data/token" "$tmp/original-token"
"$install_dir/start.sh" --tls '' > "$tmp/web.log" 2>&1 &
foreground=$!
attempt=0
until "$install_dir/xdp" status --data-dir "$install_dir/data" > "$tmp/status.json" 2>/dev/null; do
  attempt=$((attempt + 1))
  if [ "$attempt" -ge 10 ]; then cat "$tmp/web.log"; exit 1; fi
  sleep 1
done
case "$(cat "$tmp/status.json")" in *'"running": false'*) ;; *) printf 'Fresh install unexpectedly started DNS/HTTP\n' >&2; exit 1 ;; esac
"$install_dir/xdp" start --data-dir "$install_dir/data" > "$tmp/start.json"
case "$(cat "$tmp/start.json")" in *'"running": true'*) ;; *) cat "$tmp/start.json"; exit 1 ;; esac

# Upgrade a live executable, preserving token/config. Linux rejects an in-place cp.
# serve persists its explicit listen/LAN overrides, so snapshot after startup.
cp "$install_dir/data/config.json" "$tmp/original-config"
sh "$repo/setup.sh" --mode native --binary "$XDP_TEST_BINARY" \
  --install-dir "$install_dir" --lan-ip 192.168.1.20 --web-port 18080 --yes > "$tmp/update.log"
cmp "$tmp/original-token" "$install_dir/data/token"
cmp "$tmp/original-config" "$install_dir/data/config.json"
kill "$foreground"
wait "$foreground"
foreground=

if command -v rc-service >/dev/null 2>&1; then
  # Exercise real OpenRC tools with a runtime fixture, without claiming a full VM boot.
  mkdir -p /run/openrc
  printf 'default\n' > /run/openrc/softlevel
  printf '\nrc_cgroup_mode="none"\n' >> /etc/rc.conf
  "$install_dir/install-service.sh" --manager auto --start > "$tmp/service.log" 2>&1 || { cat "$tmp/service.log"; exit 1; }
  rc-service xdp status
  test -L /etc/runlevels/default/xdp
  test "$(stat -c '%a' "$install_dir/logs/xdp.log")" = 600
  attempt=0
  until "$install_dir/xdp" status --data-dir "$install_dir/data" > "$tmp/service-status.json" 2>/dev/null; do
    attempt=$((attempt + 1))
    if [ "$attempt" -ge 10 ]; then cat "$install_dir/logs/xdp.log"; exit 1; fi
    sleep 1
  done
  case "$(cat "$tmp/service-status.json")" in *'"running": false'*) ;; *) cat "$tmp/service-status.json"; exit 1 ;; esac
  "$install_dir/xdp" start --data-dir "$install_dir/data" > /dev/null
  sh "$repo/setup.sh" --mode native --binary "$XDP_TEST_BINARY" \
    --install-dir "$install_dir" --lan-ip 192.168.1.20 --web-port 18080 --yes > "$tmp/service-upgrade.log"
  rc-service xdp restart
  rc-service xdp status
  rc-service xdp stop
  printf 'OpenRC registration, protected logs, actual start/stop/restart, live upgrade and API: passed\n'
elif command -v systemctl >/dev/null 2>&1; then
  # A systemd binary in a minimal container does not imply a running manager.
  test ! -d /run/systemd/system
  if "$install_dir/install-service.sh" --manager systemd --start > "$tmp/no-init.log" 2>&1; then
    printf 'Inactive systemd accepted --start\n' >&2; exit 1
  fi
  test ! -e /etc/systemd/system/xdp.service
  systemd-analyze verify "$install_dir/xdp.service"
  "$install_dir/install-service.sh" --manager auto > "$tmp/service.log" 2>&1 || { cat "$tmp/service.log"; exit 1; }
  systemctl is-enabled xdp.service
  printf 'systemd syntax, escaping, offline enable and inactive-init detection: passed\n'
fi
if "$install_dir/install-service.sh" > "$tmp/overwrite.log" 2>&1; then
  printf 'Service definition overwrite accepted without confirmation\n' >&2; exit 1
fi
"$install_dir/install-service.sh" --yes > "$tmp/service-update.log" 2>&1
printf 'Native install, authenticated API, low-port bind and live update: passed\n'
