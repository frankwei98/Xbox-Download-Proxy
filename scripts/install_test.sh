#!/bin/sh
set -eu

repo=$(CDPATH='' cd -- "$(dirname "$0")/.." && pwd -P)
tmp=$(mktemp -d "${TMPDIR:-/tmp}/xdp-install-test.XXXXXX")
trap 'rm -rf "$tmp"' EXIT
trap 'exit 1' HUP INT TERM
mkdir -p "$tmp/bin" "$tmp/fixture/project" "$tmp/package" "$tmp/home with spaces"
cat > "$tmp/fixture/project/setup.sh" <<'SETUP'
#!/bin/sh
printf '%s\n' "$@" > "$INSTALL_TEST_ARGS"
exit "${INSTALL_TEST_SETUP_STATUS:-0}"
SETUP
touch "$tmp/fixture/project/go.mod"
tar -czf "$tmp/source.tar.gz" -C "$tmp/fixture" project
cat > "$tmp/package/xdp" <<'BINARY'
#!/bin/sh
set -eu
printf '%s\n' "$1" >> "$INSTALL_TEST_BINARY_LOG"
if [ "$1" = init ]; then
  shift
  [ "$1" = --data-dir ]
  mkdir -p "$2"
  printf 'release-test-token\n' > "$2/token"
  printf '{"webListen":":8080","lanIP":"192.168.1.20"}\n' > "$2/config.json"
fi
BINARY
chmod +x "$tmp/package/xdp"
tar -czf "$tmp/release.tar.gz" -C "$tmp/package" xdp
if command -v sha256sum >/dev/null 2>&1; then
  digest=$(sha256sum "$tmp/release.tar.gz")
else
  digest=$(shasum -a 256 "$tmp/release.tar.gz")
fi
export INSTALL_TEST_DIGEST="${digest%% *}"
cat > "$tmp/bin/curl" <<'CURL'
#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$INSTALL_TEST_CURL_LOG"
[ "${INSTALL_TEST_DOWNLOAD_FAIL:-0}" -eq 0 ] || exit 22
[ "$1" = -fsSL ]
shift
if [ "$1" = -o ]; then
  [ "${INSTALL_TEST_NO_RELEASE:-0}" -eq 0 ] || exit 22
  printf '%s' 'https://github.com/frankwei98/Xbox-Download-Proxy/releases/tag/v9.8.7'
  exit 0
fi
url=$1
[ "$2" = -o ]
out=$3
case "$url" in
  https://codeload.github.com/frankwei98/Xbox-Download-Proxy/tar.gz/refs/heads/main)
    cp "$INSTALL_TEST_ARCHIVE" "$out" ;;
  https://github.com/frankwei98/Xbox-Download-Proxy/releases/download/v9.8.7/*.sha256)
    digest=$INSTALL_TEST_DIGEST
    if [ "${INSTALL_TEST_BAD_CHECKSUM:-0}" -eq 1 ]; then digest=$(printf '%064d' 0); fi
    name=${url##*/}
    printf '%s  %s\n' "$digest" "${name%.sha256}" > "$out" ;;
  https://github.com/frankwei98/Xbox-Download-Proxy/releases/download/v9.8.7/*.tar.gz)
    [ "${INSTALL_TEST_MISSING_ASSET:-0}" -eq 0 ] || exit 22
    cp "$INSTALL_TEST_RELEASE" "$out" ;;
  *) printf 'Unexpected URL: %s\n' "$url" >&2; exit 90 ;;
esac
CURL
cat > "$tmp/bin/uname" <<'UNAME'
#!/bin/sh
case "$1" in
  -s) printf '%s\n' "${INSTALL_TEST_OS:-Linux}" ;;
  -m) printf '%s\n' "${INSTALL_TEST_ARCH:-x86_64}" ;;
esac
UNAME
cat > "$tmp/bin/go" <<'GO'
#!/bin/sh
printf 'FAIL: Release installation invoked Go\n' >&2
exit 77
GO
# Keep the simulated Linux installer independent of host privilege helpers.
printf '#!/bin/sh\nprintf "0\n"\n' > "$tmp/bin/id"
chmod +x "$tmp/bin/curl" "$tmp/bin/uname" "$tmp/bin/go" "$tmp/bin/id"
export PATH="$tmp/bin:$PATH"
export INSTALL_TEST_ARGS="$tmp/args"
export INSTALL_TEST_ARCHIVE="$tmp/source.tar.gz"
export INSTALL_TEST_RELEASE="$tmp/release.tar.gz"
export INSTALL_TEST_CURL_LOG="$tmp/curl.log"
export INSTALL_TEST_BINARY_LOG="$tmp/binary.log"

# Preserve explicit binary argument boundaries and bypass Release downloads.
# Keep a pipe to simulate curl supplying stdin.
# shellcheck disable=SC2002
cat "$repo/install.sh" | HOME="$tmp/home with spaces" sh -s -- \
  --mode native --lan-ip 192.168.1.10 --binary '/path with spaces/xdp' > "$tmp/out"
