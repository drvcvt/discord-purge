package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Scanner orchestrates concurrent channel scanning and message deletion.
type Scanner struct {
	client  *Client
	filter  Filter
	stats   *Stats
	workers int
	dryRun  bool
	export  string // export file path, empty = no export

	checkpoint *Checkpoint
	exportMu   sync.Mutex
	exportFile *os.File
	deleteLog  chan string // buffered channel for deletion previews (TUI reads this)
}

func NewScanner(client *Client, filter Filter, workers int, stats *Stats) *Scanner {
	return &Scanner{
		client:  client,
		filter:  filter,
		workers: workers,
		stats:   stats,
	}
}

// ScanAndDelete scans all channels concurrently, filters messages, and deletes them.
// This is the main pipeline: scan → filter → delete
func (s *Scanner) ScanAndDelete(channelIDs []string, dryRun bool, exportPath string, countOnly bool) {
	s.dryRun = dryRun
	s.export = exportPath

	if exportPath != "" {
		f, err := os.Create(exportPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s could not create export file: %v\n", colorRed("ERR"), err)
		} else {
			s.exportFile = f
			defer f.Close()
		}
	}

	// Buffered channel for matched messages
	matched := make(chan Message, 1000)

	// Start scanner goroutines
	var scanWg sync.WaitGroup
	sem := make(chan struct{}, s.workers) // concurrency limiter

	for _, chID := range channelIDs {
		scanWg.Add(1)
		go func(channelID string) {
			defer scanWg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			s.scanChannel(channelID, matched, countOnly)
		}(chID)
	}

	// Close matched channel when all scanners done
	go func() {
		scanWg.Wait()
		close(matched)
	}()

	if countOnly {
		// Just drain the channel, counting is done atomically
		for range matched {
		}
		return
	}

	// Start deleter goroutines
	var delWg sync.WaitGroup
	// Use up to workers deleters, but cap at channel count
	numDeleters := s.workers
	if numDeleters > len(channelIDs) {
		numDeleters = len(channelIDs)
	}
	if numDeleters < 1 {
		numDeleters = 1
	}
	for i := 0; i < numDeleters; i++ {
		delWg.Add(1)
		go func() {
			defer delWg.Done()
			s.deleteWorker(matched)
		}()
	}
	delWg.Wait()
}

func (s *Scanner) scanChannel(channelID string, out chan<- Message, countOnly bool) {
	before := ""
	if s.filter.Before != nil {
		before = TimeToSnowflake(*s.filter.Before)
	}

	// Check checkpoint for resume point
	if s.checkpoint != nil {
		if lastID := s.checkpoint.Get(channelID); lastID != "" {
			// We scan newest-first, so set "before" to checkpoint ID to skip already-processed
			// Actually for resume we want to continue from where we left off
			// The checkpoint stores the last DELETED message ID
			// Since we paginate backwards (newest first), we need before=checkpoint to skip ahead
			before = lastID
		}
	}

	for {
		msgs, err := s.client.GetMessages(channelID, 100, before)
		if err != nil {
			break
		}
		if len(msgs) == 0 {
			break
		}

		for i := range msgs {
			msg := &msgs[i]
			msg.ChannelID = channelID // ensure it's set
			s.stats.Scanned.Add(1)

			if !s.matchMessage(msg) {
				s.stats.Filtered.Add(1)
				continue
			}

			s.stats.Matched.Add(1)

			if s.exportFile != nil {
				s.exportMessage(msg)
			}

			if !countOnly {
				out <- *msg
			}
		}

		// Paginate: use last message ID as "before"
		before = msgs[len(msgs)-1].ID
	}

	s.stats.DoneChannels.Add(1)
}

func (s *Scanner) matchMessage(msg *Message) bool {
	// Author filter
	if s.filter.AuthorID != "" && msg.Author.ID != s.filter.AuthorID {
		return false
	}

	// Date filters (using snowflake timestamps)
	if s.filter.After != nil {
		msgTime := SnowflakeToTime(msg.ID)
		if msgTime.Before(*s.filter.After) {
			return false
		}
	}

	// Content match
	if s.filter.Match != nil {
		if !s.filter.Match.MatchString(msg.Content) {
			return false
		}
	}

	// Type filter
	switch s.filter.TypeFilter {
	case "attachments":
		if len(msg.Attachments) == 0 {
			return false
		}
	case "links":
		if !linkRe.MatchString(msg.Content) {
			return false
		}
	case "embeds":
		if len(msg.Embeds) == 0 {
			return false
		}
	case "text":
		if len(msg.Attachments) > 0 || len(msg.Embeds) > 0 {
			return false
		}
	}

	return true
}

