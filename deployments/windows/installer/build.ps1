param([string]$Version = '0.1.1')
$ErrorActionPreference = 'Stop'
$root = Split-Path (Split-Path (Split-Path $PSScriptRoot -Parent) -Parent) -Parent
$compiler = Join-Path ${env:ProgramFiles(x86)} 'Inno Setup 6\ISCC.exe'
if (-not (Test-Path -LiteralPath $compiler)) { throw 'Inno Setup 6 is required to build the Windows installer.' }
$prerequisites = Join-Path $root 'dist\prerequisites'
New-Item -ItemType Directory -Force $prerequisites | Out-Null
$redist = Join-Path $prerequisites 'vc_redist.x64.exe'
Invoke-WebRequest https://aka.ms/vs/17/release/vc_redist.x64.exe -OutFile $redist
$signature = Get-AuthenticodeSignature -LiteralPath $redist
if ($signature.Status -ne 'Valid' -or $signature.SignerCertificate.Subject -notmatch 'O=Microsoft Corporation') {
  throw 'The bundled Visual C++ runtime must have a valid Microsoft signature.'
}
& $compiler "/DAppVersion=$Version" "/DPackageDir=$(Join-Path $root 'dist\jumpvnc-server-windows-amd64')" "/DRedistPath=$redist" (Join-Path $PSScriptRoot 'jumpvnc.iss')
if ($LASTEXITCODE -ne 0) { throw 'Windows installer compilation failed.' }
$installer = Join-Path $root 'dist\jumpvnc-server-windows-amd64-setup.exe'
$hash = (Get-FileHash -LiteralPath $installer -Algorithm SHA256).Hash.ToLowerInvariant()
"$hash  $(Split-Path $installer -Leaf)" | Set-Content "$installer.sha256" -Encoding ascii
