#!/bin/sh
set -eu
base=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
mkdir -p "$base/vendor"
archive="$base/vendor/rustvncserver-2.2.1.crate"
if [ ! -f "$archive" ]; then
  curl -fL https://static.crates.io/crates/rustvncserver/rustvncserver-2.2.1.crate -o "$archive"
fi
printf '%s  %s\n' e01b2f51293e6d8f21ed834009e839439d250b8233f461ddb88f4a750a4e026e "$archive" | shasum -a 256 -c -
if [ ! -d "$base/vendor/rustvncserver" ]; then
  tar -xzf "$archive" -C "$base/vendor"
  mv "$base/vendor/rustvncserver-2.2.1" "$base/vendor/rustvncserver"
  # Isolation-only change: retain upstream auth/parser/encoding behavior.
  python3 - "$base/vendor/rustvncserver/src/server.rs" <<'PY'
import pathlib, sys
p=pathlib.Path(sys.argv[1]);s=p.read_text();old='TcpListener::bind(format!("0.0.0.0:{port}"))'
assert s.count(old)==1
p.write_text(s.replace(old, 'TcpListener::bind(format!("127.0.0.1:{port}"))'))
PY
fi
