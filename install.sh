#!/bin/sh
# Remote entry point; keep the checkout for Compose rebuilds.
set -eu

main() {
  for required in curl tar mktemp; do
    command -v "$required" >/dev/null 2>&1 || {
      printf 'install: %s is required\n' "$required" >&2
      exit 1
    }
  done

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

  # In `curl | sh`, stdin contains this script. Read prompts from the
  # controlling terminal instead; headless callers must pass setup options.
  if [ ! -t 0 ] && [ -t 1 ] && ( : < /dev/tty ) 2>/dev/null; then
    sh "$source_dir/setup.sh" "$@" < /dev/tty
  else
    sh "$source_dir/setup.sh" "$@"
  fi
}

main "$@"
