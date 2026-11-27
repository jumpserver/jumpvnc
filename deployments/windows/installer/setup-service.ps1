param([switch]$NoStartService, [switch]$EnableWindowsLogin, [switch]$ManagedAccount)
$ErrorActionPreference = 'Stop'
$root = Split-Path (Split-Path (Split-Path $PSScriptRoot -Parent) -Parent) -Parent
$agent = Join-Path $root 'bin\jumpvnc.exe'
$config = Join-Path $root 'service.yaml'
& $agent -initialize-service -config $config *>&1 | Tee-Object -FilePath (Join-Path $root 'setup.log') -Append
if ($LASTEXITCODE -ne 0) { throw 'Cannot initialize JumpVNC. See setup.log in the installation directory.' }
if (-not (Get-Service SuperVNC -ErrorAction SilentlyContinue)) {
  & (Join-Path $root 'deployments\windows\install.ps1') -Agent $agent -Config $config
} else {
  $command = '"{0}" -service -config "{1}"' -f $agent, $config
  $service = Get-CimInstance Win32_Service -Filter "Name='SuperVNC'"
  $changed = Invoke-CimMethod -InputObject $service -MethodName Change -Arguments @{PathName=$command}
  if ($changed.ReturnValue -ne 0) { throw 'Cannot update the JumpVNC service executable.' }
  Set-Service SuperVNC -StartupType Automatic
  & (Join-Path $root 'deployments\windows\install-service-tray.ps1') -Agent $agent
}
Get-ChildItem -LiteralPath $root -Filter 'unins*.exe' | ForEach-Object {
  & icacls.exe $_.FullName /grant '*S-1-5-32-545:RX' | Out-Null
  if ($LASTEXITCODE -ne 0) { throw 'Cannot grant uninstaller execution permission.' }
}
Get-NetFirewallRule -Name 'JumpVNC.Server' -ErrorAction SilentlyContinue | Remove-NetFirewallRule
New-NetFirewallRule -Name 'JumpVNC.Server' -DisplayName 'JumpVNC Server' -Direction Inbound -Action Allow -Protocol TCP -Program $agent -Service SuperVNC | Out-Null
if ($EnableWindowsLogin -or $ManagedAccount) {
  & (Join-Path $PSScriptRoot 'set-windows-login.ps1') -Action Enable
} else {
  & (Join-Path $PSScriptRoot 'set-windows-login.ps1') -Action Disable
}
if ($ManagedAccount) {
  & (Join-Path $PSScriptRoot 'set-managed-account.ps1') -Action Enable
} else {
  & (Join-Path $PSScriptRoot 'set-managed-account.ps1') -Action Disable
}
if (-not $NoStartService) { Start-Service SuperVNC }
