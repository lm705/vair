; Vair installer (Inno Setup 6+). Built by `wails3 task windows:package`, which
; passes the version and the paths in; see build/windows/Taskfile.yml.
;
; What this installs, and why it looks the way it does:
;
;   {app}\Vair.exe         the program, built WITHOUT the portable build tag, so
;                          it carries no engines inside it
;   {app}\vair-helper.exe  the Vair helper service (VairHelper): the small part
;                          that runs as SYSTEM and does what needs administrator
;                          rights — the tunnel, the kill switch's filters, the
;                          firewall rules — so the program itself never does
;   {app}\bin\             sing-box, xray, the geo data and the rule-set
;                          baselines, as ordinary files
;
; For all users, in Program Files, and nowhere else: the service runs the
; engines in {app}\bin as SYSTEM and lets them out of its blocks, so the folder
; they live in must be one no ordinary account can write — the same rule the
; Linux packages have followed from the start. Hence the administrator prompt
; at install and at every update; the program itself then runs with the user's
; ordinary rights. The app finds the engines by looking beside itself first
; (internal/engines/engines_windows.go). See dev/WINDOWS-SERVICE.md.
;
; Nothing here writes the firewall, the kill switch or the vair:// association.
; Those are runtime state the program owns and turns on and off from its own
; settings (through the service); an installer that pre-creates them leaves
; rubbish behind that nobody knows how to remove. The service itself is
; installed by its own program (vair-helper -install-service), which also
; repairs it later from the program's settings, so there is one way to set it
; up rather than one here and another there. Autostart is offered, because being asked once at install
; time is exactly where that question belongs — but the installer does not write
; the Run key itself either: it asks the program to, so the program's own switch
; keeps telling the truth. And at UNINSTALL time the program is asked to undo all
; of it, and to close itself first — see CurUninstallStepChanged.

#ifndef AppVersion
  #define AppVersion "0.0.0"
#endif
#ifndef SourceExe
  #define SourceExe "..\..\..\bin\packaged\Vair.exe"
#endif
#ifndef HelperExe
  #define HelperExe "..\..\..\bin\packaged\vair-helper.exe"
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

; For all users only (see the top of this file): the helper service needs a
; folder ordinary accounts cannot write. Releases up to 2.7.0 installed per user
; by default; such a copy is removed once this one is in place (CurStepChanged),
; and its data stays. Updates come from this setup program too: the app knows a
; copy it cannot replace itself (core.installKind) and offers the installer.
PrivilegesRequired=admin
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
; somewhere else is left alone — it is a different installation. The service is
; stopped before that, by PrepareToInstall, rather than left to the Restart
; Manager: stopping it is how its tunnel comes down cleanly.
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
en.InstallingService=Installing the Vair helper service...
ru.InstallingService=Устанавливается служба-помощник Vair...
en.ServiceFailed=The Vair helper service could not be installed (%1). Vair works without it in proxy mode; for TUN mode and the kill switch, open Settings → System in Vair and press Repair, or run this setup again.
ru.ServiceFailed=Не удалось установить службу-помощник Vair (%1). Без неё Vair работает в режиме прокси; для режима TUN и kill switch откройте в Vair «Настройки → Система» и нажмите «Восстановить» или запустите установку ещё раз.
en.OldCopyFailed=The previous Vair, installed for this user only in %1, could not be removed (%2). Uninstall it from Settings → Apps, then run this setup again.
ru.OldCopyFailed=Не удалось удалить прежний Vair, установленный только для этого пользователя в %1 (%2). Удалите его через «Параметры → Приложения» и запустите установку снова.
en.StopServiceFailed=The Vair helper service did not stop (%1), so its files cannot be replaced. Restart Windows and run this setup again.
ru.StopServiceFailed=Служба-помощник Vair не остановилась (%1), поэтому её файлы нельзя заменить. Перезагрузите Windows и запустите установку снова.
en.InstallingWebView2=Installing the Microsoft WebView2 runtime, which Vair needs to draw its window...
ru.InstallingWebView2=Устанавливается Microsoft WebView2 — без него Vair не может отрисовать окно...
en.WebView2Failed=The WebView2 runtime could not be installed. Vair will not start without it; install "Microsoft Edge WebView2 Runtime" and try again.
ru.WebView2Failed=Не удалось установить WebView2. Без него Vair не запустится: установите «Microsoft Edge WebView2 Runtime» и попробуйте снова.
en.KeepData=Keep your configurations and settings?%n%nChoose No to delete %1 as well.
ru.KeepData=Сохранить конфигурации и настройки?%n%nОтвет «Нет» удалит также папку %1.
en.CleanupFailed=Vair was not removed: it could not undo everything it changed on this computer, and without the program nothing would be left to undo it.%n%nDetails: %1%n%nQuit Vair from the tray and turn the kill switch off, then uninstall again and allow the administrator prompt.
ru.CleanupFailed=Vair не удалён: программа не смогла отменить все свои изменения в системе, а без неё отменить их было бы нечем.%n%nПодробности: %1%n%nЗакройте Vair через трей и выключите kill switch, затем запустите удаление снова и разрешите запрос прав администратора.
en.ServiceRemoveFailed=Vair was not removed: the Vair helper service could not take down everything it holds (code %1) — the kill switch's filters among them — and without its program nothing would be left to take them down.%n%nRestart Windows and uninstall again.
ru.ServiceRemoveFailed=Vair не удалён: служба-помощник Vair не смогла снять всё, что держит (код %1), в том числе фильтры kill switch, а без её программы снять их было бы нечем.%n%nПерезагрузите Windows и запустите удаление снова.

