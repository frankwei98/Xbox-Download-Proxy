#!/bin/sh
# Remote entry point; keep the checkout for Compose rebuilds.
set -eu

fail() { printf 'install: %s\n' "$*" >&2; exit 1; }

# Inspect setup options without changing their original argument boundaries.
read_options() {
  mode=
  explicit_binary=0
  help=0
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --mode)
        [ "$#" -ge 2 ] || fail '--mode requires a value'
        mode=$2; shift 2 ;;
      --binary)
        if [ "$#" -lt 2 ] || [ -z "$2" ]; then fail '--binary requires a path'; fi
        explicit_binary=1; shift 2 ;;
      --install-dir|--lan-ip|--web-port|--offline)
        [ "$#" -ge 2 ] || fail "$1 requires a value"
        shift 2 ;;
      --help|-h) help=1; shift ;;
      --yes|--start|--install-deps) shift ;;
      *) fail "unknown option: $1" ;;
    esac
  done
}

download_binary() {
  case "$(uname -s)/$(uname -m)" in
    Linux/x86_64|Linux/amd64) target=linux-amd64 ;;
    Linux/aarch64|Linux/arm64) target=linux-arm64 ;;
    Darwin/arm64|Darwin/aarch64) target=darwin-arm64 ;;
    *) fail "no supported Release binary for $(uname -s)/$(uname -m)" ;;
  esac
  if command -v sha256sum >/dev/null 2>&1; then
    checksum_tool=sha256sum
  elif command -v shasum >/dev/null 2>&1; then
    checksum_tool=shasum
  else
    fail 'SHA-256 verification needs sha256sum or shasum'
  fi
  release_base=https://github.com/frankwei98/Xbox-Download-Proxy/releases
  latest_url=$(curl -fsSL -o /dev/null -w '%{url_effective}' "$release_base/latest") ||
    fail 'cannot find the latest Release; check the network and published releases'
  case "$latest_url" in
    "$release_base/tag/"*) version=${latest_url#"$release_base/tag/"} ;;
    *) fail 'latest Release did not resolve to a release tag' ;;
  esac
  case "$version" in ''|*[!a-zA-Z0-9._-]*) fail 'invalid Release tag' ;; esac
  asset=xdp-$version-$target.tar.gz
  release_dir=$source_dir/release
  mkdir -p "$release_dir"
  printf 'Downloading Release %s (%s)\n' "$version" "$target"
  curl -fsSL "$release_base/download/$version/$asset" -o "$release_dir/$asset" ||
    fail "cannot download $asset; check that this Release includes your platform"
  curl -fsSL "$release_base/download/$version/$asset.sha256" -o "$release_dir/$asset.sha256" ||
    fail "cannot download checksum for $asset"
  # Read only the expected digest; never use remote checksum filenames as paths.
  expected=$(awk 'NR == 1 {print $1}' "$release_dir/$asset.sha256")
  [ "${#expected}" -eq 64 ] || fail 'invalid SHA-256 checksum'
  case "$expected" in *[!0-9a-fA-F]*) fail 'invalid SHA-256 checksum' ;; esac
  if [ "$checksum_tool" = sha256sum ]; then
    actual=$(sha256sum "$release_dir/$asset")
  else
    actual=$(shasum -a 256 "$release_dir/$asset")
  fi
  actual=${actual%% *}
  expected=$(printf '%s' "$expected" | tr 'A-F' 'a-f')
  [ "$actual" = "$expected" ] || fail "SHA-256 verification failed for $asset"
  tar -xzf "$release_dir/$asset" -C "$release_dir" xdp || fail 'Release archive is missing xdp'
  if [ ! -f "$release_dir/xdp" ] || [ -L "$release_dir/xdp" ]; then fail 'Release xdp must be a regular file'; fi
  chmod 755 "$release_dir/xdp"
}

main() {
  for required in curl tar mktemp; do
    command -v "$required" >/dev/null 2>&1 || {
      printf 'install: %s is required\n' "$required" >&2
      exit 1
    }
  done

  read_options "$@"
  if [ "$help" -eq 0 ]; then
    if [ -z "$mode" ]; then
      mode=native
      if [ -t 0 ] && [ -t 1 ]; then
        printf 'Installation mode [native/compose] (native): '
        IFS= read -r answer || true
        mode=${answer:-native}
      fi
      set -- "$@" --mode "$mode"
    fi
    case "$mode" in native|compose) ;; *) fail 'mode must be native or compose' ;; esac
  fi

  source_parent=${HOME:?HOME is not set}/.local/share/xdp-sources
  mkdir -p "$source_parent"
  source_dir=$(mktemp -d "$source_parent/source.XXXXXX")
  # Remove incomplete downloads, but retain a complete checkout even if setup
  # fails, so users can inspect it or retry with different options.
  complete=0
  trap 'if [ "$complete" -eq 0 ]; then rm -rf "$source_dir"; fi' EXIT
  trap 'exit 1' HUP INT TERM
  printf 'Downloading sources to %s\n' "$source_dir"
  curl -fsSL https://codeload.github.com/frankwei98/Xbox-Download-Proxy/tar.gz/refs/heads/main \
    -o "$source_dir/source.tar.gz" || {
      printf 'install: source download failed\n' >&2
      exit 1
    }
  tar -xzf "$source_dir/source.tar.gz" -C "$source_dir" --strip-components=1
  rm -f "$source_dir/source.tar.gz"
  if [ ! -f "$source_dir/setup.sh" ] || [ ! -f "$source_dir/go.mod" ]; then
    printf 'install: incomplete source archive\n' >&2
    exit 1
  fi
  complete=1

  if [ "$help" -eq 0 ] && [ "$mode" = native ] && [ "$explicit_binary" -eq 0 ]; then
    download_binary
    set -- "$@" --binary "$release_dir/xdp"
  fi
  sh "$source_dir/setup.sh" "$@"
}

# Reconnect before prompting for mode, and before setup prompts. Keeping all
# execution in a function ensures a piped script is parsed before reading tty.
if [ ! -t 0 ] && [ -t 1 ] && ( : < /dev/tty ) 2>/dev/null; then
  main "$@" < /dev/tty
else
  main "$@"
fi
