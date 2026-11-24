param([Parameter(Mandatory=$true)][string]$Agent,[switch]$OpenPanel)
$ErrorActionPreference='Stop'
# Launch independently: closing this launcher or the tray never stops the SCM service.
$arguments=@('-manage-service')
if($OpenPanel){$arguments+='-open-panel'}
Start-Process -FilePath $Agent -ArgumentList $arguments -WindowStyle Hidden
