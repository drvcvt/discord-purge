package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// PurgeOpts holds the non-filter execution options.
type PurgeOpts struct {
	DryRun     bool
	CountOnly  bool
	Workers    int
	ExportPath string
	Resume     bool
}

func main() {
	tokenFlag := flag.String("token", "", `Discord user token (omit=auto-detect, "auto"=force re-detect)`)
	guild := flag.String("guild", "", "Guild/server ID (omit=interactive)")
	channels := flag.String("channels", "", "Comma-separated channel IDs (omit=all text channels)")
	before := flag.String("before", "", "Only messages before date (YYYY-MM-DD or 30d/2w/6m/1y)")
	after := flag.String("after", "", "Only messages after date (YYYY-MM-DD or 30d/2w/6m/1y)")
	match := flag.String("match", "", `Keyword filter (prefix "regex:" for regex)`)
	typeFilter := flag.String("type", "all", "Type filter: all, attachments, links, embeds, text")
	dryRun := flag.Bool("dry-run", false, "Preview only, don't delete")
	countOnly := flag.Bool("count", false, "Scan and count only, don't delete")
	workers := flag.Int("workers", 10, "Number of concurrent scanner goroutines")
	export := flag.String("export", "", "Export messages to .jsonl before deleting")
	autoExport := flag.Bool("auto-export", true, "Auto-export to timestamped .jsonl file")
	resume := flag.Bool("resume", false, "Resume interrupted purge from checkpoint")
	includeThreads := flag.Bool("threads", false, "Include threads in scan")
	saveToken := flag.Bool("save-token", false, "Save resolved token for future use")
	forgetToken := flag.Bool("forget-token", false, "Delete saved token and exit")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "discord-purge — fast concurrent message scanner & deleter\n\n")
		fmt.Fprintf(os.Stderr, "Usage:\n")
		fmt.Fprintf(os.Stderr, "  purge [flags]                    interactive mode\n")
		fmt.Fprintf(os.Stderr, "  purge --guild <id> [flags]       CLI mode\n\n")
		fmt.Fprintf(os.Stderr, "Examples:\n")
		fmt.Fprintf(os.Stderr, "  purge                                            interactive TUI\n")
		fmt.Fprintf(os.Stderr, "  purge --guild 123456789                          scan entire server\n")
		fmt.Fprintf(os.Stderr, "  purge --guild 123 --channels 456,789 --dry-run   preview 2 channels\n")
		fmt.Fprintf(os.Stderr, "  purge --guild 123 --before 30d --workers 20      last 30 days, 20 workers\n")
		fmt.Fprintf(os.Stderr, "  purge --guild 123 --count                        count only, no delete\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	fmt.Println()
	fmt.Printf("  %s\n\n", colorBold("discord-purge"))

	// --forget-token
	if *forgetToken {
		path := configPath("token")
		if err := os.Remove(path); err != nil {
			fmt.Printf("  %s no saved token found\n", colorDim("~"))
		} else {
			fmt.Printf("  %s saved token deleted\n", colorGreen("OK"))
		}
		return
	}

	// Interactive mode when no --guild provided
	if *guild == "" && *channels == "" && flag.NArg() == 0 {
		tuiMain(*tokenFlag, *workers)
		return
	}

	// --- CLI mode ---

	if flag.NArg() > 0 && *channels == "" {
		*channels = strings.Join(flag.Args(), ",")
	}

	tok, user := ResolveToken(*tokenFlag)
	if tok == "" {
		os.Exit(1)
	}
	if *saveToken {
		SaveToken(tok)
	}

	client := NewClient(tok)

	if *guild == "" {
		fmt.Printf("  %s --guild is required in CLI mode (or run without flags for interactive)\n", colorRed("ERR"))
		os.Exit(1)
	}

	// Resolve channels
	var channelIDs []string
	if *channels != "" {
		for _, id := range strings.Split(*channels, ",") {
			id = strings.TrimSpace(id)
			if id != "" {
				channelIDs = append(channelIDs, id)
			}
		}
	} else {
		fmt.Printf("  %s fetching channels...\n", colorDim("..."))
		chs, err := client.GetGuildChannels(*guild)
		if err != nil {
			fmt.Printf("  %s could not fetch channels: %v\n", colorRed("ERR"), err)
			os.Exit(1)
		}
		for _, ch := range chs {
			if ch.Type == 0 || ch.Type == 5 {
				channelIDs = append(channelIDs, ch.ID)
			}
		}
		fmt.Printf("  %s found %d text channels\n", colorGreen("OK"), len(channelIDs))
	}

	if len(channelIDs) == 0 {
		fmt.Printf("  %s no channels to scan\n", colorYellow("!"))
		return
	}

	if *includeThreads {
		fmt.Printf("  %s discovering threads...\n", colorDim("..."))
		extra := DiscoverThreads(client, *guild, channelIDs)
		if len(extra) > 0 {
			channelIDs = append(channelIDs, extra...)
			fmt.Printf("  %s found %d threads\n", colorGreen("OK"), len(extra))
		}
	}

	// Build filter
	filter := Filter{
		AuthorID:   user.ID,
		TypeFilter: *typeFilter,
	}
	if *before != "" {
		t := parseDate(*before)
		if t == nil {
			fmt.Printf("  %s cannot parse --before date: %s\n", colorRed("ERR"), *before)
			os.Exit(1)
		}
		filter.Before = t
	}
	if *after != "" {
		t := parseDate(*after)
		if t == nil {
			fmt.Printf("  %s cannot parse --after date: %s\n", colorRed("ERR"), *after)
			os.Exit(1)
		}
		filter.After = t
	}
	if *match != "" {
		if strings.HasPrefix(*match, "regex:") {
			re, err := regexp.Compile((*match)[6:])
			if err != nil {
				fmt.Printf("  %s invalid regex: %v\n", colorRed("ERR"), err)
				os.Exit(1)
			}
			filter.Match = re
		} else {
			filter.Match = regexp.MustCompile(`(?i)` + regexp.QuoteMeta(*match))
		}
	}

	exportPath := *export
	if exportPath == "" && *autoExport && !*countOnly {
		exportPath = fmt.Sprintf("purge_export_%s.jsonl", timeNow().Format("20060102_150405"))
	}

	runPurge(client, user, *guild, channelIDs, filter, PurgeOpts{
		DryRun:     *dryRun,
		CountOnly:  *countOnly,
		Workers:    *workers,
		ExportPath: exportPath,
		Resume:     *resume,
	})
}

