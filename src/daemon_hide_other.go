//go:build !windows

package main

// No-ops on non-Windows: the daemon is never launched via a hidden-window
// wrapper there, and the port-busy heuristic is handled by net/errors.
func hideConsole() {}

func daemonPortBusy(err error) bool {
    if err == nil {
        return false
    }
    s := err.Error()
    return len(s) > 0 && (indexOf(s, "address already in use") >= 0 || indexOf(s, "bind:") >= 0)
}

func indexOf(haystack, needle string) int {
    for i := 0; i+len(needle) <= len(haystack); i++ {
        if haystack[i:i+len(needle)] == needle {
            return i
        }
    }
    return -1
}
