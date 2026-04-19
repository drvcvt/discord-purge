package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// createNewConsole is the Windows CREATE_NEW_CONSOLE process creation flag.
// See: https://learn.microsoft.com/en-us/windows/win32/procthread/process-creation-flags
const createNewConsole = 0x00000010

// daemonAddr is the loopback bind address for the HTTP bridge.
// The Equicord plugin hardcodes this port, keep them in sync.
const daemonAddr = "127.0.0.1:48654"

type purgeRequest struct {
	ChannelID string `json:"channel_id"`
	GuildID   string `json:"guild_id"`
	ChannelName string `json:"channel_name"`
	DryRun    bool   `json:"dry_run"`
	Match     string `json:"match"`
	Before    string `json:"before"`
	After     string `json:"after"`
	Type      string `json:"type"`
}

type purgeResponse struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
	Command string `json:"command,omitempty"`
}

// runDaemon starts the local HTTP bridge used by the Equicord plugin.
// It listens only on 127.0.0.1 and spawns a new console window for each
// incoming purge request so the user sees the familiar TUI-style progress.
func runDaemon() {
	// Detach from any console that was allocated by the launcher (wscript.exe,
	// NSIS Exec, etc). Without this, the Go runtime can briefly flash a
	// terminal window before we'd otherwise run fully headless. Must happen
	// before the first fmt.Printf so we don't crash on stdout-to-closed-fd.
	hideConsole()

	exe, err := os.Executable()
	if err != nil {
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/ping", handlePing)
	mux.HandleFunc("/purge", func(w http.ResponseWriter, r *http.Request) {
		handlePurge(w, r, exe)
	})

	srv := &http.Server{
		Addr:              daemonAddr,
		Handler:           corsMiddleware(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		// Exit silently on port-in-use — another daemon already owns the port,
		// which is the common case when the installer starts us while an older
		// instance is still alive. The existing daemon keeps serving.
		if daemonPortBusy(err) {
			os.Exit(0)
		}
		os.Exit(1)
	}
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func handlePing(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"ok":      true,
		"name":    "discord-purge",
		"version": 1,
	})
}

func handlePurge(w http.ResponseWriter, r *http.Request, exe string) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req purgeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	req.ChannelID = strings.TrimSpace(req.ChannelID)
	req.GuildID = strings.TrimSpace(req.GuildID)
	if !isSnowflake(req.ChannelID) {
		writeErr(w, http.StatusBadRequest, "channel_id missing or invalid")
		return
	}
	if req.GuildID != "" && !isSnowflake(req.GuildID) {
		writeErr(w, http.StatusBadRequest, "guild_id invalid")
		return
	}

	args := buildPurgeArgs(req)
	cmdLine := exe + " " + strings.Join(args, " ")

	if err := spawnConsole(exe, args); err != nil {
		writeErr(w, http.StatusInternalServerError, "spawn failed: "+err.Error())
		return
	}

	fmt.Printf("  %s spawned purge for %s %s\n",
		colorGreen(">>"),
		colorCyan("#"+defaultIfEmpty(req.ChannelName, req.ChannelID)),
		colorDim("("+modeLabel(req)+")"),
	)

	json.NewEncoder(w).Encode(purgeResponse{
		OK:      true,
		Message: "purge started",
		Command: cmdLine,
	})
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(purgeResponse{OK: false, Message: msg})
}

func buildPurgeArgs(req purgeRequest) []string {
	args := []string{"--channels", req.ChannelID}
	if req.GuildID != "" {
		args = append(args, "--guild", req.GuildID)
	}
	if req.DryRun {
		args = append(args, "--dry-run")
	}
	if req.Match != "" {
		args = append(args, "--match", req.Match)
	}
	if req.Before != "" {
		args = append(args, "--before", req.Before)
	}
	if req.After != "" {
		args = append(args, "--after", req.After)
	}
	if req.Type != "" && req.Type != "all" {
		args = append(args, "--type", req.Type)
	}
	return args
}

func modeLabel(req purgeRequest) string {
	parts := []string{}
	if req.DryRun {
		parts = append(parts, "dry-run")
	} else {
		parts = append(parts, "delete")
	}
	if req.Match != "" {
		parts = append(parts, "match="+req.Match)
	}
	if req.Before != "" {
		parts = append(parts, "before="+req.Before)
	}
	if req.After != "" {
		parts = append(parts, "after="+req.After)
	}
	if req.Type != "" && req.Type != "all" {
		parts = append(parts, "type="+req.Type)
	}
	return strings.Join(parts, " ")
}

// spawnConsole launches a new Windows console window running purge.exe with
// the given arguments. We bypass cmd.exe entirely (quoting there is cursed)
// and use CREATE_NEW_CONSOLE to give the child its own console. The child
// is invoked with --pause-on-exit so the window stays open for the user to
// read the final stats.
func spawnConsole(exe string, args []string) error {
	args = append([]string{"--pause-on-exit", "--verbose"}, args...)
	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewConsole}
	return cmd.Start()
}

func isSnowflake(s string) bool {
	if len(s) < 15 || len(s) > 25 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func defaultIfEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
