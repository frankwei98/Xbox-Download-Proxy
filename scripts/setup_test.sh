#!/bin/sh
set -eu

repo=$(CDPATH='' cd -- "$(dirname "$0")/.." && pwd -P)
tmp=$(mktemp -d "${TMPDIR:-/tmp}/xdp-setup-test.XXXXXX")
tmp=$(CDPATH='' cd -- "$tmp" && pwd -P)
trap 'rm -rf "$tmp"' EXIT HUP INT TERM
runner=$tmp/repo
mkdir -p "$runner"
cp "$repo/setup.sh" "$repo/Dockerfile" "$runner/"
mkdir -p "$runner/scripts"
cp "$repo/scripts/install-service.sh" "$runner/scripts/"
bin=$tmp/bin
mkdir -p "$bin"
export MOCK_LOG="$tmp/mock.log"
export MOCK_TOKEN=preserved-token

cat > "$bin/go" <<'EOF'
#!/bin/sh
set -eu
if [ "${1-}" = version ]; then printf 'go version %s linux/amd64\n' "${MOCK_GO_VERSION:-go1.24.0}"; exit 0; fi
[ "${MOCK_GO_FAIL:-0}" -eq 0 ] || exit 42
printf 'go build CGO_ENABLED=%s\n' "${CGO_ENABLED-}" >> "$MOCK_LOG"
out=
while [ "$#" -gt 0 ]; do
  if [ "$1" = -o ]; then out=$2; shift 2; else shift; fi
done
[ -n "$out" ] || exit 90
cat > "$out" <<'BIN'
#!/bin/sh
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
#!/bin/sh
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
assert sh -n "$native_dir/start.sh"
assert contains "$(cat "$native_dir/start.sh")" '#!/bin/sh'
assert contains "$(cat "$native_dir/start.sh")" "\$(echo should-not-run)"
assert contains "$(cat "$tmp/native.out")" "Start Web/API with: $(quote_sh "$native_dir/start.sh")"
assert contains "$(cat "$MOCK_LOG")" "go build CGO_ENABLED=0"
if [ "$(uname -s)" = Linux ]; then
  assert test -f "$native_dir/xdp.service"
  assert sh -n "$native_dir/xdp.openrc" "$native_dir/install-service.sh"
  # OpenRC evaluates command_args as shell words; verify literal argument boundaries.
  # shellcheck disable=SC2034,SC1091,SC2154
  (
    RC_SVCNAME=xdp
    . "$native_dir/xdp.openrc"
    assert test "$command" = /bin/sh
    eval "set -- $command_args"
    assert test "$#" = 1
    assert test "$1" = "$native_dir/start.sh"
    eval "set -- $output_log"
    assert test "$#" = 1
    assert test "$1" = "$native_dir/logs/xdp.log"
  )
fi
: > "$MOCK_LOG"
"$native_dir/start.sh" --tls ''
assert contains "$(cat "$MOCK_LOG")" "serve"
assert contains "$(cat "$MOCK_LOG")" "--lan-ip"
assert contains "$(cat "$MOCK_LOG")" "192.168.1.25"
assert contains "$(cat "$MOCK_LOG")" ":18080"
assert contains "$(cat "$MOCK_LOG")" "--tls"
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
tab=$(printf '\t')
assert contains "$log" "compose${tab}-f"
assert contains "$log" "run${tab}--rm${tab}xdp${tab}init"
if contains "$log" "--offline="; then printf 'FAIL: Compose default overrode existing offline setting\n' >&2; exit 1; fi
if contains "$log" "up${tab}-d"; then printf 'FAIL: Compose started by default\n' >&2; exit 1; fi
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
assert contains "$(cat "$MOCK_LOG")" "up${tab}-d"
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
# Build a PATH containing only the required utilities. Go may live in /usr/bin
# on Alpine/Debian; hiding it must not depend on the host's filesystem layout.
tools=$tmp/tools
mkdir -p "$tools"
for tool in cat chmod cp dirname id mkdir mktemp mv rm sed sh uname; do
  ln -s "$(command -v "$tool")" "$tools/$tool"
