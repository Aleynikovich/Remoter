; Remoter's Windows installer, built with Inno Setup 6 by the release
; workflow: iscc /DAppVersion=dev-57 /DNumericVersion=1.0.57.0
;   /DSourceExe=dist\remoter-windows-amd64.exe /ODist installer\remoter.iss
;
; It installs remoter.exe in Program Files and runs it with -setup, which
; points the boot service at it, stops every other Remoter on the computer
; (old downloads, the old separate host and viewer programs) and makes it the
; one that starts at sign-in. After that the boot service keeps it updated.

#ifndef AppVersion
  #define AppVersion "dev"
#endif
#ifndef NumericVersion
  #define NumericVersion "1.0.0.0"
#endif
#ifndef SourceExe
  #define SourceExe "..\remoter.exe"
#endif

[Setup]
; Fixed: Windows finds an earlier install by it (uninstall key Remoter_is1).
AppId=Remoter
AppName=Remoter
AppVersion={#AppVersion}
AppVerName=Remoter {#AppVersion}
AppPublisher=Remoter
AppPublisherURL=https://github.com/Aleynikovich/Remoter
VersionInfoVersion={#NumericVersion}
DefaultDirName={autopf}\Remoter
DisableDirPage=yes
DefaultGroupName=Remoter
DisableProgramGroupPage=yes
PrivilegesRequired=admin
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
OutputBaseFilename=RemoterSetup
UninstallDisplayIcon={app}\remoter.exe
UninstallDisplayName=Remoter
Compression=lzma2
SolidCompression=yes
WizardStyle=modern
; remoter -setup stops the running copies itself, after the new one is in
; place, so the computer is unreachable for as short a time as possible.
CloseApplications=no
RestartApplications=no

[Tasks]
Name: desktopicon; Description: "Create a desktop shortcut"

[Files]
Source: "{#SourceExe}"; DestDir: "{app}"; DestName: "remoter.exe"; Flags: ignoreversion

[Icons]
; Straight in the Start menu, not in a folder, so it is easy to find.
Name: "{autoprograms}\Remoter"; Filename: "{app}\remoter.exe"
Name: "{autodesktop}\Remoter"; Filename: "{app}\remoter.exe"; Tasks: desktopicon

[InstallDelete]
; dev-57 put the shortcut in a Remoter folder.
Type: filesandordirs; Name: "{autoprograms}\Remoter"

[Registry]
; Tells copies downloaded elsewhere to hand over to this one.
Root: HKLM; Subkey: "Software\Remoter"; ValueType: string; ValueName: "Path"; ValueData: "{app}\remoter.exe"; Flags: uninsdeletekey

[Run]
Filename: "{app}\remoter.exe"; Parameters: "-setup"; StatusMsg: "Starting Remoter..."; Flags: runhidden waituntilterminated
Filename: "{app}\remoter.exe"; Description: "Open Remoter"; Flags: postinstall nowait skipifsilent runasoriginaluser

[UninstallRun]
Filename: "{app}\remoter.exe"; Parameters: "-unsetup"; RunOnceId: "RemoterUnsetup"; Flags: runhidden waituntilterminated

[UninstallDelete]
; Left by self-updates (remoter.old.exe, remoter.new.exe).
Type: files; Name: "{app}\remoter.*.exe"

[Code]
// Windows lets a running program's file be renamed but not overwritten, and
// the boot service runs from {app}\remoter.exe. Move it aside so the new one
// can be copied in without stopping anything yet; Remoter deletes the
// .old.exe once it restarts.
function PrepareToInstall(var NeedsRestart: Boolean): String;
var
  Exe, Old: String;
begin
  Result := '';
  Exe := ExpandConstant('{app}\remoter.exe');
  Old := ExpandConstant('{app}\remoter.old.exe');
  if FileExists(Exe) then
  begin
    DeleteFile(Old);
    if not RenameFile(Exe, Old) then
      Result := 'Could not replace ' + Exe + '. Close Remoter and try again.';
  end;
end;

// If the install stopped before the new copy was in place, put the old one
// back, so the boot service still has a program to run.
procedure DeinitializeSetup();
var
  Exe, Old: String;
begin
  try
    Exe := ExpandConstant('{app}\remoter.exe');
    Old := ExpandConstant('{app}\remoter.old.exe');
    if not FileExists(Exe) and FileExists(Old) then
      RenameFile(Old, Exe);
  except
    // {app} is not known yet when setup quits before its first page.
  end;
end;
