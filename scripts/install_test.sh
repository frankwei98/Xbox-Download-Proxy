#!/bin/sh
set -eu

repo=$(CDPATH='' cd -- "$(dirname "$0")/.." && pwd -P)
tmp=$(mktemp -d "${TMPDIR:-/tmp}/xdp-install-test.XXXXXX")
trap 'rm -rf "$tmp"' EXIT
trap 'exit 1' HUP INT TERM
mkdir -p "$tmp/bin" "$tmp/fixture/project" "$tmp/home with spaces"
cat > "$tmp/fixture/project/setup.sh" <<'EOF'
#!/bin/sh
printf '%s\n' "$@" > "$INSTALL_TEST_ARGS"
exit "${INSTALL_TEST_SETUP_STATUS:-0}"
EOF
touch "$tmp/fixture/project/go.mod"
tar -czf "$tmp/source.tar.gz" -C "$tmp/fixture" project
cat > "$tmp/bin/curl" <<'EOF'
#!/bin/sh
set -eu
[ "${INSTALL_TEST_DOWNLOAD_FAIL:-0}" -eq 0 ] || exit 22
[ "$1" = -fsSL ]
[ "$2" = https://codeload.github.com/frankwei98/Xbox-Download-Proxy/tar.gz/refs/heads/main ]
[ "$3" = -o ]
cp "$INSTALL_TEST_ARCHIVE" "$4"
EOF
chmod +x "$tmp/bin/curl"
export PATH="$tmp/bin:$PATH"
export INSTALL_TEST_ARGS="$tmp/args"
export INSTALL_TEST_ARCHIVE="$tmp/source.tar.gz"

# Match the documented pipeline; preserve each argument and retain the sources.
cat "$repo/install.sh" | HOME="$tmp/home with spaces" sh -s -- \
  --mode native --lan-ip 192.168.1.10 --binary '/path with spaces/xdp' > "$tmp/out"
printf '%s\n' --mode native --lan-ip 192.168.1.10 --binary '/path with spaces/xdp' > "$tmp/expected"
cmp "$tmp/args" "$tmp/expected"
set -- "$tmp/home with spaces/.local/share/xdp-sources"/source.*
[ "$#" -eq 1 ] && [ -f "$1/setup.sh" ] && [ ! -e "$1/source.tar.gz" ]

# A setup error must propagate and leave sources available for a retry.
if HOME="$tmp/setup-fail" INSTALL_TEST_SETUP_STATUS=37 sh "$repo/install.sh" > "$tmp/out"; then
  printf 'FAIL: setup failure was ignored\n' >&2; exit 1
else
  [ "$?" -eq 37 ]
fi
set -- "$tmp/setup-fail/.local/share/xdp-sources"/source.*
[ -f "$1/setup.sh" ]

# Download failures and incomplete archives must not run setup or leave sources.
rm "$tmp/args"
if HOME="$tmp/download-fail" INSTALL_TEST_DOWNLOAD_FAIL=1 sh "$repo/install.sh" > "$tmp/out" 2>&1; then
  printf 'FAIL: download failure was ignored\n' >&2; exit 1
fi
[ ! -e "$tmp/args" ]
set -- "$tmp/download-fail/.local/share/xdp-sources"/source.*
[ ! -e "$1" ]
rm "$tmp/fixture/project/go.mod"
tar -czf "$tmp/incomplete.tar.gz" -C "$tmp/fixture" project
if HOME="$tmp/incomplete" INSTALL_TEST_ARCHIVE="$tmp/incomplete.tar.gz" sh "$repo/install.sh" > "$tmp/out" 2>&1; then
  printf 'FAIL: incomplete archive was accepted\n' >&2; exit 1
fi
[ ! -e "$tmp/args" ]
set -- "$tmp/incomplete/.local/share/xdp-sources"/source.*
[ ! -e "$1" ]
printf 'Remote installer tests passed\n'
