; Vair installer (Inno Setup 6+). Built by `wails3 task windows:package`, which
; passes the version and the paths in; see build/windows/Taskfile.yml.
;
; What this installs, and why it looks the way it does:
;
;   {app}\Vair.exe      the program, built WITHOUT the portable build tag, so it
;                       carries no engines inside it
;   {app}\bin\          sing-box, xray, the geo data and the rule-set baselines,
;                       as ordinary files
;
; The engines are files rather than payload inside the exe because a privileged
; service may only ever spawn a binary the user cannot rewrite — the same rule
; the Linux packages have followed from the start. The app finds them by looking
; beside itself first (internal/engines/engines_windows.go).
;
; Nothing here writes the firewall, the kill switch or the vair:// association.
; Those are runtime state the program owns and turns on and off from its own
; settings; an installer that pre-creates them leaves rubbish behind that nobody
; knows how to remove. Autostart is offered, because being asked once at install
; time is exactly where that question belongs — but the installer does not write
; the Run key itself either: it asks the program to, so the program's own switch
; keeps telling the truth. And at UNINSTALL time the program is asked to undo all
; of it — see CurUninstallStepChanged.

#ifndef AppVersion
  #define AppVersion "0.0.0"
#endif
#ifndef SourceExe
  #define SourceExe "..\..\..\bin\packaged\Vair.exe"
#endif
#ifndef EngineDir
  #define EngineDir "..\..\..\bin"
#endif
#ifndef OutputDir
  #define OutputDir "..\..\..\bin"
#endif

#define AppName "Vair"
#define AppPublisher "lm705"
#define AppURL "https://github.com/lm705/vair"

