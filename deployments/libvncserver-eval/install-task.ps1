$ErrorActionPreference='Stop'
$base=$PSScriptRoot
foreach($file in @('build\Release\libvnc-desktop.exe','vnc-password.txt','config.yaml','run-test.ps1','..\sshvnc-windows-amd64.exe')) {
  if(!(Test-Path -LiteralPath (Join-Path $base $file))) { throw "Missing prerequisite: $file" }
}
$user=[Security.Principal.WindowsIdentity]::GetCurrent().Name
$action=New-ScheduledTaskAction -Execute 'powershell.exe' -Argument "-NoProfile -NonInteractive -WindowStyle Hidden -ExecutionPolicy RemoteSigned -File `"$base\run-test.ps1`"" -WorkingDirectory $base
$principal=New-ScheduledTaskPrincipal -UserId $user -LogonType Interactive -RunLevel Limited
$settings=New-ScheduledTaskSettingsSet -ExecutionTimeLimit ([TimeSpan]::Zero) -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries
Register-ScheduledTask -TaskName 'svnc-libvncserver-test' -Action $action -Principal $principal -Settings $settings -Description 'Manual LibVNCServer DXGI desktop evaluation on loopback ports 2223/5902' -Force | Out-Null
Start-ScheduledTask -TaskName 'svnc-libvncserver-test'
