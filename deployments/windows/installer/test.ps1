param([Parameter(Mandatory=$true)][string]$Installer)
$ErrorActionPreference = 'Stop'
$Installer = (Resolve-Path -LiteralPath $Installer).Path
function Install-JumpVNC([switch]$EnableWindowsLogin) {
  $options = @('/VERYSILENT', '/SUPPRESSMSGBOXES', '/NORESTART', '/NoStartService=1')
  if ($EnableWindowsLogin) { $options += '/TASKS=windowslogin' }
  $process = Start-Process -FilePath $Installer -ArgumentList $options -Wait -PassThru
  if ($process.ExitCode -ne 0) { throw "Installer failed: $($process.ExitCode)" }
}
Install-JumpVNC
$root = Join-Path $env:ProgramFiles 'SuperVNC'
$loginID = '{D55005C6-847D-422B-95D8-C57A72D45D2B}'
$class = "HKLM:\SOFTWARE\Classes\CLSID\$loginID"
$provider = "HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Authentication\Credential Providers\$loginID"
if ((Test-Path -LiteralPath $class) -or (Test-Path -LiteralPath $provider)) { throw 'Windows login must be opt-in.' }
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
# Exercise upgrades from the previous executable name without replacing credentials.
$currentCommand = $service.PathName
$legacyCommand = $currentCommand.Replace('\bin\jumpvnc.exe', '\bin\sshvnc.exe')
Copy-Item (Join-Path $root 'bin\jumpvnc.exe') (Join-Path $root 'bin\sshvnc.exe')
$changed = Invoke-CimMethod -InputObject $service -MethodName Change -Arguments @{PathName=$legacyCommand}
if ($changed.ReturnValue -ne 0) { throw 'Cannot prepare legacy service fixture.' }
Install-JumpVNC -EnableWindowsLogin
if (-not (Test-Path -LiteralPath $provider)) { throw 'Requested Windows login component is not registered.' }
$inproc = Get-Item -LiteralPath "$class\InprocServer32"
if ($inproc.GetValue('') -ne (Join-Path $root 'backends\windows\jumpvnc-credential-provider.dll') -or
    $inproc.GetValue('ThreadingModel') -ne 'Apartment') { throw 'Invalid in-process login server registration.' }
$upgraded = Get-CimInstance Win32_Service -Filter "Name='SuperVNC'"
if ($upgraded.PathName -ne $currentCommand) { throw 'Upgrade retained the old executable name.' }
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
if ((Test-Path -LiteralPath $class) -or (Test-Path -LiteralPath $provider)) { throw 'Login component left registered after uninstall.' }
if (Get-Service SuperVNC -ErrorAction SilentlyContinue) { throw 'Uninstaller left the service registered.' }
if (Get-NetFirewallRule -Name 'JumpVNC.Server' -ErrorAction SilentlyContinue) { throw 'Uninstaller left the firewall rule.' }
foreach ($file in $files) {
  if ((Get-FileHash -LiteralPath (Join-Path $root $file)).Hash -ne $before[$file]) { throw "Uninstaller removed user configuration: $file" }
}
