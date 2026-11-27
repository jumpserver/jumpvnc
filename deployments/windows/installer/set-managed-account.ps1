#Requires -RunAsAdministrator
param([ValidateSet('Enable','Disable')][string]$Action)
$ErrorActionPreference = 'Stop'
$root = Join-Path ([Environment]::GetFolderPath('ProgramFiles')) 'SuperVNC'
$active = Join-Path $root 'managed-account.json'
$inactive = "$active.disabled"
$name = 'JumpVNCUser'
if ((Test-Path -LiteralPath $active) -and (Test-Path -LiteralPath $inactive)) { throw 'Ambiguous managed account state.' }
$record = if (Test-Path -LiteralPath $active) { $active } elseif (Test-Path -LiteralPath $inactive) { $inactive }
if ($Action -eq 'Disable' -and !$record) { return }
if ((Get-CimInstance Win32_ComputerSystem).DomainRole -ge 4) { throw 'A local account requires a member or standalone machine.' }
$user = Get-LocalUser -Name $name -ErrorAction SilentlyContinue
if ($record) {
  $state = Get-Content -LiteralPath $record -Raw | ConvertFrom-Json
  if (!$user -or $state.sid -ne $user.SID.Value) { throw 'The recorded managed account SID does not match.' }
  if ($Action -eq 'Disable') {
    if ($record -eq $active) { [IO.File]::Move($active,$inactive) }
    Disable-LocalUser -SID $user.SID
    return
  }
} elseif ($user) { throw 'JumpVNCUser already exists and is not owned by this installation.' }

$key = (Get-Content -LiteralPath (Join-Path $root 'client_ed25519_key.pub') -Raw).Trim() -split '\s+'
if ($key[0] -ne 'ssh-ed25519') { throw 'Expected the installation public key.' }
$sha = [Security.Cryptography.SHA256]::Create()
try { $fingerprint = 'SHA256:' + [Convert]::ToBase64String($sha.ComputeHash([Convert]::FromBase64String($key[1]))).TrimEnd('=') } finally { $sha.Dispose() }
if ($record) {
  if ($state.fingerprint -cne $fingerprint) { throw 'Installation public key differs from the enrolled key.' }
  $admins = Get-LocalGroupMember -SID 'S-1-5-32-544'
  if ($admins.SID.Value -contains $state.sid) { throw 'The managed desktop account must be a standard user.' }
  if ($record -eq $inactive) { Enable-LocalUser -SID $user.SID; [IO.File]::Move($inactive,$active) }
  return
}
Add-Type -AssemblyName System.Security
$rng = [Security.Cryptography.RandomNumberGenerator]::Create()
$random = New-Object byte[] 32
$secure = New-Object Security.SecureString
$created = $null
$temp = "$active.$([guid]::NewGuid()).tmp"
try {
  $rng.GetBytes($random)
  $bytes = [Text.Encoding]::UTF8.GetBytes(([Convert]::ToBase64String($random) + '!aB7'))
  foreach ($b in $bytes) { $secure.AppendChar([char]$b) }
  $secure.MakeReadOnly()
  $cipher = [Security.Cryptography.ProtectedData]::Protect($bytes,$null,[Security.Cryptography.DataProtectionScope]::LocalMachine)
  $created = New-LocalUser -Name $name -Password $secure -Disabled -PasswordNeverExpires -UserMayNotChangePassword -Description 'JumpVNC managed desktop'
  $users = Get-LocalGroupMember -SID 'S-1-5-32-545'
  if ($users.SID.Value -notcontains $created.SID.Value) { Add-LocalGroupMember -SID 'S-1-5-32-545' -Member $created }
  $json = @{sid=$created.SID.Value; fingerprint=$fingerprint; encrypted=[Convert]::ToBase64String($cipher)} | ConvertTo-Json -Compress
  $file = [IO.File]::Open($temp,'CreateNew','Write','None')
  $file.Dispose()
  $acl = New-Object Security.AccessControl.FileSecurity
  $acl.SetSecurityDescriptorSddlForm('D:P(A;;FA;;;SY)(A;;FA;;;BA)')
  [IO.File]::SetAccessControl($temp,$acl)
  [IO.File]::WriteAllText($temp,$json,(New-Object Text.UTF8Encoding($false)))
  Enable-LocalUser -SID $created.SID
  [IO.File]::Move($temp,$active)
} catch {
  if ($created) { Remove-LocalUser -SID $created.SID }
  throw
} finally {
  Remove-Item -LiteralPath $temp -ErrorAction SilentlyContinue
  if ($bytes) { [Array]::Clear($bytes,0,$bytes.Length) }
  [Array]::Clear($random,0,$random.Length)
  $secure.Dispose()
  $rng.Dispose()
}