// runPurge is the shared execution path for both TUI and CLI mode.
func runPurge(client *Client, user *User, guildID string, channelIDs []string, filter Filter, opts PurgeOpts) {
	// Print config
	fmt.Printf("\n  %s\n", colorDim(strings.Repeat("-", 50)))
	fmt.Printf("  account:   %s %s\n", colorCyan(user.Username), colorDim("("+user.ID+")"))
	if guildID != "" {
		fmt.Printf("  guild:     %s\n", colorBold(guildID))
	}
	fmt.Printf("  channels:  %s\n", colorBold(strconv.Itoa(len(channelIDs))))
	fmt.Printf("  workers:   %s\n", colorBold(strconv.Itoa(opts.Workers)))
	if filter.Before != nil {
		fmt.Printf("  before:    %s\n", filter.Before.Format("2006-01-02"))
	}
	if filter.After != nil {
		fmt.Printf("  after:     %s\n", filter.After.Format("2006-01-02"))
	}
	if filter.Match != nil {
		fmt.Printf("  match:     %s\n", colorBold(filter.Match.String()))
	}
	if filter.TypeFilter != "all" {
		fmt.Printf("  type:      %s\n", colorBold(filter.TypeFilter))
	}
	if opts.DryRun {
		fmt.Printf("  mode:      %s\n", colorYellow("DRY RUN"))
	}
	if opts.CountOnly {
		fmt.Printf("  mode:      %s\n", colorCyan("COUNT ONLY"))
	}
	if opts.ExportPath != "" && !opts.CountOnly {
		fmt.Printf("  export:    %s\n", colorDim(opts.ExportPath))
	}
	fmt.Printf("  %s\n\n", colorDim(strings.Repeat("-", 50)))

	// Checkpoint
	checkpoint := NewCheckpoint()
	if opts.Resume {
		saved := checkpoint.Load()
		if len(saved) > 0 {
			fmt.Printf("  %s resuming from checkpoint (%d channels)\n", colorCyan(">>"), len(saved))
		} else {
			fmt.Printf("  %s no checkpoint found, starting fresh\n", colorDim("~"))
		}
	}

	stats := NewStats(len(channelIDs))

	// Ctrl+C handler
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	go func() {
		<-sigCh
		fmt.Printf("\n\n  %s saving checkpoint...\n", colorYellow("!"))
		checkpoint.Save()
		printFinalStats(stats, opts.ExportPath)
		os.Exit(130)
	}()

	// Progress ticker
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				printProgress(stats)
			case <-done:
				return
			}
		}
	}()

	// Run
	scanner := NewScanner(client, filter, opts.Workers, stats)
	scanner.checkpoint = checkpoint
	exportPath := opts.ExportPath
	if opts.CountOnly {
		exportPath = ""
	}

	if guildID != "" && !opts.CountOnly {
		// Search-based scan: faster for guilds (finds user's messages directly)
		scanner.ScanAndDeleteViaSearch(guildID, channelIDs, opts.DryRun, exportPath)
	} else {
		// Channel-based scan: for DMs or count-only mode
		scanner.ScanAndDelete(channelIDs, opts.DryRun, exportPath, opts.CountOnly)
	}

	close(done)

	if stats.Deleted.Load() > 0 && !opts.DryRun && !opts.CountOnly {
		checkpoint.Clear()
	}

	printFinalStats(stats, opts.ExportPath)
}

