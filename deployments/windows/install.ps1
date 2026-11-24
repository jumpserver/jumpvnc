param(
  [Parameter(Mandatory=$true)][string]$Agent,
  [Parameter(Mandatory=$true)][string]$Config,
  [string]$TaskName='sshvnc-agent',
  [switch]$AtLogon,
  [switch]$Start,
  [switch]$NoTray
)
$ErrorActionPreference='Stop'
$Agent=(Resolve-Path -LiteralPath $Agent).Path
$Config=(Resolve-Path -LiteralPath $Config).Path
$launcher=Join-Path $PSScriptRoot 'run-agent.ps1'
$user=[Security.Principal.WindowsIdentity]::GetCurrent().Name
$arguments='-NoProfile -NonInteractive -WindowStyle Hidden -ExecutionPolicy RemoteSigned -File "'+$launcher+'" -Agent "'+$Agent+'" -Config "'+$Config+'"'
if ($NoTray) { $arguments+=' -NoTray' }
$action=New-ScheduledTaskAction -Execute 'powershell.exe' -Argument $arguments -WorkingDirectory $PSScriptRoot
$principal=New-ScheduledTaskPrincipal -UserId $user -LogonType Interactive -RunLevel Limited
$settings=New-ScheduledTaskSettingsSet -ExecutionTimeLimit ([TimeSpan]::Zero) -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1)
$task=@{TaskName=$TaskName; Action=$action; Principal=$principal; Settings=$settings; Description='JumpVNC Server current-user desktop and SFTP Agent'}
if ($AtLogon) { $task.Trigger=New-ScheduledTaskTrigger -AtLogOn -User $user }
# Do not silently replace an existing deployment. Stop/remove its task explicitly first.
Register-ScheduledTask @task | Out-Null
if ($Start) { Start-ScheduledTask -TaskName $TaskName }