[Setup]
; Never change AppId: it is what makes the next release an upgrade of this one
; rather than a second copy in Programs and Features.
AppId={{8F3C7A21-6B4E-4D2F-9E15-0C7A5B93D4E8}
AppName={#AppName}
AppVersion={#AppVersion}
AppVerName={#AppName} {#AppVersion}
AppPublisher={#AppPublisher}
AppPublisherURL={#AppURL}
AppSupportURL={#AppURL}
VersionInfoVersion={#AppVersion}

; Per-user by default, and that is a technical decision rather than a timid one:
; the program updates itself by replacing its own exe, which it can only do in a
; directory it can write. An administrator can still choose all-users on the
; first page, and the app notices it cannot write to Program Files and offers the
; installer instead of an in-place update (core.installKind).
PrivilegesRequired=lowest
PrivilegesRequiredOverridesAllowed=dialog commandline
DefaultDirName={autopf}\{#AppName}
DefaultGroupName={#AppName}
DisableProgramGroupPage=yes
AllowNoIcons=yes

OutputDir={#OutputDir}
OutputBaseFilename=Vair_{#AppVersion}_setup
SetupIconFile=..\icon.ico
UninstallDisplayIcon={app}\Vair.exe
UninstallDisplayName={#AppName}
Compression=lzma2/max
SolidCompression=yes
WizardStyle=modern
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible

; The Restart Manager finds a Vair running from THIS folder and closes it, which
; is what makes an upgrade over a running copy work. A portable copy running from
; somewhere else is left alone — it is a different installation.
CloseApplications=yes
RestartApplications=no

[Languages]
Name: "en"; MessagesFile: "compiler:Default.isl"
Name: "ru"; MessagesFile: "compiler:Languages\Russian.isl"

[CustomMessages]
en.CreateDesktopIcon=Create a desktop shortcut
ru.CreateDesktopIcon=Создать ярлык на рабочем столе
en.Autostart=Start Vair when Windows starts (minimised to the tray)
ru.Autostart=Запускать Vair при входе в Windows (свёрнутым в трей)
en.LaunchApp=Run Vair
ru.LaunchApp=Запустить Vair
en.TunNeedsAdmin=TUN mode needs administrator rights: start Vair with "Run as administrator" when you want it.
ru.TunNeedsAdmin=Для режима TUN нужны права администратора: запускайте Vair через «Запуск от имени администратора».
en.InstallingWebView2=Installing the Microsoft WebView2 runtime, which Vair needs to draw its window...
ru.InstallingWebView2=Устанавливается Microsoft WebView2 — без него Vair не может отрисовать окно...
en.WebView2Failed=The WebView2 runtime could not be installed. Vair will not start without it; install "Microsoft Edge WebView2 Runtime" and try again.
ru.WebView2Failed=Не удалось установить WebView2. Без него Vair не запустится: установите «Microsoft Edge WebView2 Runtime» и попробуйте снова.
en.KeepData=Keep your configurations and settings?%n%nChoose No to delete %1 as well.
ru.KeepData=Сохранить конфигурации и настройки?%n%nОтвет «Нет» удалит также папку %1.
en.CleanupFailed=Vair could not undo everything it changed on this computer — the kill switch may still be blocking outbound traffic.%n%nDetails: %1%n%nRe-run the uninstaller as administrator, or check Windows Firewall.
ru.CleanupFailed=Vair не смог отменить все изменения в системе — kill switch может по-прежнему блокировать исходящий трафик.%n%nПодробности: %1%n%nЗапустите удаление от имени администратора или проверьте брандмауэр Windows.

[Tasks]
Name: "desktopicon"; Description: "{cm:CreateDesktopIcon}"; Flags: unchecked
Name: "autostart"; Description: "{cm:Autostart}"; Flags: unchecked

[Files]
Source: "{#SourceExe}"; DestDir: "{app}"; DestName: "Vair.exe"; Flags: ignoreversion
; The engines, and everything the engines read. The .dat/.srs/gfw.txt copies here
; are baselines: the program seeds them into %LOCALAPPDATA%\vair\rulesets on
; first run and refreshes THAT copy, because this directory is read-only for the
; user in an all-users install.
Source: "{#EngineDir}\sing-box.exe"; DestDir: "{app}\bin"; Flags: ignoreversion
Source: "{#EngineDir}\xray.exe"; DestDir: "{app}\bin"; Flags: ignoreversion
Source: "{#EngineDir}\*.dat"; DestDir: "{app}\bin"; Flags: ignoreversion
Source: "{#EngineDir}\*.srs"; DestDir: "{app}\bin"; Flags: ignoreversion
Source: "{#EngineDir}\gfw.txt"; DestDir: "{app}\bin"; Flags: ignoreversion
; The WebView2 bootstrapper: 1.7 MB that downloads the runtime when the machine
; has none. Deleted after use — it is a tool, not part of the installation.
Source: "MicrosoftEdgeWebview2Setup.exe"; DestDir: "{tmp}"; Flags: deleteafterinstall

[Icons]
Name: "{group}\{#AppName}"; Filename: "{app}\Vair.exe"
Name: "{autodesktop}\{#AppName}"; Filename: "{app}\Vair.exe"; Tasks: desktopicon

[Run]
; Autostart is switched on by the PROGRAM, not by the installer writing the Run
; key itself. The program keeps its own setting for it, and the switch in
; Settings has to agree with what Windows will actually do — an installer that
; wrote only the registry would leave that switch saying "off" about a program
; that starts by itself.
Filename: "{app}\Vair.exe"; Parameters: "--enable-autostart"; Flags: runhidden waituntilterminated; Tasks: autostart
Filename: "{app}\Vair.exe"; Description: "{cm:LaunchApp}"; Flags: nowait postinstall skipifsilent

[Code]
{ WebView2 is what draws every pixel of Vair's interface. Windows 11 has it,
  Windows 10 usually has it via Edge, and a fresh or stripped-down Windows 10 has
  nothing — the app then starts and shows no window at all, which is the worst
  possible first impression. Detecting it is a registry read; fixing it is the
  1.7 MB bootstrapper the setup carries. }
function WebView2Installed(): Boolean;
var
  Version: String;
begin
  Result := False;
  { Per-machine, 64-bit and 32-bit views, then per-user. A "pv" of 0.0.0.0 means
    the key survived an uninstall and the runtime is gone. }
  if RegQueryStringValue(HKLM, 'SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}', 'pv', Version) then
    Result := (Version <> '') and (Version <> '0.0.0.0');
  if not Result then
    if RegQueryStringValue(HKLM, 'SOFTWARE\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}', 'pv', Version) then
      Result := (Version <> '') and (Version <> '0.0.0.0');
  if not Result then
    if RegQueryStringValue(HKCU, 'SOFTWARE\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}', 'pv', Version) then
      Result := (Version <> '') and (Version <> '0.0.0.0');
end;

procedure InstallWebView2();
var
  ResultCode: Integer;
begin
  WizardForm.StatusLabel.Caption := CustomMessage('InstallingWebView2');
  { //silent //install is the bootstrapper's own unattended mode. It needs the
    network; on a machine with none it fails, and saying so beats a window that
    never appears. }
  if not Exec(ExpandConstant('{tmp}\MicrosoftEdgeWebview2Setup.exe'), '/silent /install', '',
              SW_HIDE, ewWaitUntilTerminated, ResultCode) or (ResultCode <> 0) then
    MsgBox(CustomMessage('WebView2Failed'), mbError, MB_OK);
end;

procedure CurStepChanged(CurStep: TSetupStep);
begin
  if CurStep = ssPostInstall then
    if not WebView2Installed() then
      InstallWebView2();
end;

procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
var
  ResultCode: Integer;
  DataDir: String;
  LogPath: String;
  Elevated: Boolean;
begin
  { Before the files go: ask the program to undo what it does to the SYSTEM.
    This matters more here than in most uninstallers. Vair can be holding the
    Windows Firewall profile at "block everything outbound" (the kill switch),
    a system proxy pointing at a listener that is about to disappear, and a TUN
    adapter with routes through it. Delete the exe with those in place and the
    machine has no internet and nothing left to fix it with.

    The program is elevated for this: the firewall rules need it. Failure is not
    fatal to the uninstall — the program exits non-zero and says what is still
    switched on, and that gets in front of the user, because "no internet after
    uninstalling a VPN client" is not something to leave them to work out. }
  if CurUninstallStep = usUninstall then
  begin
    if FileExists(ExpandConstant('{app}\Vair.exe')) then
    begin
      Elevated := ShellExec('runas', ExpandConstant('{app}\Vair.exe'), '--uninstall-cleanup', '',
                            SW_HIDE, ewWaitUntilTerminated, ResultCode);
      { Elevation can be refused, or be impossible — a silent uninstall has
        nobody at the screen to answer the prompt. Then the cleanup ran not at
        all, which is the very outcome this exists to prevent, so run it again
        without elevation: the registry entries and the system proxy do not need
        administrator, and the program will say so itself if what is left does. }
      if (not Elevated) or (ResultCode <> 0) then
      begin
        if not ShellExec('open', ExpandConstant('{app}\Vair.exe'), '--uninstall-cleanup', '',
                         SW_HIDE, ewWaitUntilTerminated, ResultCode) then
          ResultCode := 1;
      end;
      if ResultCode <> 0 then
      begin
        { One statement per line, and never a line that starts with "[": the
          script parser reads that as a section header even inside [Code]. }
        LogPath := ExpandConstant('{localappdata}\vair\uninstall.log');
        SuppressibleMsgBox(FmtMessage(CustomMessage('CleanupFailed'), [LogPath]), mbError, MB_OK, IDOK);
      end;
    end;
  end;

  { Configurations, the measurement history and settings are the user's, and are
    kept unless they say otherwise. Only this user's copy can be removed: in an
    all-users install every account has its own, and the uninstaller runs as one
    of them. }
  if CurUninstallStep = usPostUninstall then
  begin
    DataDir := ExpandConstant('{localappdata}\vair');
    if DirExists(DataDir) then
      if SuppressibleMsgBox(FmtMessage(CustomMessage('KeepData'), [DataDir]),
                            mbConfirmation, MB_YESNO, IDYES) = IDNO then
        DelTree(DataDir, True, True, True);
  end;
end;

procedure CurPageChanged(CurPageID: Integer);
begin
  { Said once, on the last page, where it is read rather than clicked past. }
  if CurPageID = wpFinished then
    WizardForm.FinishedLabel.Caption :=
      WizardForm.FinishedLabel.Caption + #13#10#13#10 + CustomMessage('TunNeedsAdmin');
end;