var linkRe = regexp.MustCompile(`https?://`)

func (s *Scanner) deleteWorker(msgs <-chan Message) {
	for msg := range msgs {
		if s.dryRun {
			s.stats.Deleted.Add(1)
			preview := msg.Content
			if len(preview) > 60 {
				preview = preview[:60] + "..."
			}
			preview = strings.ReplaceAll(preview, "\n", " ")
			if preview == "" {
				preview = "<embed/attachment>"
			}
			ts := SnowflakeToTime(msg.ID).Format("2006-01-02 15:04")
			printLog("DRY", colorYellow, ts, preview, "")
			continue
		}

		err := s.client.DeleteMessage(msg.ChannelID, msg.ID)
		if err != nil {
			s.stats.Failed.Add(1)
			continue
		}

		s.stats.Deleted.Add(1)
		if s.checkpoint != nil {
			s.checkpoint.Update(msg.ChannelID, msg.ID)
		}
	}
}

func (s *Scanner) exportMessage(msg *Message) {
	s.exportMu.Lock()
	defer s.exportMu.Unlock()

	record := map[string]any{
		"id":          msg.ID,
		"channel_id":  msg.ChannelID,
		"timestamp":   msg.Timestamp,
		"content":     msg.Content,
		"attachments": msg.Attachments,
	}
	data, _ := json.Marshal(record)
	s.exportFile.Write(data)
	s.exportFile.Write([]byte("\n"))
}

// DiscoverThreads finds all threads (active + archived) for the given channels in a guild.
func DiscoverThreads(client *Client, guildID string, channelIDs []string) []string {
	seen := make(map[string]bool, len(channelIDs))
	for _, id := range channelIDs {
		seen[id] = true
	}

	var extra []string
	var mu sync.Mutex

	// Active threads
	threads, _ := client.GetActiveThreads(guildID)
	for _, t := range threads {
		if seen[t.ParentID] && !seen[t.ID] {
			mu.Lock()
			extra = append(extra, t.ID)
			seen[t.ID] = true
			mu.Unlock()
		}
	}

	// Archived threads (parallel per channel)
	var wg sync.WaitGroup
	for _, chID := range channelIDs {
		wg.Add(1)
		go func(cid string) {
			defer wg.Done()
			for _, kind := range []string{"public", "private"} {
				threads, _ := client.GetArchivedThreads(cid, kind)
				for _, t := range threads {
					mu.Lock()
					if !seen[t.ID] {
						extra = append(extra, t.ID)
						seen[t.ID] = true
					}
					mu.Unlock()
				}
			}
		}(chID)
	}
	wg.Wait()

	return extra
}

// PreScan uses the search API to find which channels have messages from a user.
// Fetches page 0 to get total_results, then dispatches remaining pages concurrently
// with a concurrency limit of 2. The client's rate-limit handling covers 429s naturally.
func PreScan(client *Client, guildID, authorID string, onProgress func(found, total int)) (map[string]int, int) {
	// Phase 1: first page to learn total
	sr, err := client.SearchGuildMessages(guildID, authorID, 0)
	if err != nil || sr == nil || len(sr.Messages) == 0 {
		return nil, 0
	}

	total := sr.TotalResults
	if total == 0 {
		return nil, 0
	}

	counts := make(map[string]int)
	var mu sync.Mutex

	for _, group := range sr.Messages {
		for _, msg := range group {
			if msg.Hit {
				counts[msg.ChannelID]++
			}
		}
	}

	firstBatch := 25
	if firstBatch > total {
		firstBatch = total
	}
	if onProgress != nil {
		onProgress(firstBatch, total)
	}

	if total <= 25 {
		return counts, total
	}

	// Phase 2: remaining pages concurrently
	totalPages := (total + 24) / 25
	maxPages := totalPages
	if maxPages > 20 {
		maxPages = 20 // cap sample at 500 messages
	}

	sem := make(chan struct{}, 2)
	var wg sync.WaitGroup
	var done atomic.Int32
	done.Store(1) // page 0 already fetched

	for page := 1; page < maxPages; page++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			offset := p * 25
			sr, err := client.SearchGuildMessages(guildID, authorID, offset)
			if err != nil || sr == nil || len(sr.Messages) == 0 {
				done.Add(1)
				return
			}

			mu.Lock()
			for _, group := range sr.Messages {
				for _, msg := range group {
					if msg.Hit {
						counts[msg.ChannelID]++
					}
				}
			}
			mu.Unlock()

			d := int(done.Add(1))
			if onProgress != nil {
				prog := d * 25
				if prog > total {
					prog = total
				}
				onProgress(prog, total)
			}
		}(page)
	}

	wg.Wait()

	if onProgress != nil {
		onProgress(total, total)
	}

	// Extrapolate from sample
	sampled := 0
	for _, c := range counts {
		sampled += c
	}
	if sampled > 0 && total > sampled {
		ratio := float64(total) / float64(sampled)
		for k, v := range counts {
			counts[k] = int(float64(v) * ratio)
		}
	}

	return counts, total
}

