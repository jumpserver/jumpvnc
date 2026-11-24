param([Parameter(Mandatory=$true)][string]$Agent)
$ErrorActionPreference='Stop'
$principal=New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
if(-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
  throw 'Run this installer from an elevated PowerShell session.'
}
$Agent=(Resolve-Path -LiteralPath $Agent).Path
$service=Get-CimInstance Win32_Service -Filter "Name='SuperVNC'"
if(-not $service -or $service.PathName -notmatch '^"([^"]+)"' -or $Matches[1] -ne $Agent) {
  throw 'The tray executable must match the installed JumpVNC service executable.'
}
$launcher=(Resolve-Path (Join-Path $PSScriptRoot 'run-service-tray.ps1')).Path
$root=Join-Path ([Environment]::GetFolderPath('ProgramFiles')) 'SuperVNC'
if($Agent -ne (Join-Path $root 'bin\jumpvnc.exe') -or $launcher -ne (Join-Path $root 'deployments\windows\run-service-tray.ps1')) {
  throw 'Install the service and tray launcher in the protected JumpVNC installation directory first.'
}
# Let logged-in users execute the tray, while configuration, passwords and keys
# keep their existing administrator/SYSTEM ACLs. No inheritable user grant.
foreach($path in @($root,(Join-Path $root 'bin'),(Join-Path $root 'deployments'),$PSScriptRoot,$Agent,$launcher)) {
  & icacls.exe $path /grant '*S-1-5-32-545:RX' | Out-Null
  if($LASTEXITCODE -ne 0){throw "Cannot grant tray read/execute access: $path"}
}
$systemPowerShell=Join-Path ([Environment]::SystemDirectory) 'WindowsPowerShell\v1.0\powershell.exe'
$arguments='-NoProfile -NonInteractive -WindowStyle Hidden -ExecutionPolicy RemoteSigned -File "'+$launcher+'" -Agent "'+$Agent+'"'
$command='"'+$systemPowerShell+'" '+$arguments
# Standard per-user logon execution; no passwords or elevated scheduled task.
$run='HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Run'
New-ItemProperty -Path $run -Name 'JumpVNC Server' -PropertyType String -Value $command -Force | Out-Null
$folder=Join-Path ([Environment]::GetFolderPath('CommonPrograms')) 'JumpVNC'
New-Item -ItemType Directory -Path $folder -Force | Out-Null
$shell=New-Object -ComObject WScript.Shell
$shortcut=$shell.CreateShortcut((Join-Path $folder 'JumpVNC Server.lnk'))
$shortcut.TargetPath=$systemPowerShell
$shortcut.Arguments=$arguments+' -OpenPanel'
$shortcut.WorkingDirectory=Split-Path -Parent $Agent
$shortcut.IconLocation=$Agent+',0'
$shortcut.Description='Manage the JumpVNC Windows service'
$shortcut.Save()
Write-Output 'JumpVNC tray will start at user logon. Open JumpVNC Server from the Start menu to use it now.'
