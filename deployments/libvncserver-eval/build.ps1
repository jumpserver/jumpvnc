$ErrorActionPreference='Continue'
$base=$PSScriptRoot.Replace('\','/')
$cmake='C:\Program Files (x86)\Microsoft Visual Studio\2022\BuildTools\Common7\IDE\CommonExtensions\Microsoft\CMake\CMake\bin\cmake.exe'
$env:PATH='C:\Program Files\NASM;'+$env:PATH
function CMake { & $cmake @args; if ($LASTEXITCODE -ne 0) { throw "cmake failed: $args" } }
CMake -S "$base/zlib" -B "$base/build-zlib" -A x64 -DCMAKE_INSTALL_PREFIX="$base/deps" -DZLIB_BUILD_EXAMPLES=OFF
CMake --build "$base/build-zlib" --config Release --target INSTALL --parallel 8
CMake -S "$base/jpeg" -B "$base/build-jpeg" -A x64 -DCMAKE_INSTALL_PREFIX="$base/deps" -DENABLE_SHARED=OFF -DENABLE_STATIC=ON -DWITH_TURBOJPEG=OFF -DWITH_SIMD=ON -DWITH_CRT_DLL=ON -DWITH_TOOLS=OFF -DWITH_TESTS=OFF
CMake --build "$base/build-jpeg" --config Release --target INSTALL --parallel 8
CMake -S $base -B "$base/build" -A x64 -DCMAKE_PREFIX_PATH="$base/deps" -DJPEG_LIBRARY="$base/deps/lib/jpeg-static.lib" -DZLIB_LIBRARY="$base/deps/lib/zs.lib" -DJPEG_INCLUDE_DIR="$base/deps/include" -DZLIB_INCLUDE_DIR="$base/deps/include"
CMake --build "$base/build" --config Release --target libvnc-desktop --parallel 8
