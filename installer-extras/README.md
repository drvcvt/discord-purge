# installer-extras

Files that get shipped with the NSIS installer but aren't part of the Go binary.

- **`daemon.vbs`** — wrapper that launches `purge.exe --daemon` with a hidden
  window. Invoked by the autostart registry entry and by the Equicord plugin
  when it wants to start the daemon without a visible console.

- **`autostart.reg`** — reference dump of the registry key the NSIS installer
  writes to `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`. Import it
  manually if the installer didn't run.
