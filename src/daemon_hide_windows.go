//go:build windows

package main

import "syscall"

// hideConsole frees the console inherited from the launcher (e.g. wscript.exe).
// This prevents the brief terminal flash that can appear when the Go binary
// starts under a hidden-window VBS wrapper. The daemon has no TUI of its own
// so losing the console is fine — all user-visible output happens in the
// spawned purge.exe children, which get their own CREATE_NEW_CONSOLE.
func hideConsole() {
    kernel32 := syscall.NewLazyDLL("kernel32.dll")
    proc := kernel32.NewProc("FreeConsole")
    _, _, _ = proc.Call()
}

// daemonPortBusy returns true when the daemon can't bind because another
// instance already owns the port. On Windows the underlying error is a
// WSAEADDRINUSE wrapped in a fmt.Errorf.
func daemonPortBusy(err error) bool {
    if err == nil {
        return false
    }
    s := err.Error()
    return contains(s, "address already in use") ||
        contains(s, "Only one usage of each socket address") ||
        contains(s, "bind:") && contains(s, "10048")
}

func contains(haystack, needle string) bool {
    return len(haystack) >= len(needle) && (len(needle) == 0 || indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
    for i := 0; i+len(needle) <= len(haystack); i++ {
        if haystack[i:i+len(needle)] == needle {
            return i
        }
    }
    return -1
}
