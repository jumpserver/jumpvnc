param(
  [Parameter(Mandatory=$true)][string]$Agent,
  [Parameter(Mandatory=$true)][string]$Config,
  [switch]$Start,
  [switch]$NoTray
)
$ErrorActionPreference='Stop'
& (Join-Path $PSScriptRoot 'install-service.ps1') @PSBoundParameters
