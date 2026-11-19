#!/bin/sh
set -eu
base=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
mkdir -p "$base/build"
source_dir=${LIBVNC_SOURCE_DIR:-"$base/build/vendor/libvncserver"}
build_dir=${LIBVNC_BUILD_DIR:-"$base/build/libvncserver"}
if [ ! -f "$source_dir/CMakeLists.txt" ]; then
  git clone --depth 1 --branch LibVNCServer-0.9.15 https://github.com/LibVNC/libvncserver.git "$source_dir"
  test "$(git -C "$source_dir" rev-parse HEAD)" = 9b54b1ec32731bd23158ca014dc18014db4194c3
fi
jpeg_prefix=$(brew --prefix jpeg-turbo)
ssl_prefix=$(brew --prefix openssl@3)
cmake -S "$source_dir" -B "$build_dir" -DCMAKE_POLICY_VERSION_MINIMUM=3.5 -DCMAKE_BUILD_TYPE=Release -DBUILD_SHARED_LIBS=OFF -DWITH_EXAMPLES=OFF -DWITH_TESTS=OFF -DWITH_GNUTLS=OFF -DWITH_GCRYPT=OFF -DWITH_SASL=OFF -DWITH_SDL=OFF -DWITH_GTK=OFF -DWITH_QT=OFF -DWITH_FFMPEG=OFF -DWITH_XCB=OFF -DWITH_LIBSSHTUNNEL=OFF -DWITH_LZO=OFF -DWITH_SYSTEMD=OFF -DOPENSSL_ROOT_DIR="$ssl_prefix" -DJPEG_LIBRARY="$jpeg_prefix/lib/libjpeg.dylib" -DJPEG_INCLUDE_DIR="$jpeg_prefix/include"
cmake --build "$build_dir" --target vncclient -j 8
# No credentials are embedded in the program or app bundle.
c++ -std=c++17 -O2 -I"$source_dir/include" -I"$build_dir/include" "$base/latency.cpp" "$build_dir/libvncclient.a" -L"$jpeg_prefix/lib" -ljpeg -lz -L"$ssl_prefix/lib" -lssl -lcrypto -o "$base/build/latency"
# SDL flags intentionally split into compiler arguments, as emitted by pkg-config.
c++ -std=c++17 -O2 -I"$source_dir/include" -I"$build_dir/include" $(pkg-config --cflags sdl2) "$base/viewer.cpp" "$build_dir/libvncclient.a" $(pkg-config --libs sdl2) -L"$jpeg_prefix/lib" -ljpeg -lz -L"$ssl_prefix/lib" -lssl -lcrypto -o "$base/build/LibVNCClient"
app="$base/build/LibVNCClient Test.app"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
cp "$base/build/LibVNCClient" "$app/Contents/MacOS/LibVNCClient"
cp "$source_dir/COPYING" "$app/Contents/Resources/LICENSE-LibVNCServer.txt"
cat > "$app/Contents/Info.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleExecutable</key><string>LibVNCClient</string>
<key>CFBundleIdentifier</key><string>com.sshvnc.libvncclient-test</string>
<key>CFBundleName</key><string>LibVNCClient Test</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>NSHighResolutionCapable</key><true/>
</dict></plist>
PLIST
codesign --force --sign - "$app"
