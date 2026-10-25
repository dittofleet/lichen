#!/bin/sh
# Build lichen from this checkout and install it as a dev build. Restarts
# the daemon if one is set up. For first-time setup from source, run this
# first, then `lichen setup <sync repo git url>`.
set -eu

DEST="${LICHEN_INSTALL_DIR:-$HOME/.local/bin}"
SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)

grep -q '^module lichen$' "$SCRIPT_DIR/go.mod" 2>/dev/null \
  || { echo "dev.sh must be run from a lichen checkout" >&2; exit 1; }
command -v go >/dev/null 2>&1 \
  || { echo "go is required to build from source" >&2; exit 1; }

mkdir -p "$DEST"
echo "Building lichen from source (dev build)..." >&2
(cd "$SCRIPT_DIR" && go build -o "$DEST/lichen" .)

# macOS keys permission grants (TCC, keychain) to the code-signing
# identity, and ad-hoc identities change every build. Re-signing with a
# stable Developer ID keeps those grants across rebuilds. Release
# binaries never come through here, so notarization is not at risk.
if [ "$(uname -s)" = Darwin ]; then
  IDENTITY=$(security find-identity -v -p codesigning 2>/dev/null | sed -n 's/.*"\(Developer ID Application: [^"]*\)".*/\1/p' | head -1)
  if [ -n "$IDENTITY" ]; then
    codesign --force --sign "$IDENTITY" --identifier dev.lichen --options runtime "$DEST/lichen" 2>/dev/null \
      && echo "Signed with: $IDENTITY" >&2 || true
  fi
fi

echo "Installed $DEST/lichen" >&2
if "$DEST/lichen" restart >/dev/null 2>&1; then
  echo "Daemon restarted" >&2
else
  echo "No daemon set up. First-time setup: lichen setup <sync repo git url>" >&2
fi
