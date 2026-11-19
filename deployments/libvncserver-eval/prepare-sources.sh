#!/bin/sh
set -eu
base=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
fetch() {
  name=$1 url=$2 tag=$3 revision=$4
  if [ ! -d "$base/$name/.git" ]; then
    git clone --depth 1 --branch "$tag" "$url" "$base/$name"
  fi
  test "$(git -C "$base/$name" rev-parse HEAD)" = "$revision"
}
fetch libvncserver https://github.com/LibVNC/libvncserver.git LibVNCServer-0.9.15 9b54b1ec32731bd23158ca014dc18014db4194c3
fetch jpeg https://github.com/libjpeg-turbo/libjpeg-turbo.git 3.2.1 7b4993786d856cbccabfc0ed2f923ba000e326cc
fetch zlib https://github.com/madler/zlib.git v1.3.2 da607da739fa6047df13e66a2af6b8bec7c2a498
