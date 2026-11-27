#ifndef AppVersion
  #define AppVersion "0.1.3"
#endif
#ifndef PackageDir
  #define PackageDir "..\..\..\dist\jumpvnc-server-windows-amd64"
#endif
#ifndef RedistPath
  #define RedistPath "..\..\..\dist\prerequisites\vc_redist.x64.exe"
#endif

[Setup]
AppId={{737DA3F3-8A83-4661-9844-B2F00DE07B05}
AppName=JumpVNC Server
AppVersion={#AppVersion}
AppPublisher=JumpServer
AppPublisherURL=https://github.com/jumpserver/jumpvnc
DefaultDirName={autopf}\SuperVNC
DisableDirPage=yes
DisableProgramGroupPage=yes
PrivilegesRequired=admin
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
MinVersion=10.0
OutputDir=..\..\..\dist
OutputBaseFilename=jumpvnc-server-windows-amd64-setup
LicenseFile={#PackageDir}\LICENSE
Compression=lzma2
SolidCompression=yes
WizardStyle=modern
SetupLogging=yes
CloseApplications=yes
RestartApplications=no
UninstallDisplayName=JumpVNC Server

[Tasks]
Name: "windowslogin"; Description: "Enable Windows console sign-in from JumpVNC Viewer"; Flags: unchecked

Name: "windowslogin\managedaccount"; Description: "Use a dedicated standard local account (JumpVNCUser) with a device-generated password"; Flags: unchecked

[Files]
Source: "{#PackageDir}\*"; DestDir: "{app}"; Flags: ignoreversion recursesubdirs createallsubdirs
Source: "{#RedistPath}"; DestDir: "{tmp}"; Flags: deleteafterinstall
Source: "prepare-install.ps1"; Flags: dontcopy

[Run]
Filename: "{app}\bin\jumpvnc.exe"; Parameters: "-manage-service -open-panel"; Description: "Open JumpVNC Server"; Flags: nowait postinstall skipifsilent runascurrentuser

[Code]
function RunPowerShell(const Script, Extra: String): Boolean;
var
  ExitCode: Integer;
begin
  Result := Exec(ExpandConstant('{sys}\WindowsPowerShell\v1.0\powershell.exe'),
    '-NoProfile -NonInteractive -ExecutionPolicy Bypass -File "' + Script + '" ' + Extra,
    '', SW_HIDE, ewWaitUntilTerminated, ExitCode);
  Result := Result and (ExitCode = 0);
end;

function PrepareToInstall(var NeedsRestart: Boolean): String;
begin
  ExtractTemporaryFile('prepare-install.ps1');
  if not RunPowerShell(ExpandConstant('{tmp}\prepare-install.ps1'),
    '-InstallRoot "' + ExpandConstant('{app}') + '"') then
    Result := 'Could not stop the existing JumpVNC installation. No program files were replaced.';
end;

procedure CurStepChanged(CurStep: TSetupStep);
var
  ExitCode: Integer;
  Extra: String;
begin
  if CurStep <> ssPostInstall then Exit;
  if not Exec(ExpandConstant('{tmp}\vc_redist.x64.exe'), '/install /quiet /norestart',
    '', SW_HIDE, ewWaitUntilTerminated, ExitCode) then
    RaiseException('Could not launch the Visual C++ runtime installer.');
  if (ExitCode <> 0) and (ExitCode <> 1638) and (ExitCode <> 3010) then
    RaiseException('The Visual C++ runtime could not be installed.');
  Extra := '';
  if ExpandConstant('{param:NoStartService|0}') = '1' then Extra := '-NoStartService';
  if WizardIsTaskSelected('windowslogin') then Extra := Extra + ' -EnableWindowsLogin';
  if WizardIsTaskSelected('windowslogin\managedaccount') then Extra := Extra + ' -ManagedAccount';
  if not RunPowerShell(ExpandConstant('{app}\deployments\windows\installer\setup-service.ps1'), Extra) then
    RaiseException('Could not configure JumpVNC. See setup.log in the installation directory.');
end;

function InitializeUninstall(): Boolean;
begin
  Result := RunPowerShell(ExpandConstant('{app}\deployments\windows\installer\prepare-install.ps1'),
    '-InstallRoot "' + ExpandConstant('{app}') + '" -Uninstall');
  if not Result then
    MsgBox('Could not stop or remove the JumpVNC service. The installation was kept.', mbError, MB_OK);
end;