[Tasks]
Name: "desktopicon"; Description: "{cm:CreateDesktopIcon}"; Flags: unchecked
Name: "autostart"; Description: "{cm:Autostart}"; Flags: unchecked

[Files]
Source: "{#SourceExe}"; DestDir: "{app}"; DestName: "Vair.exe"; Flags: ignoreversion
Source: "{#HelperExe}"; DestDir: "{app}"; DestName: "vair-helper.exe"; Flags: ignoreversion
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
;
; Both as the user who started the setup, not as the administrator it runs as:
; the Run key is that user's, and Vair must never run elevated — with the
; service it has no need to, and an elevated copy would be the one thing the
; split exists to avoid. A copy that had autostart before the move from a
; per-user install gets it back the same way, from CurStepChanged.
Filename: "{app}\Vair.exe"; Parameters: "--enable-autostart"; Flags: runhidden waituntilterminated runasoriginaluser; Tasks: autostart
Filename: "{app}\Vair.exe"; Description: "{cm:LaunchApp}"; Flags: nowait postinstall skipifsilent runasoriginaluser

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

const
  AppIdKey = '{8F3C7A21-6B4E-4D2F-9E15-0C7A5B93D4E8}_is1';
  UninstallRoot = 'Software\Microsoft\Windows\CurrentVersion\Uninstall\';
  ServiceName = 'VairHelper';
  SC_MANAGER_CONNECT = $0001;
  SERVICE_QUERY_STATUS = $0004;
  SERVICE_STOPPED = 1;

type
  TServiceStatus = record
    dwServiceType: Cardinal;
    dwCurrentState: Cardinal;
    dwControlsAccepted: Cardinal;
    dwWin32ExitCode: Cardinal;
    dwServiceSpecificExitCode: Cardinal;
    dwCheckPoint: Cardinal;
    dwWaitHint: Cardinal;
  end;

function OpenSCManager(lpMachineName, lpDatabaseName: String; dwDesiredAccess: Cardinal): THandle;
  external 'OpenSCManagerW@advapi32.dll stdcall';
function OpenService(hSCManager: THandle; lpServiceName: String; dwDesiredAccess: Cardinal): THandle;
  external 'OpenServiceW@advapi32.dll stdcall';
function QueryServiceStatus(hService: THandle; var ServiceStatus: TServiceStatus): Boolean;
  external 'QueryServiceStatus@advapi32.dll stdcall';
function CloseServiceHandle(hSCObject: THandle): Boolean;
  external 'CloseServiceHandle@advapi32.dll stdcall';

var
  HadOldAutostart: Boolean;
  { The service this setup stopped, and whether the new files went in: a
    setup that stops and then fails puts the old service back. }
  StoppedService: Boolean;
  Installed: Boolean;

