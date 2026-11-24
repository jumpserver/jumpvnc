#!/bin/sh
set -eu
base=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
base=${SVNC_DEPENDENCIES:-$base}
revision=9b54b1ec32731bd23158ca014dc18014db4194c3
mkdir -p "$base"
if [ ! -d "$base/libvncserver/.git" ]; then
  git clone --depth 1 --branch LibVNCServer-0.9.15 https://github.com/LibVNC/libvncserver.git "$base/libvncserver"
fi
test "$(git -C "$base/libvncserver" rev-parse HEAD)" = "$revision"
