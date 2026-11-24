param(
  [Parameter(Mandatory=$true)][string]$Agent,
  [Parameter(Mandatory=$true)][string]$Config,
  [switch]$Start
)

$ErrorActionPreference = 'Stop'
$name = 'SuperVNC'
$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = New-Object Security.Principal.WindowsPrincipal($identity)
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
  throw 'Run this installer from an elevated PowerShell session.'
}
if (Get-Service -Name $name -ErrorAction SilentlyContinue) {
  throw 'The SuperVNC service already exists. Manage it through Windows Services.'
}

$root = Join-Path ([Environment]::GetFolderPath('ProgramFiles')) 'SuperVNC'
function Resolve-ServiceFile([string]$Path) {
  $file = Get-Item -LiteralPath $Path
  $prefix = $root.TrimEnd('\') + '\'
  if ($file -isnot [IO.FileInfo] -or -not $file.FullName.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase)) {
    throw "The service file must be under $root`: $Path"
  }
  if ($file.Attributes -band [IO.FileAttributes]::ReparsePoint) {
    throw "The service file must not be a reparse point: $($file.FullName)"
  }
  $item = $file.Directory
  while ($item -and $item.FullName.StartsWith($root, [StringComparison]::OrdinalIgnoreCase)) {
    if ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) {
      throw "The service path must not traverse a reparse point: $($item.FullName)"
    }
    $item = $item.Parent
  }
  return $file.FullName
}

$agentPath = Resolve-ServiceFile $Agent
$configPath = Resolve-ServiceFile $Config
$command = '"{0}" -service -config "{1}"' -f $agentPath, $configPath
New-Service -Name $name -DisplayName 'SuperVNC Server' -Description 'Shares the Windows console over SSH/SVNC' `
  -BinaryPathName $command -StartupType Manual | Out-Null

$sc = Join-Path ([Environment]::SystemDirectory) 'sc.exe'
& $sc failure $name 'reset=' '86400' 'actions=' 'restart/5000/restart/15000/restart/60000' | Out-Null
if ($LASTEXITCODE -ne 0) { throw 'Could not configure SuperVNC service restart actions.' }
& $sc failureflag $name 1 | Out-Null
if ($LASTEXITCODE -ne 0) { throw 'Could not enable restart actions for service errors.' }
Set-Service -Name $name -StartupType Automatic
if ($Start) { Start-Service -Name $name }
