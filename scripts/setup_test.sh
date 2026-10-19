#!/usr/bin/env bash
set -eu

repo=$(CDPATH='' cd -- "$(dirname "$0")/.." && pwd -P)
tmp=$(mktemp -d "${TMPDIR:-/tmp}/xdp-setup-test.XXXXXX")
tmp=$(CDPATH='' cd -- "$tmp" && pwd -P)
trap 'rm -rf "$tmp"' EXIT HUP INT TERM
runner=$tmp/repo
mkdir -p "$runner"
cp "$repo/setup.sh" "$repo/Dockerfile" "$runner/"
bin=$tmp/bin
mkdir -p "$bin"
export MOCK_LOG=$tmp/mock.log
export MOCK_TOKEN=preserved-token

cat > "$bin/go" <<'EOF'
#!/usr/bin/env bash
set -eu
out=
while [ "$#" -gt 0 ]; do
  if [ "$1" = -o ]; then out=$2; shift 2; else shift; fi
done
[ -n "$out" ] || exit 90
cat > "$out" <<'BIN'
#!/usr/bin/env bash
set -eu
printf 'xdp' >> "$MOCK_LOG"
for arg in "$@"; do printf '\t%s' "$arg" >> "$MOCK_LOG"; done
printf '\n' >> "$MOCK_LOG"
if [ "${1-}" = init ]; then
  data_dir=
  offline=
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --data-dir) data_dir=$2; shift 2 ;;
      --offline=*) offline=${1#--offline=}; shift ;;
      *) shift ;;
    esac
  done
  mkdir -p "$data_dir"
  if [ ! -f "$data_dir/token" ]; then printf '%s\n' "$MOCK_TOKEN" > "$data_dir/token"; fi
  if [ -n "$offline" ]; then printf '%s\n' "$offline" > "$data_dir/offline";
  elif [ ! -f "$data_dir/offline" ]; then printf 'false\n' > "$data_dir/offline"; fi
fi
BIN
chmod +x "$out"
EOF

cat > "$bin/docker" <<'EOF'
#!/usr/bin/env bash
set -eu
printf 'docker' >> "$MOCK_LOG"
for arg in "$@"; do printf '\t%s' "$arg" >> "$MOCK_LOG"; done
printf '\n' >> "$MOCK_LOG"
case "${1-} ${2-}" in
  'compose version') exit 0 ;;
esac
for arg in "$@"; do
  if [ "$arg" = cat ]; then printf '%s\n' "$MOCK_TOKEN"; exit 0; fi
  if [ "$arg" = build ] && [ "${MOCK_DOCKER_FAIL-}" = 1 ]; then exit 42; fi
done
EOF
chmod +x "$bin/go" "$bin/docker"
export PATH="$bin:$PATH"

assert() { if ! "$@"; then printf 'FAIL: %s\n' "$*" >&2; exit 1; fi; }
contains() { case "$1" in *"$2"*) return 0 ;; *) return 1 ;; esac; }
quote_sh() { printf "'%s'" "$(printf '%s' "$1" | sed "s/'/'\\\\''/g")"; }

# Native supports hostile-looking literal paths, preserves initialized token on updates,
# and the generated launcher passes the original values as individual arguments.
native_dir="$tmp/xdp path 'quoted' \$(echo should-not-run)"
"$runner/setup.sh" --mode native --install-dir "$native_dir" --lan-ip 192.168.1.25 --web-port 18080 --offline yes --yes > "$tmp/native.out"
assert test "$(cat "$native_dir/data/token")" = "$MOCK_TOKEN"
assert test "$(cat "$native_dir/data/offline")" = true
assert bash -n "$native_dir/start.sh"
assert contains "$(cat "$native_dir/start.sh")" "\$(echo should-not-run)"
assert contains "$(cat "$tmp/native.out")" "Start Web/API with: $(quote_sh "$native_dir/start.sh")"
assert contains "$(cat "$tmp/native.out")" "sudo $(quote_sh "$native_dir/start.sh")"
: > "$MOCK_LOG"
"$native_dir/start.sh"
assert contains "$(cat "$MOCK_LOG")" "serve"
assert contains "$(cat "$MOCK_LOG")" "--lan-ip"
assert contains "$(cat "$MOCK_LOG")" "192.168.1.25"
assert contains "$(cat "$MOCK_LOG")" ":18080"
"$runner/setup.sh" --mode native --install-dir "$native_dir" --lan-ip 192.168.1.25 --yes > "$tmp/native-again.out"
assert test "$(cat "$native_dir/data/token")" = "$MOCK_TOKEN"
assert test "$(cat "$native_dir/data/offline")" = true
"$runner/setup.sh" --mode native --install-dir "$tmp/native-offline-no" --lan-ip 192.168.1.25 --offline no --yes > "$tmp/native-offline-no.out"
assert test "$(cat "$tmp/native-offline-no/data/offline")" = false
if "$runner/setup.sh" --mode native --install-dir "$native_dir" --lan-ip 192.168.1.25 > "$tmp/refuse.out" 2>&1; then
  printf 'FAIL: native overwrite without --yes was accepted\n' >&2; exit 1
fi

