param([string]$Dependencies=$PSScriptRoot, [string]$CMakeCommand='cmake')
$ErrorActionPreference='Stop'
$base=$PSScriptRoot.Replace('\','/')
$cmake=(Get-Command -Name $CMakeCommand -CommandType Application -ErrorAction Stop).Source
$deps=$Dependencies.Replace('\','/')
$env:PATH='C:\Program Files\NASM;'+$env:PATH
function Invoke-CMake {
  $saved=$ErrorActionPreference
  try {
    # Windows PowerShell wraps native stderr warnings as ErrorRecords.
    $ErrorActionPreference='Continue'
    & $cmake @args
    $code=$LASTEXITCODE
  } finally { $ErrorActionPreference=$saved }
  if ($code -ne 0) { throw "cmake failed ($code): $args" }
}
Invoke-CMake -S "$deps/zlib" -B "$base/build-zlib" -G 'Visual Studio 17 2022' -A x64 -DCMAKE_INSTALL_PREFIX="$base/deps" -DZLIB_BUILD_EXAMPLES=OFF
Invoke-CMake --build "$base/build-zlib" --config Release --target INSTALL --parallel 8
Invoke-CMake -S "$deps/jpeg" -B "$base/build-jpeg" -G 'Visual Studio 17 2022' -A x64 -DCMAKE_INSTALL_PREFIX="$base/deps" -DENABLE_SHARED=OFF -DENABLE_STATIC=ON -DWITH_TURBOJPEG=OFF -DWITH_SIMD=ON -DWITH_CRT_DLL=ON -DWITH_TOOLS=OFF -DWITH_TESTS=OFF
Invoke-CMake --build "$base/build-jpeg" --config Release --target INSTALL --parallel 8
Invoke-CMake -S $base -B "$base/build" -G 'Visual Studio 17 2022' -A x64 -DSVNC_DEPENDENCIES="$deps" -DCMAKE_PREFIX_PATH="$base/deps" -DJPEG_LIBRARY="$base/deps/lib/jpeg-static.lib" -DZLIB_LIBRARY="$base/deps/lib/zs.lib" -DJPEG_INCLUDE_DIR="$base/deps/include" -DZLIB_INCLUDE_DIR="$base/deps/include"
Invoke-CMake --build "$base/build" --config Release --target svnc-desktop --parallel 8

# Keep matching dependency sources and licenses when distributing this GPL backend.