// --- Helpers ---

func timeNow() time.Time { return time.Now() }


func configPath(name string) string {
	return os.Getenv("USERPROFILE") + "/.config/discord-purger/" + name
}

// --- Progress display ---

func printProgress(s *Stats) {
	scanned := s.Scanned.Load()
	matched := s.Matched.Load()
	deleted := s.Deleted.Load()
	failed := s.Failed.Load()
	doneCh := s.DoneChannels.Load()
	elapsed := time.Since(s.StartTime).Truncate(time.Second)
	rate := float64(scanned) / max(time.Since(s.StartTime).Seconds(), 0.1)

	parts := []string{
		fmt.Sprintf("%s scanned", colorBold(fmtCount(int(scanned)))),
		fmt.Sprintf("%s matched", colorCyan(fmtCount(int(matched)))),
		fmt.Sprintf("%s deleted", colorGreen(fmtCount(int(deleted)))),
	}
	if failed > 0 {
		parts = append(parts, fmt.Sprintf("%s failed", colorRed(fmtCount(int(failed)))))
	}
	parts = append(parts,
		fmt.Sprintf("%d/%d ch", doneCh, s.TotalCh),
		fmt.Sprintf("%.0f/s", rate),
		elapsed.String(),
	)
	fmt.Printf("\r\033[K  %s %s %s", colorDim("--"), strings.Join(parts, colorDim(" | ")), colorDim("--"))
}

func printFinalStats(s *Stats, exportPath string) {
	fmt.Printf("\r\033[K\n")
	elapsed := time.Since(s.StartTime).Truncate(time.Millisecond)
	scanned := s.Scanned.Load()
	rate := float64(scanned) / max(time.Since(s.StartTime).Seconds(), 0.1)

	fmt.Printf("  %s\n", colorBold("results"))
	fmt.Printf("    scanned:  %s\n", colorBold(fmtCount(int(scanned))))
	fmt.Printf("    matched:  %s\n", colorCyan(fmtCount(int(s.Matched.Load()))))
	fmt.Printf("    deleted:  %s\n", colorGreen(fmtCount(int(s.Deleted.Load()))))
	if s.Failed.Load() > 0 {
		fmt.Printf("    failed:   %s\n", colorRed(fmtCount(int(s.Failed.Load()))))
	}
	fmt.Printf("    channels: %d\n", s.TotalCh)
	fmt.Printf("    time:     %s\n", elapsed)
	fmt.Printf("    rate:     %.0f msgs/sec\n", rate)
	if exportPath != "" {
		fmt.Printf("    export:   %s\n", colorDim(exportPath))
	}
	fmt.Println()
}

// --- Date parsing ---

func parseDate(s string) *time.Time {
	if t := parseRelativeDate(s); t != nil {
		return t
	}
	return parseAbsoluteDate(s)
}

func parseRelativeDate(s string) *time.Time {
	if len(s) < 2 {
		return nil
	}
	unit := s[len(s)-1]
	n, err := strconv.Atoi(s[:len(s)-1])
	if err != nil || n <= 0 {
		return nil
	}
	now := time.Now().UTC()
	var t time.Time
	switch unit {
	case 'd':
		t = now.AddDate(0, 0, -n)
	case 'w':
		t = now.AddDate(0, 0, -n*7)
	case 'm':
		t = now.AddDate(0, -n, 0)
	case 'y':
		t = now.AddDate(-n, 0, 0)
	default:
		return nil
	}
	return &t
}

func parseAbsoluteDate(s string) *time.Time {
	for _, layout := range []string{"2006-01-02", "02.01.2006", "02/01/2006"} {
		if t, err := time.Parse(layout, s); err == nil {
			t = t.UTC()
			return &t
		}
	}
	return nil
}