printf '%s\n' --mode native --lan-ip 192.168.1.10 --binary '/path with spaces/xdp' > "$tmp/expected"
cmp "$tmp/args" "$tmp/expected"
set -- "$tmp/home with spaces/.local/share/xdp-sources"/source.*
[ "$#" -eq 1 ] && [ -f "$1/setup.sh" ] && [ ! -e "$1/source.tar.gz" ]
# No rg/grep dependency: the log contains exactly the single source request.
[ "$(wc -l < "$tmp/curl.log" | tr -d ' ')" -eq 1 ]

# Automatic selection covers Linux aliases and macOS; native is also the headless default.
for platform in Linux:x86_64:linux-amd64 Linux:amd64:linux-amd64 Linux:aarch64:linux-arm64 Linux:arm64:linux-arm64 Darwin:arm64:darwin-arm64; do
  os=${platform%%:*}
  rest=${platform#*:}
  arch=${rest%%:*}
  target=${rest#*:}
  : > "$tmp/curl.log"
  HOME="$tmp/$target-$arch" INSTALL_TEST_OS="$os" INSTALL_TEST_ARCH="$arch" \
    sh "$repo/install.sh" --lan-ip 192.168.1.10 > "$tmp/out"
  case "$(cat "$tmp/curl.log")" in *"xdp-v9.8.7-$target.tar.gz.sha256"*) ;; *) exit 1 ;; esac
  set -- "$tmp/$target-$arch/.local/share/xdp-sources"/source.*
  printf '%s\n' --lan-ip 192.168.1.10 --mode native --binary "$1/release/xdp" > "$tmp/expected"
  cmp "$tmp/args" "$tmp/expected"
done

# Compose and --help never fetch a native Release.
for option in compose help; do
  : > "$tmp/curl.log"
  if [ "$option" = compose ]; then set -- --mode compose --lan-ip 192.168.1.10; else set -- --help; fi
  HOME="$tmp/$option" sh "$repo/install.sh" "$@" > "$tmp/out"
  [ "$(wc -l < "$tmp/curl.log" | tr -d ' ')" -eq 1 ]
done

# A setup error propagates and leaves sources available for retry.
if HOME="$tmp/setup-fail" INSTALL_TEST_SETUP_STATUS=37 sh "$repo/install.sh" --mode compose > "$tmp/out"; then
  printf 'FAIL: setup failure was ignored\n' >&2; exit 1
else
  [ "$?" -eq 37 ]
fi
set -- "$tmp/setup-fail/.local/share/xdp-sources"/source.*
[ -f "$1/setup.sh" ]

# Each failure must stop before setup, without silently falling back to a Go build.
for failure in download no-release missing-asset bad-checksum unsupported; do
  rm -f "$tmp/args"
  set --
  case "$failure" in
    download) set -- INSTALL_TEST_DOWNLOAD_FAIL=1 ;;
    no-release) set -- INSTALL_TEST_NO_RELEASE=1 ;;
    missing-asset) set -- INSTALL_TEST_MISSING_ASSET=1 ;;
    bad-checksum) set -- INSTALL_TEST_BAD_CHECKSUM=1 ;;
    unsupported) set -- INSTALL_TEST_ARCH=armv7l ;;
  esac
  if env HOME="$tmp/$failure" "$@" sh "$repo/install.sh" --lan-ip 192.168.1.10 > "$tmp/out" 2>&1; then
    printf 'FAIL: %s was ignored\n' "$failure" >&2; exit 1
  fi
  [ ! -e "$tmp/args" ]
done
set -- "$tmp/download/.local/share/xdp-sources"/source.*
[ ! -e "$1" ]

# Incomplete source archives also stop before setup and are cleaned up.
rm "$tmp/fixture/project/go.mod"
tar -czf "$tmp/incomplete.tar.gz" -C "$tmp/fixture" project
if HOME="$tmp/incomplete" INSTALL_TEST_ARCHIVE="$tmp/incomplete.tar.gz" sh "$repo/install.sh" > "$tmp/out" 2>&1; then
  printf 'FAIL: incomplete archive was accepted\n' >&2; exit 1
fi
[ ! -e "$tmp/args" ]
set -- "$tmp/incomplete/.local/share/xdp-sources"/source.*
[ ! -e "$1" ]

# Exercise the real setup.sh with a downloaded binary, even when Go is present
# but cannot run. A successful install must initialize data and create launchers.
cp "$repo/setup.sh" "$tmp/fixture/project/setup.sh"
touch "$tmp/fixture/project/go.mod"
mkdir -p "$tmp/fixture/project/scripts"
cp "$repo/scripts/install-service.sh" "$tmp/fixture/project/scripts/"
tar -czf "$tmp/real-source.tar.gz" -C "$tmp/fixture" project
HOME="$tmp/real-home" INSTALL_TEST_ARCHIVE="$tmp/real-source.tar.gz" \
  sh "$repo/install.sh" --lan-ip 192.168.1.10 --autostart no --install-dir "$tmp/installed app" > "$tmp/out"
[ "$(cat "$tmp/installed app/data/token")" = release-test-token ]
[ -x "$tmp/installed app/xdp" ]
[ -x "$tmp/installed app/start.sh" ]
[ -f "$tmp/installed app/xdp.service" ]
printf 'Remote installer tests passed\n'