{ Whether the service is down: stopped, or not there at all. Asked of the
  service manager rather than read off net.exe, whose exit code is the same
  for "was not running" and "would not stop". }
function ServiceDown(): Boolean;
var
  Manager, Service: THandle;
  Status: TServiceStatus;
begin
  Result := False;
  Manager := OpenSCManager('', 'ServicesActive', SC_MANAGER_CONNECT);
  if Manager = 0 then
    Exit;
  Service := OpenService(Manager, ServiceName, SERVICE_QUERY_STATUS);
  if Service = 0 then
    Result := True
  else
  begin
    if QueryServiceStatus(Service, Status) then
      Result := Status.dwCurrentState = SERVICE_STOPPED;
    CloseServiceHandle(Service);
  end;
  CloseServiceHandle(Manager);
end;

{ The service is stopped before its files are replaced — by its own program
  when the installed one has it, which takes the tunnel down and waits for
  the stop; otherwise through the service manager. A persistent block stays
  up through the update: its filters are the filtering engine's, not the
  service's process. }
function StopService(): String;
var
  ResultCode, I: Integer;
  Helper: String;
begin
  Result := '';
  if ServiceDown() then
    Exit;
  StoppedService := True;
  Helper := ExpandConstant('{app}\vair-helper.exe');
  if not (FileExists(Helper) and Exec(Helper, '-stop-service', '', SW_HIDE, ewWaitUntilTerminated, ResultCode) and (ResultCode = 0)) then
    Exec(ExpandConstant('{sys}\net.exe'), 'stop ' + ServiceName, '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
  for I := 1 to 60 do
  begin
    if ServiceDown() then
      Exit;
    Sleep(500);
  end;
  Result := FmtMessage(CustomMessage('StopServiceFailed'), [IntToStr(ResultCode)]);
end;

function PrepareToInstall(var NeedsRestart: Boolean): String;
begin
  Result := StopService();
end;

{ A copy from 2.7.0 or earlier installed for this user only: its own
  uninstaller takes it away — closing it, putting back what it changed and
  removing its files — and leaves the data in %LOCALAPPDATA%\vair, which this
  copy goes on using. Done once this copy is in place, so a setup that fails
  leaves the old one rather than none. Its autostart entry goes with it, and
  is put back for this copy if it was there. }
procedure RemoveOldPerUserCopy();
var
  Uninstaller, Location: String;
  RunValue: String;
  ResultCode: Integer;
  Failed: String;
begin
  if not RegQueryStringValue(HKCU, UninstallRoot + AppIdKey, 'UninstallString', Uninstaller) then
    Exit;
  Uninstaller := RemoveQuotes(Uninstaller);
  if not RegQueryStringValue(HKCU, UninstallRoot + AppIdKey, 'InstallLocation', Location) then
    Location := ExtractFileDir(Uninstaller);
  if not FileExists(Uninstaller) then
  begin
    { The files are already gone; only the entry is left. }
    RegDeleteKeyIncludingSubkeys(HKCU, UninstallRoot + AppIdKey);
    Exit;
  end;
  if RegQueryStringValue(HKCU, 'Software\Microsoft\Windows\CurrentVersion\Run', 'Vair', RunValue) then
    HadOldAutostart := Pos(Lowercase(RemoveBackslashUnlessRoot(Location)), Lowercase(RunValue)) > 0;
  { /SUPPRESSMSGBOXES answers "keep your configurations?" with its default,
    which is yes. }
  Failed := '';
  if not Exec(Uninstaller, '/VERYSILENT /SUPPRESSMSGBOXES /NORESTART', '', SW_HIDE, ewWaitUntilTerminated, ResultCode) then
    Failed := SysErrorMessage(ResultCode)
  else if ResultCode <> 0 then
    Failed := IntToStr(ResultCode);
  if Failed <> '' then
  begin
    SuppressibleMsgBox(FmtMessage(CustomMessage('OldCopyFailed'), [Location, Failed]), mbError, MB_OK, IDOK);
    Exit;
  end;
  { Setup saw the per-user entry when it wrote this copy's and named this one
    "Vair (all users)" to tell the two apart; with the old one gone there is
    nothing to tell apart. }
  if not RegKeyExists(HKCU, UninstallRoot + AppIdKey) then
    RegWriteStringValue(HKLM, UninstallRoot + AppIdKey, 'DisplayName', '{#AppName}');
end;

{ After the files: the service is installed, or its registration brought up to
  date, and started, by its own program. A failure does not undo the install —
  the program works without the service in proxy mode and can repair it from
  its settings — but it is said, not swallowed. }
procedure InstallService();
var
  ResultCode: Integer;
begin
  WizardForm.StatusLabel.Caption := CustomMessage('InstallingService');
  if not Exec(ExpandConstant('{app}\vair-helper.exe'), '-install-service', '', SW_HIDE, ewWaitUntilTerminated, ResultCode) then
    SuppressibleMsgBox(FmtMessage(CustomMessage('ServiceFailed'), [SysErrorMessage(ResultCode)]), mbError, MB_OK, IDOK)
  else if ResultCode <> 0 then
    SuppressibleMsgBox(FmtMessage(CustomMessage('ServiceFailed'), [IntToStr(ResultCode)]), mbError, MB_OK, IDOK);
end;

procedure CurStepChanged(CurStep: TSetupStep);
var
  ResultCode: Integer;
begin
  if CurStep = ssPostInstall then
  begin
    Installed := True;
    { The old copy first: its cleanup puts back what it changed, and the
      service then starts on a machine with nothing of the old copy left. }
    RemoveOldPerUserCopy();
    InstallService();
    if HadOldAutostart and not WizardIsTaskSelected('autostart') then
      ExecAsOriginalUser(ExpandConstant('{app}\Vair.exe'), '--enable-autostart', '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
    if not WebView2Installed() then
      InstallWebView2();
  end;
end;

{ A setup that stopped the service and then did not install — cancelled, or
  a file it could not replace — starts the old one again: its files are
  still the ones in place. }
procedure DeinitializeSetup();
var
  ResultCode: Integer;
begin
  if StoppedService and not Installed then
    Exec(ExpandConstant('{sys}\net.exe'), 'start ' + ServiceName, '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
end;

procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
var
  ResultCode: Integer;
  DataDir: String;
  LogPath: String;
  Elevated: Boolean;
begin
  { Before the files go: ask the program to undo what it does to the SYSTEM.
    This matters more here than in most uninstallers. Vair can be holding a
    block on every outbound connection (the kill switch), a system proxy
    pointing at a listener that is about to disappear, and a TUN adapter with
    routes through it. Delete the exe with those in place and the machine has
    no internet and nothing left to fix it with.

    The program closes a running copy of itself first: the uninstaller does not
    (Restart Manager is for Setup only), and Vair lives in the tray. Then the
    helper service takes down everything it holds — the tunnel, every filter,
    its firewall rules, leftover adapters — and removes itself.

    If either cannot, the uninstall is called off rather than carried on: these
    programs are the one thing that can still take the block down, and the
    message says what is left and what to do. }
  if CurUninstallStep = usUninstall then
  begin
    if FileExists(ExpandConstant('{app}\Vair.exe')) then
    begin
      Elevated := ShellExec('runas', ExpandConstant('{app}\Vair.exe'), '--uninstall-cleanup', '',
                            SW_HIDE, ewWaitUntilTerminated, ResultCode);
      { Elevation can be refused, or be impossible — a silent uninstall has
        nobody at the screen to answer the prompt. Then the cleanup ran not at
        all, so run it again without elevation: the registry entries, the
        system proxy and a copy started without administrator rights do not
        need it, and the program will say so itself if what is left does. }
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
        Abort;
      end;
    end;
    if FileExists(ExpandConstant('{app}\vair-helper.exe')) then
    begin
      if not Exec(ExpandConstant('{app}\vair-helper.exe'), '-uninstall-service', '', SW_HIDE, ewWaitUntilTerminated, ResultCode) then
        ResultCode := -1;
      if ResultCode <> 0 then
      begin
        SuppressibleMsgBox(FmtMessage(CustomMessage('ServiceRemoveFailed'), [IntToStr(ResultCode)]), mbError, MB_OK, IDOK);
        Abort;
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

