!include "MUI2.nsh"

Name "discord-purge"
OutFile "discord-purge-setup.exe"
InstallDir "$LOCALAPPDATA\discord-purge"
RequestExecutionLevel user

!define MUI_ICON "${NSISDIR}\Contrib\Graphics\Icons\modern-install.ico"

!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "English"

Section "Install"
    SetOutPath "$INSTDIR"
    File "purge.exe"

    ; Create uninstaller
    WriteUninstaller "$INSTDIR\uninstall.exe"

    ; Start menu shortcut
    CreateDirectory "$SMPROGRAMS\discord-purge"
    CreateShortcut "$SMPROGRAMS\discord-purge\discord-purge.lnk" "$INSTDIR\purge.exe"
    CreateShortcut "$SMPROGRAMS\discord-purge\Uninstall.lnk" "$INSTDIR\uninstall.exe"

    ; Add to PATH
    EnVar::AddValue "PATH" "$INSTDIR"
SectionEnd

Section "Uninstall"
    Delete "$INSTDIR\purge.exe"
    Delete "$INSTDIR\uninstall.exe"
    RMDir "$INSTDIR"

    Delete "$SMPROGRAMS\discord-purge\discord-purge.lnk"
    Delete "$SMPROGRAMS\discord-purge\Uninstall.lnk"
    RMDir "$SMPROGRAMS\discord-purge"

    EnVar::DeleteValue "PATH" "$INSTDIR"
SectionEnd
