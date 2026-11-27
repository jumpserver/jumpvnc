param([Parameter(Mandatory=$true)][string]$InstallRoot, [switch]$Uninstall)
$ErrorActionPreference = 'Stop'
$expectedRoot = Join-Path ([Environment]::GetFolderPath('ProgramFiles')) 'SuperVNC'
if (-not [string]::Equals($InstallRoot.TrimEnd('\'), $expectedRoot, [StringComparison]::OrdinalIgnoreCase)) {
  throw 'Unexpected installation directory.'
}
$agent = Join-Path $InstallRoot 'bin\jumpvnc.exe'
$legacyAgent = Join-Path $InstallRoot 'bin\sshvnc.exe'
$expectedCommand = '"{0}" -service -config "{1}"' -f $agent, (Join-Path $InstallRoot 'service.yaml')
$legacyCommand = '"{0}" -service -config "{1}"' -f $legacyAgent, (Join-Path $InstallRoot 'service.yaml')
$service = Get-CimInstance Win32_Service -Filter "Name='SuperVNC'"
if ($service) {
  if (-not [string]::Equals($service.PathName, $expectedCommand, [StringComparison]::OrdinalIgnoreCase) -and
      -not [string]::Equals($service.PathName, $legacyCommand, [StringComparison]::OrdinalIgnoreCase)) {
    throw 'The existing SuperVNC service has a different executable or configuration. Keep its installation unchanged.'
  }
  Stop-Service SuperVNC -ErrorAction Stop
  (Get-Service SuperVNC).WaitForStatus('Stopped', [TimeSpan]::FromSeconds(45))
}
# Only close managers belonging to this installation, leaving other programs alone.
Get-Process -Name jumpvnc, sshvnc -ErrorAction SilentlyContinue | Where-Object {
  $_.Path -and ([string]::Equals($_.Path, $agent, [StringComparison]::OrdinalIgnoreCase) -or [string]::Equals($_.Path, $legacyAgent, [StringComparison]::OrdinalIgnoreCase))
} | Stop-Process -Force -ErrorAction Stop
if ($Uninstall) {
  & (Join-Path $InstallRoot 'deployments\windows\installer\set-windows-login.ps1') -Action Disable
  if ($service) {
    & "$env:SystemRoot\System32\sc.exe" delete SuperVNC
    if ($LASTEXITCODE -ne 0) { throw 'Cannot remove JumpVNC service.' }
  }
  Remove-ItemProperty 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Run' -Name 'JumpVNC Server' -ErrorAction SilentlyContinue
  Get-NetFirewallRule -Name 'JumpVNC.Server' -ErrorAction SilentlyContinue | Remove-NetFirewallRule
  $shortcut = Join-Path ([Environment]::GetFolderPath('CommonPrograms')) 'JumpVNC\JumpVNC Server.lnk'
  Remove-Item -LiteralPath $shortcut -ErrorAction SilentlyContinue
}