done
if PATH="$tools" /bin/sh "$runner/setup.sh" --mode native --install-dir "$tmp/missing-go" --lan-ip 10.0.0.1 --yes > "$tmp/no-go.out" 2>&1; then
  printf 'FAIL: native setup succeeded without Go or a repository binary\n' >&2; exit 1
fi
assert contains "$(cat "$tmp/no-go.out")" "needs Go >= 1.24 or a prebuilt xdp"
cp "$native_dir/xdp" "$runner/xdp"
PATH="$tools" /bin/sh "$runner/setup.sh" --mode native --install-dir "$tmp/prebuilt" --lan-ip 10.0.0.1 --yes > "$tmp/prebuilt.out"
assert test "$(cat "$tmp/prebuilt/data/token")" = "$MOCK_TOKEN"
if MOCK_DOCKER_FAIL=1 "$runner/setup.sh" --mode compose --install-dir "$tmp/compose-failure" --lan-ip 10.0.0.1 --yes > "$tmp/compose-failure.out" 2>&1; then
  printf 'FAIL: Compose build failure was ignored\n' >&2; exit 1
fi
assert contains "$(cat "$tmp/compose-failure.out")" "Docker Compose image build failed"

# Explicit prebuilt installs bypass even an old/broken Go, and failed builds or
# wrong-architecture executables must not replace a previously working binary.
: > "$MOCK_LOG"
MOCK_GO_VERSION=go1.19.8 "$runner/setup.sh" --mode native --binary "$native_dir/xdp" --install-dir "$tmp/explicit-binary" --lan-ip 10.0.0.1 > "$tmp/explicit.out"
if contains "$(cat "$MOCK_LOG")" 'go build'; then printf 'FAIL: --binary compiled sources\n' >&2; exit 1; fi
if MOCK_GO_VERSION=go1.23.12 "$runner/setup.sh" --mode native --install-dir "$tmp/old-go" --lan-ip 10.0.0.1 > "$tmp/old-go.out" 2>&1; then
  printf 'FAIL: old Go was accepted\n' >&2; exit 1
fi
assert contains "$(cat "$tmp/old-go.out")" 'Go >= 1.24 is required'
if MOCK_GO_FAIL=1 "$runner/setup.sh" --mode native --install-dir "$native_dir" --lan-ip 10.0.0.1 --yes > "$tmp/build-fail.out" 2>&1; then
  printf 'FAIL: build failure was ignored\n' >&2; exit 1
fi
assert test -x "$native_dir/xdp"
printf '#!/bin/sh\nexit 1\n' > "$tmp/bad-binary"
if "$runner/setup.sh" --mode native --binary "$tmp/bad-binary" --install-dir "$native_dir" --lan-ip 10.0.0.1 --yes > "$tmp/bad-binary.out" 2>&1; then
  printf 'FAIL: unusable binary was accepted\n' >&2; exit 1
fi
assert contains "$(cat "$tmp/bad-binary.out")" 'binary cannot run'
assert cmp "$native_dir/xdp" "$tmp/explicit-binary/xdp"
for option in --binary --install-deps; do
  set -- "$option"
  [ "$option" != --binary ] || set -- "$@" "$tmp/bad-binary"
  if "$runner/setup.sh" --mode compose "$@" --lan-ip 10.0.0.1 > "$tmp/native-only.out" 2>&1; then
    printf 'FAIL: native-only option was accepted for Compose\n' >&2; exit 1
  fi
  assert contains "$(cat "$tmp/native-only.out")" 'only supported in native mode'
done

# Isolated package-manager mocks prove dependency installation is opt-in and
# that a prebuilt install adds certificates without downloading a Go toolchain.
deps=$tmp/deps
mkdir -p "$deps"
for tool in "$tools"/*; do ln -s "$tool" "$deps/${tool##*/}"; done
rm "$deps/id" "$deps/uname"
cat > "$deps/id" <<'EOF'
#!/bin/sh
printf '%s\n' "${MOCK_UID:-0}"
EOF
printf '#!/bin/sh\nprintf "Linux\\n"\n' > "$deps/uname"
cat > "$deps/apk" <<'EOF'
#!/bin/sh
printf 'apk %s\n' "$*" >> "$MOCK_LOG"
case " $* " in
  *' go '*) cp "$MOCK_GO_BINARY" "$(dirname "$0")/go"; chmod +x "$(dirname "$0")/go" ;;
