!include "MUI2.nsh"

Name "discord-purge"
OutFile "discord-purge-setup.exe"
InstallDir "$LOCALAPPDATA\discord-purge"
RequestExecutionLevel user
SetCompressor /SOLID lzma

!define MUI_ICON "${NSISDIR}\Contrib\Graphics\Icons\modern-install.ico"
!define AUTOSTART_KEY "Software\Microsoft\Windows\CurrentVersion\Run"
!define AUTOSTART_NAME "DiscordPurgeDaemon"

!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "English"

Section "Install"
    SetOutPath "$INSTDIR"
    File "purge.exe"
    File "installer-extras\daemon.vbs"

    ; Uninstaller
    WriteUninstaller "$INSTDIR\uninstall.exe"

    ; Start menu shortcuts
    CreateDirectory "$SMPROGRAMS\discord-purge"
    CreateShortcut "$SMPROGRAMS\discord-purge\discord-purge.lnk" "$INSTDIR\purge.exe"
    CreateShortcut "$SMPROGRAMS\discord-purge\Uninstall.lnk" "$INSTDIR\uninstall.exe"

    ; Register autostart: wscript.exe runs daemon.vbs hidden at every login.
    ; User-scope (HKCU), no UAC, no admin required.
    WriteRegStr HKCU "${AUTOSTART_KEY}" "${AUTOSTART_NAME}" 'wscript.exe "$INSTDIR\daemon.vbs"'

    ; Kick the daemon off now via the same wrapper so users don't need to
    ; reboot or run anything manually. Hidden by default (wscript).
    Exec 'wscript.exe "$INSTDIR\daemon.vbs"'

SectionEnd

Section "Uninstall"
    ; Best-effort shutdown of any running daemon before we pull files.
    ExecWait 'taskkill /F /IM purge.exe'

    DeleteRegValue HKCU "${AUTOSTART_KEY}" "${AUTOSTART_NAME}"

    Delete "$INSTDIR\purge.exe"
    Delete "$INSTDIR\daemon.vbs"
    Delete "$INSTDIR\uninstall.exe"
    RMDir "$INSTDIR"

    Delete "$SMPROGRAMS\discord-purge\discord-purge.lnk"
    Delete "$SMPROGRAMS\discord-purge\Uninstall.lnk"
    RMDir "$SMPROGRAMS\discord-purge"
SectionEnd
