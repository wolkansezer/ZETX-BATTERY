; ZETX BATTERY kurulum betiği (Inno Setup 6: https://jrsoftware.org/isinfo.php)
; Derleme: ISCC /DAppVersion=1.4.4 setup.iss   (önce dist\ZETX BATTERY.exe derlenmiş olmalı)
#ifndef AppVersion
  #define AppVersion "0.0.0"
#endif
#define AppName "ZETX BATTERY"
#define AppExe "ZETX BATTERY.exe"

[Setup]
AppId={{6F1C2B7E-3A4D-4E8B-9C55-2D7A1E0B4F63}
AppName={#AppName}
AppVersion={#AppVersion}
AppVerName={#AppName} {#AppVersion}
AppPublisher=Volkan Sezer (ZETX)
AppPublisherURL=https://github.com/wolkansezer/ZETX-BATTERY
AppSupportURL=https://discord.gg/nvqc84AHZf
AppUpdatesURL=https://github.com/wolkansezer/ZETX-BATTERY/releases/latest
DefaultDirName={localappdata}\Programs\{#AppName}
DisableDirPage=yes
DisableProgramGroupPage=yes
PrivilegesRequired=lowest
OutputDir=dist
OutputBaseFilename=ZETX_BATTERY_Kurulum
SetupIconFile=app.ico
UninstallDisplayIcon={app}\{#AppExe}
UninstallDisplayName={#AppName}
Compression=none
SolidCompression=no
WizardStyle=modern
ShowLanguageDialog=no
CloseApplications=force
RestartApplications=no
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
VersionInfoVersion={#AppVersion}
VersionInfoProductVersion={#AppVersion}
VersionInfoCompany=Volkan Sezer (ZETX)
VersionInfoProductName={#AppName}
VersionInfoDescription={#AppName} kurulum programi
VersionInfoCopyright=(c) 2026 Volkan Sezer - MIT

[Languages]
Name: "turkish"; MessagesFile: "compiler:Languages\Turkish.isl"

[Tasks]
Name: "autostart"; Description: "Windows açılınca {#AppName} da başlasın"
Name: "desktopicon"; Description: "Masaüstüne kısayol oluştur"

[Files]
Source: "dist\{#AppExe}"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
Name: "{userprograms}\{#AppName}"; Filename: "{app}\{#AppExe}"
Name: "{userdesktop}\{#AppName}"; Filename: "{app}\{#AppExe}"; Tasks: desktopicon

[Registry]
Root: HKCU; Subkey: "Software\Microsoft\Windows\CurrentVersion\Run"; ValueType: string; ValueName: "{#AppName}"; ValueData: """{app}\{#AppExe}"""; Tasks: autostart
Root: HKCU; Subkey: "Software\{#AppName}"; ValueType: dword; ValueName: "Installed"; ValueData: 1; Flags: uninsdeletekey

[InstallDelete]
; kaldırma programı: eski (1.4.3 ve öncesi) kurulumdan kalan dosya
Type: files; Name: "{app}\Kaldir.exe"

[Run]
Filename: "{app}\{#AppExe}"; Description: "{#AppName} programını başlat"; Flags: nowait postinstall skipifsilent

[Code]
// Çalışan programı kapatır: ana penceresine WM_CLOSE gönderir ve kapanmasını bekler.
procedure CloseRunningApp;
var
  Wnd: HWND;
  I: Integer;
begin
  Wnd := FindWindowByClassName('ZETXBatteryBar');
  if Wnd = 0 then
    Exit;
  PostMessage(Wnd, 16, 0, 0);
  for I := 1 to 40 do
  begin
    if FindWindowByClassName('ZETXBatteryBar') = 0 then
      Break;
    Sleep(100);
  end;
  Sleep(300);
end;

function PrepareToInstall(var NeedsRestart: Boolean): String;
begin
  CloseRunningApp;
  Result := '';
end;

procedure CurStepChanged(CurStep: TSetupStep);
begin
  // 1.4.3 ve öncesinin "Uygulamalar" listesindeki kaydı (yenisi bu kurulumla yazılır)
  if CurStep = ssInstall then
    RegDeleteKeyIncludingSubkeys(HKEY_CURRENT_USER, 'Software\Microsoft\Windows\CurrentVersion\Uninstall\ZETXBattery');
end;

function InitializeUninstall: Boolean;
begin
  CloseRunningApp;
  Result := True;
end;

procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
begin
  if CurUninstallStep = usUninstall then
    RegDeleteValue(HKEY_CURRENT_USER, 'Software\Microsoft\Windows\CurrentVersion\Run', 'ZETX BATTERY');
  if CurUninstallStep = usPostUninstall then
    if MsgBox('Ayarlar ve tanılama dosyası da silinsin mi?', mbConfirmation, MB_YESNO) = IDYES then
      DelTree(ExpandConstant('{userappdata}\ZETX BATTERY'), True, True, True);
end;