esac
EOF
chmod +x "$deps/id" "$deps/uname" "$deps/apk"
: > "$MOCK_LOG"
PATH="$deps" "$runner/setup.sh" --mode native --binary "$native_dir/xdp" --install-dir "$tmp/deps-none" --lan-ip 10.0.0.1 > "$tmp/deps-none.out"
if contains "$(cat "$MOCK_LOG")" 'apk '; then printf 'FAIL: packages installed without opt-in\n' >&2; exit 1; fi
PATH="$deps" "$runner/setup.sh" --mode native --binary "$native_dir/xdp" --install-deps --install-dir "$tmp/deps-apk" --lan-ip 10.0.0.1 > "$tmp/deps-apk.out"
assert contains "$(cat "$MOCK_LOG")" 'apk add --no-cache ca-certificates'
if contains "$(cat "$MOCK_LOG")" 'ca-certificates go'; then printf 'FAIL: prebuilt install added Go\n' >&2; exit 1; fi
if PATH="$deps" MOCK_UID=1000 "$runner/setup.sh" --mode native --binary "$native_dir/xdp" --install-deps --install-dir "$tmp/deps-nonroot" --lan-ip 10.0.0.1 > "$tmp/nonroot.out" 2>&1; then
  printf 'FAIL: non-root dependency install accepted\n' >&2; exit 1
fi
assert contains "$(cat "$tmp/nonroot.out")" 'requires root'
# An executable copied from another OS/architecture is not a usable fallback.
cp "$tmp/bad-binary" "$runner/xdp"
chmod +x "$runner/xdp"
: > "$MOCK_LOG"
PATH="$deps" MOCK_GO_BINARY="$bin/go" "$runner/setup.sh" --mode native --install-deps --install-dir "$tmp/deps-source" --lan-ip 10.0.0.1 > "$tmp/deps-source.out"
assert contains "$(cat "$MOCK_LOG")" 'apk add --no-cache ca-certificates go'
assert contains "$(cat "$MOCK_LOG")" 'go build CGO_ENABLED=0'
rm "$deps/go"
cp "$native_dir/xdp" "$runner/xdp"
rm "$deps/apk"
cat > "$deps/apt-get" <<'EOF'
#!/bin/sh
printf 'apt-get %s\n' "$*" >> "$MOCK_LOG"
EOF
chmod +x "$deps/apt-get"
: > "$MOCK_LOG"
PATH="$deps" "$runner/setup.sh" --mode native --binary "$native_dir/xdp" --install-deps --install-dir "$tmp/deps-apt" --lan-ip 10.0.0.1 > "$tmp/deps-apt.out"
assert contains "$(cat "$MOCK_LOG")" 'apt-get update'
assert contains "$(cat "$MOCK_LOG")" 'apt-get install -y --no-install-recommends ca-certificates'

if [ "$(uname -s)" = Linux ]; then
  weird_dir="$tmp/unit %n \"double\" \\slash \${UNSET}"
  "$runner/setup.sh" --mode native --binary "$native_dir/xdp" --install-dir "$weird_dir" --lan-ip 10.0.0.1 > "$tmp/unit.out"
  assert contains "$(cat "$weird_dir/xdp.service")" '%%n'
  assert contains "$(cat "$weird_dir/xdp.service")" "\$\${UNSET}"
  assert contains "$(cat "$weird_dir/xdp.service")" '\"double\"'
  assert contains "$(cat "$weird_dir/xdp.service")" '\\slash'
  assert sh -n "$weird_dir/xdp.openrc" "$weird_dir/install-service.sh"
fi

printf 'setup.sh tests passed\n'
