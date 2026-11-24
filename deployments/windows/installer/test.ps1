param([Parameter(Mandatory=$true)][string]$Installer)
$ErrorActionPreference = 'Stop'
$Installer = (Resolve-Path -LiteralPath $Installer).Path
function Install-JumpVNC {
  $process = Start-Process -FilePath $Installer -ArgumentList '/VERYSILENT', '/SUPPRESSMSGBOXES', '/NORESTART', '/NoStartService=1' -Wait -PassThru
  if ($process.ExitCode -ne 0) { throw "Installer failed: $($process.ExitCode)" }
}
Install-JumpVNC
$root = Join-Path $env:ProgramFiles 'SuperVNC'
$service = Get-CimInstance Win32_Service -Filter "Name='SuperVNC'"
if (-not $service -or $service.StartMode -ne 'Auto' -or $service.State -ne 'Stopped') { throw 'Incorrect service registration.' }
if (-not (Get-NetFirewallRule -Name 'JumpVNC.Server').Enabled) { throw 'Missing firewall rule.' }
$files = @('service.yaml', 'ssh_host_ed25519_key', 'client_ed25519_key', 'authorized_keys', 'vnc-password.txt')
$before = @{}
foreach ($file in $files) {
  $path = Join-Path $root $file
  $before[$file] = (Get-FileHash -LiteralPath $path).Hash
  $sddl = (Get-Acl -LiteralPath $path).Sddl
  if ($sddl -match ';;;BU\)') { throw "Users can access private configuration: $file" }
}
# A second installation must preserve every identity and setting.
Install-JumpVNC
foreach ($file in $files) {
  if ((Get-FileHash -LiteralPath (Join-Path $root $file)).Hash -ne $before[$file]) { throw "Upgrade replaced $file" }
}
$env:JUMPVNC_TEST_INSTALLED_PANEL = '1'
$env:JUMPVNC_PANEL_SCREENSHOT = Join-Path $PWD 'dist\jumpvnc-management.png'
go test ./cmd/server -run TestInstalledManagementWindow -count=1 -v
if ($LASTEXITCODE -ne 0) { throw 'Installed management window check failed.' }
$uninstaller = Get-ChildItem -LiteralPath $root -Filter 'unins*.exe' | Select-Object -First 1
$process = Start-Process -FilePath $uninstaller.FullName -ArgumentList '/VERYSILENT', '/SUPPRESSMSGBOXES', '/NORESTART' -Wait -PassThru
if ($process.ExitCode -ne 0) { throw 'Uninstaller failed.' }
if (Get-Service SuperVNC -ErrorAction SilentlyContinue) { throw 'Uninstaller left the service registered.' }
if (Get-NetFirewallRule -Name 'JumpVNC.Server' -ErrorAction SilentlyContinue) { throw 'Uninstaller left the firewall rule.' }
foreach ($file in $files) {
  if ((Get-FileHash -LiteralPath (Join-Path $root $file)).Hash -ne $before[$file]) { throw "Uninstaller removed user configuration: $file" }
}