# Compose writes literal-safe absolute mounts, initializes but does not start by default,
# and reads the secret through the container entrypoint without printing command logs.
compose_dir="$tmp/compose path 'quoted' \$(literal)"
: > "$MOCK_LOG"
"$runner/setup.sh" --mode compose --install-dir "$compose_dir" --lan-ip 10.0.0.9 --web-port 8081 --yes > "$tmp/compose.out"
assert contains "$(cat "$compose_dir/compose.yaml")" "source: "
assert contains "$(cat "$compose_dir/compose.yaml")" "''quoted'' \$\$(literal)/data'"
assert contains "$(cat "$compose_dir/compose.yaml")" "''quoted'' \$\$(literal)/data/downloads'"
assert contains "$(cat "$compose_dir/compose.yaml")" "network_mode: host"
assert contains "$(cat "$compose_dir/compose.yaml")" "NET_BIND_SERVICE"
assert contains "$(cat "$compose_dir/compose.yaml")" "\$\$(literal)"
if contains "$(cat "$compose_dir/compose.yaml")" "--start"; then printf 'FAIL: Compose default enables DNS/HTTP startup\n' >&2; exit 1; fi
log=$(cat "$MOCK_LOG")
assert contains "$log" $'compose\t-f'
assert contains "$log" $'run\t--rm\txdp\tinit'
if contains "$log" "--offline="; then printf 'FAIL: Compose default overrode existing offline setting\n' >&2; exit 1; fi
if contains "$log" $'up\t-d'; then printf 'FAIL: Compose started by default\n' >&2; exit 1; fi
assert contains "$(cat "$tmp/compose.out")" "Management token: $MOCK_TOKEN"
assert contains "$(cat "$tmp/compose.out")" "New installations leave DNS and HTTP services stopped by default"
assert contains "$(cat "$tmp/compose.out")" "Existing configuration is preserved, including its autoStart setting"
assert contains "$(cat "$tmp/compose.out")" "docker compose -f $(quote_sh "$compose_dir/compose.yaml") up -d"
"$runner/setup.sh" --mode compose --install-dir "$compose_dir" --lan-ip 10.0.0.9 --yes > "$tmp/compose-again.out"
assert contains "$(cat "$tmp/compose-again.out")" "Management token: $MOCK_TOKEN"
: > "$MOCK_LOG"
"$runner/setup.sh" --mode compose --install-dir "$tmp/compose-offline-yes" --lan-ip 10.0.0.9 --offline yes --yes > "$tmp/compose-offline-yes.out"
assert contains "$(cat "$MOCK_LOG")" "--offline=true"
: > "$MOCK_LOG"
"$runner/setup.sh" --mode compose --install-dir "$tmp/compose-offline-no" --lan-ip 10.0.0.9 --offline no --yes > "$tmp/compose-offline-no.out"
assert contains "$(cat "$MOCK_LOG")" "--offline=false"

# Explicit Compose startup starts only Web/API; invalid input and missing tooling fail cleanly.
: > "$MOCK_LOG"
"$runner/setup.sh" --mode compose --install-dir "$tmp/compose-start" --lan-ip 10.0.0.10 --start --yes > "$tmp/compose-start.out"
assert contains "$(cat "$MOCK_LOG")" $'up\t-d'
if "$runner/setup.sh" --mode native --install-dir "$tmp/invalid" --lan-ip 999.1.1.1 --yes > "$tmp/invalid.out" 2>&1; then
  printf 'FAIL: invalid IPv4 was accepted\n' >&2; exit 1
fi
for bad_ip in 0.0.0.0 224.0.0.1 240.1.2.3 255.255.255.255 192.168.001.2 192.168.1.2.; do
  if "$runner/setup.sh" --mode native --install-dir "$tmp/invalid-ip" --lan-ip "$bad_ip" --yes > "$tmp/invalid-ip.out" 2>&1; then
    printf 'FAIL: invalid IPv4 %s was accepted\n' "$bad_ip" >&2; exit 1
  fi
done
for bad_port in 0 00080 100000 53 80 443; do
  if "$runner/setup.sh" --mode native --install-dir "$tmp/invalid-port" --lan-ip 192.168.1.2 --web-port "$bad_port" --yes > "$tmp/invalid-port.out" 2>&1; then
    printf 'FAIL: invalid Web/API port %s was accepted\n' "$bad_port" >&2; exit 1
  fi
done
if "$runner/setup.sh" --mode native --install-dir "$tmp/native-start" --lan-ip 192.168.1.2 --start --yes > "$tmp/native-start.out" 2>&1; then
  printf 'FAIL: native --start was accepted\n' >&2; exit 1
fi
assert contains "$(cat "$tmp/native-start.out")" "only supported in Compose mode"
if "$runner/setup.sh" --mode native --install-dir "$runner" --lan-ip 192.168.1.2 --yes > "$tmp/repo-install.out" 2>&1; then
  printf 'FAIL: repository root was accepted as install directory\n' >&2; exit 1
fi
assert contains "$(cat "$tmp/repo-install.out")" "cannot be the repository root"
if PATH="/usr/bin:/bin" /bin/bash "$runner/setup.sh" --mode native --install-dir "$tmp/missing-go" --lan-ip 10.0.0.1 --yes > "$tmp/no-go.out" 2>&1; then
  printf 'FAIL: native setup succeeded without Go or a repository binary\n' >&2; exit 1
fi
assert contains "$(cat "$tmp/no-go.out")" "needs Go on PATH"
cp "$native_dir/xdp" "$runner/xdp"
PATH="/usr/bin:/bin" /bin/bash "$runner/setup.sh" --mode native --install-dir "$tmp/prebuilt" --lan-ip 10.0.0.1 --yes > "$tmp/prebuilt.out"
assert test "$(cat "$tmp/prebuilt/data/token")" = "$MOCK_TOKEN"
if MOCK_DOCKER_FAIL=1 "$runner/setup.sh" --mode compose --install-dir "$tmp/compose-failure" --lan-ip 10.0.0.1 --yes > "$tmp/compose-failure.out" 2>&1; then
  printf 'FAIL: Compose build failure was ignored\n' >&2; exit 1
fi
assert contains "$(cat "$tmp/compose-failure.out")" "Docker Compose image build failed"

printf 'setup.sh tests passed\n'
