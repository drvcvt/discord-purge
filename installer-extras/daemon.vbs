' discord-purge daemon launcher
'
' Runs purge.exe --daemon with a hidden window so the background HTTP bridge
' stays alive without flashing a console at every Windows login. The NSIS
' installer drops this file alongside purge.exe and registers it under
' HKCU\Software\Microsoft\Windows\CurrentVersion\Run for autostart.
'
' The Equicord plugin expects the daemon on http://127.0.0.1:48654.
' Console popups only appear during actual delete operations (the daemon
' uses CREATE_NEW_CONSOLE when spawning the purge TUI), which is the
' intended behaviour.

Option Explicit

Dim shell, fso, scriptDir, exePath

Set shell = CreateObject("WScript.Shell")
Set fso   = CreateObject("Scripting.FileSystemObject")

scriptDir = fso.GetParentFolderName(WScript.ScriptFullName)
exePath   = scriptDir & "\purge.exe"

If Not fso.FileExists(exePath) Then
    WScript.Quit 1
End If

' 0 = hidden, False = don't wait for exit
shell.Run """" & exePath & """ --daemon", 0, False