// ScanAndDeleteViaSearch uses the Discord search API to find and delete
// the user's messages directly. Much faster than channel pagination when
// the user has few messages scattered across large channels.
// Uses multi-pass: paginate all results, delete matches, repeat until no more.
func (s *Scanner) ScanAndDeleteViaSearch(guildID string, channelIDs []string, dryRun bool, exportPath string) {
	s.dryRun = dryRun
	s.export = exportPath

	if exportPath != "" {
		f, err := os.Create(exportPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s could not create export file: %v\n", colorRed("ERR"), err)
		} else {
			s.exportFile = f
			defer f.Close()
		}
	}

	channelSet := make(map[string]bool, len(channelIDs))
	for _, id := range channelIDs {
		channelSet[id] = true
	}

	// Build search options from date filters
	var searchOpt SearchOpt
	if s.filter.After != nil {
		searchOpt.MinID = TimeToSnowflake(*s.filter.After)
	}
	if s.filter.Before != nil {
		searchOpt.MaxID = TimeToSnowflake(*s.filter.Before)
	}

	// Multi-pass: paginate all search results, delete matches.
	// After a pass with deletions, wait for index update and do another pass.
	// Stop when a full pass finds nothing new to delete.
	for pass := 0; pass < 50; pass++ {
		deletedThisPass := 0
		seen := make(map[string]bool)

		for offset := 0; ; offset += 25 {
			sr, err := s.client.SearchGuildMessages(guildID, s.filter.AuthorID, offset, searchOpt)
			if err != nil || sr == nil || sr.TotalResults == 0 || len(sr.Messages) == 0 {
				break
			}

			s.stats.TotalCh = int64(sr.TotalResults)

			for _, group := range sr.Messages {
				for _, msg := range group {
					if !msg.Hit || seen[msg.ID] {
						continue
					}
					seen[msg.ID] = true
					s.stats.Scanned.Add(1)

					// Channel filter
					if len(channelSet) > 0 && !channelSet[msg.ChannelID] {
						s.stats.Filtered.Add(1)
						continue
					}

					// Before filter (not checked in matchMessage)
					if s.filter.Before != nil {
						msgTime := SnowflakeToTime(msg.ID)
						if msgTime.After(*s.filter.Before) {
							s.stats.Filtered.Add(1)
							continue
						}
					}

					if !s.matchMessage(&msg) {
						s.stats.Filtered.Add(1)
						continue
					}

					s.stats.Matched.Add(1)

					if s.exportFile != nil {
						s.exportMessage(&msg)
					}

					// Log deletion for TUI
					if s.deleteLog != nil {
						preview := msg.Content
						if len(preview) > 80 {
							preview = preview[:80] + "..."
						}
						preview = strings.ReplaceAll(preview, "\n", " ")
						if preview == "" {
							preview = "<embed/attachment>"
						}
						select {
						case s.deleteLog <- preview:
						default: // don't block if buffer full
						}
					}

					if dryRun {
						s.stats.Deleted.Add(1)
						deletedThisPass++
						continue
					}

					if err := s.client.DeleteMessage(msg.ChannelID, msg.ID); err != nil {
						s.stats.Failed.Add(1)
					} else {
						s.stats.Deleted.Add(1)
						deletedThisPass++
					}
				}
			}

			s.stats.DoneChannels.Store(s.stats.Scanned.Load())

			if offset+25 >= sr.TotalResults {
				break
			}
		}

		if deletedThisPass == 0 {
			break // nothing more to delete
		}

		if dryRun {
			break // dry run: one pass is enough, messages don't disappear
		}

		// Wait for search index to update before next pass
		time.Sleep(3 * time.Second)
	}
}

func fmtCount(n int) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 10_000 {
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	}
	return fmt.Sprintf("%d", n)
}
