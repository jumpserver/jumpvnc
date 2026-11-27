#Requires -RunAsAdministrator
param([Parameter(Mandatory=$true)][ValidateSet('Enable','Disable')][string]$Action)
$ErrorActionPreference = 'Stop'
if (-not [Environment]::Is64BitProcess) { throw 'Use 64-bit Windows PowerShell.' }
$root = Join-Path ([Environment]::GetFolderPath('ProgramFiles')) 'SuperVNC'
$dll = Join-Path $root 'backends\windows\jumpvnc-credential-provider.dll'
$id = '{D55005C6-847D-422B-95D8-C57A72D45D2B}'
$classes = "SOFTWARE\Classes\CLSID\$id"
$providers = "SOFTWARE\Microsoft\Windows\CurrentVersion\Authentication\Credential Providers\$id"
$hive = [Microsoft.Win32.Registry]::LocalMachine
$label = 'JumpVNC Windows Login'
$owner = $hive.OpenSubKey("$classes\InprocServer32")
try {
  if ($owner -and $owner.GetValue('') -ne $dll) { throw 'This CLSID belongs to another component.' }
} finally { if ($owner) { $owner.Dispose() } }
foreach ($name in @($classes,$providers)) {
  $key = $hive.OpenSubKey($name)
  try {
    if ($key -and $key.GetValue('') -ne $label) { throw 'Existing credential provider registration is not owned by JumpVNC.' }
  } finally { if ($key) { $key.Dispose() } }
}
if ($Action -eq 'Disable') {
  $hive.DeleteSubKeyTree($providers,$false)
  $hive.DeleteSubKeyTree($classes,$false)
  return
}
if (-not (Test-Path -LiteralPath $dll -PathType Leaf)) { throw "The login provider is missing: $dll" }
try {
  $key = $hive.CreateSubKey($classes)
  try { $key.SetValue('',$label) } finally { $key.Dispose() }
  $key = $hive.CreateSubKey("$classes\InprocServer32")
  try {
    $key.SetValue('',$dll)
    $key.SetValue('ThreadingModel','Apartment')
  } finally { $key.Dispose() }
  # Make the tile discoverable only after its in-process COM server is registered.
  $key = $hive.CreateSubKey($providers)
  try { $key.SetValue('',$label) } finally { $key.Dispose() }
} catch {
  $hive.DeleteSubKeyTree($providers,$false)
  $hive.DeleteSubKeyTree($classes,$false)
  throw
}
