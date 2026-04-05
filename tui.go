package main

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ── Styles ──────────────────────────────────────────────────────────────────

var (
	styleBold   = lipgloss.NewStyle().Bold(true)
	styleDim    = lipgloss.NewStyle().Faint(true)
	styleCyan   = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	styleGreen  = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	styleYellow = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	styleRed    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	styleCursor = lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true)
)

// ── Screens ─────────────────────────────────────────────────────────────────

type screen int

const (
	screenLoading screen = iota
	screenAccounts
	screenTarget
	screenDMs
	screenServers
	screenServerMode
	screenChannels
	screenSmartScanning
	screenSmartResults
	screenEnterID
	screenOptions
	screenConfirm
	screenRunning
	screenDone
)

// ── Messages ────────────────────────────────────────────────────────────────

type accountEntry struct {
	Label string
	Token string
	User  *User
}

type accountsLoadedMsg []accountEntry
type guildsLoadedMsg []Guild
type dmsLoadedMsg []DMChannel
type channelsLoadedMsg []Channel
// sharedScanProg is written by the scan goroutine, read by the TUI tick
type sharedScanProg struct {
	found atomic.Int64
	total atomic.Int64
}

type scanTickMsg time.Time
type smartScanDoneMsg struct {
	counts map[string]int
	total  int
}
type threadsFoundMsg []string
type purgeDoneMsg struct{}
type purgeTickMsg time.Time
type errMsg struct{ err error }

// ── List item ───────────────────────────────────────────────────────────────

type item struct {
	title  string
	desc   string
	id     string
	header bool // non-selectable category
	count  int  // for smart scan
}

// ── Options ─────────────────────────────────────────────────────────────────

type optField int

const (
	optDryRun optField = iota
	optKeyword
	optTypeFilter
	optBefore
	optAfter
	optThreads
	optExport
	optStart
)

// ── Model ───────────────────────────────────────────────────────────────────

type model struct {
	screen   screen
	width    int
	height   int
	quitting bool
	err      error

	// config
	workers   int
	tokenFlag string

	// data
	accounts []accountEntry
	client   *Client
	user     *User
	guilds   []Guild
	channels []Channel
	dms      []DMChannel

	// selection
	guildID     string
	guildName   string
	channelIDs  []string
	targetLabel string

	// list state (reused across screens)
	items    []item
	cursor   int
	selected map[int]bool
	filter   string

	// text input (for channel ID entry and option text fields)
	input textinput.Model

	// options form
	optCursor  optField
	optDryRun  bool
	optKeyword string
	optType    int // index into typeFilters
	optBefore  string
	optAfter   string
	optThreads bool
	optExport  bool
	hasGuild   bool

	// smart scan
	scanFound    int
	scanTotal    int
	scanProg     *sharedScanProg // shared with goroutine
	scanCounts   map[string]int
	scanRawTotal int

	// loading
	loadingMsg string

	// purge
	purgeFilter  Filter
	stats        *Stats
	exportPath   string
	purgeElapsed time.Duration
	deleteLog    chan string   // shared with scanner goroutine
	recentDeletes []string    // last N deleted message previews

	// components
	spinner  spinner.Model
	progress progress.Model
}

var typeFilters = []string{"all", "attachments", "links", "embeds", "text"}

func newModel(tokenFlag string, workers int) model {
	sp := spinner.New()
	sp.Spinner = spinner.Dot

	ti := textinput.New()
	ti.CharLimit = 100

	prog := progress.New(progress.WithDefaultGradient())

	return model{
		screen:     screenLoading,
		loadingMsg: "scanning for accounts",
		workers:    workers,
		tokenFlag:  tokenFlag,
		selected:  make(map[int]bool),
		optExport: true,
		spinner:   sp,
		input:     ti,
		progress:  prog,
	}
}

// ── Init ────────────────────────────────────────────────────────────────────

func (m model) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, loadAccounts(m.tokenFlag))
}

func loadAccounts(tokenFlag string) tea.Cmd {
	return func() tea.Msg {
		accounts := DetectAccounts(nil)
		if len(accounts) == 0 {
			return accountsLoadedMsg(nil)
		}
		var result []accountEntry
		for _, a := range accounts {
			result = append(result, accountEntry{Label: a.Label, Token: a.Token, User: a.User})
		}
		return accountsLoadedMsg(result)
	}
}

// ── Update ──────────────────────────────────────────────────────────────────

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.progress.Width = msg.Width - 8
		if m.progress.Width > 60 {
			m.progress.Width = 60
		}
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		}

	case errMsg:
		m.err = msg.err
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	// ── Global async result handlers (screen-independent) ──
	case accountsLoadedMsg:
		m.accounts = msg
		if len(msg) == 0 {
			m.err = fmt.Errorf("no Discord accounts found — make sure Discord is installed and you're logged in")
			return m, tea.Quit
		}
		m.items = make([]item, len(msg))
		for i, a := range msg {
			m.items[i] = item{title: a.User.Username, desc: a.Label}
		}
		m.cursor = 0
		m.screen = screenAccounts
		return m, nil

	case guildsLoadedMsg:
		m.guilds = msg
		m.items = make([]item, len(msg))
		for i, g := range msg {
			m.items[i] = item{title: g.Name, desc: g.ID, id: g.ID}
		}
		m.cursor = 0
		m.filter = ""
		m.screen = screenServers
		return m, nil

	case dmsLoadedMsg:
		m.dms = msg
		m.items = make([]item, len(msg))
		for i, dm := range msg {
			m.items[i] = item{title: dmDisplayName(dm), desc: dmTypeLabel(dm), id: dm.ID}
		}
		m.cursor = 0
		m.selected = make(map[int]bool)
		m.filter = ""
		m.screen = screenDMs
		return m, nil

	case channelsLoadedMsg:
		m.channels = msg
		m.items = buildChannelTree(msg)
		m.cursor = 0
		m.selected = make(map[int]bool)
		m.filter = ""
		m.screen = screenChannels
		for m.cursor < len(m.items) && m.items[m.cursor].header {
			m.cursor++
		}
		return m, nil

	case scanTickMsg:
		if m.screen == screenSmartScanning && m.scanProg != nil {
			m.scanFound = int(m.scanProg.found.Load())
			m.scanTotal = int(m.scanProg.total.Load())
			return m, tickScan()
		}
		return m, nil

	case smartScanDoneMsg:
		return m.handleSmartScanDone(msg)

	case threadsFoundMsg:
		m.channelIDs = append(m.channelIDs, msg...)
		return m.launchPurge()

	case purgeTickMsg:
		if m.screen == screenRunning {
			// Drain delete log
			if m.deleteLog != nil {
				for {
					select {
					case preview := <-m.deleteLog:
						m.recentDeletes = append(m.recentDeletes, preview)
						if len(m.recentDeletes) > 8 {
							m.recentDeletes = m.recentDeletes[len(m.recentDeletes)-8:]
						}
					default:
						goto drained
					}
				}
			drained:
			}
			return m, tickPurge()
		}
		return m, nil

	case purgeDoneMsg:
		m.screen = screenDone
		if m.stats != nil {
			m.purgeElapsed = time.Since(m.stats.StartTime)
		}
		return m, nil
	}

	// ── Screen-specific key event handling ──
	switch m.screen {
	case screenAccounts:
		if key, ok := msg.(tea.KeyMsg); ok && key.String() == "esc" {
			m.quitting = true
			return m, tea.Quit
		}
		return m.updateList(msg, m.onAccountSelected)
	case screenTarget:
		return m.updateList(msg, m.onTargetSelected)
	case screenDMs:
		return m.updateMultiList(msg, m.onDMsSelected)
	case screenServers:
		return m.updateList(msg, m.onServerSelected)
	case screenServerMode:
		return m.updateList(msg, m.onServerModeSelected)
	case screenChannels:
		return m.updateMultiList(msg, m.onChannelsSelected)
	case screenSmartResults:
		return m.updateMultiList(msg, m.onSmartResultsSelected)
	case screenEnterID:
		return m.updateEnterID(msg)
	case screenOptions:
		return m.updateOptions(msg)
	case screenConfirm:
		if key, ok := msg.(tea.KeyMsg); ok {
			switch key.String() {
			case "enter", "y":
				return m.confirmPurge()
			case "esc", "n":
				return m.enterOptionsScreen()
			}
		}
		return m, nil
	case screenDone:
		return m.updateDone(msg)
	}
	return m, nil
}

func (m model) onAccountSelected(idx int) (tea.Model, tea.Cmd) {
	a := m.accounts[idx]
	m.client = NewClient(a.Token)
	m.user = a.User
	SaveToken(a.Token)
	return m.enterTargetScreen()
}

// ── Screen: Target ──────────────────────────────────────────────────────────

func (m model) enterTargetScreen() (tea.Model, tea.Cmd) {
	m.screen = screenTarget
	m.items = []item{
		{title: "DMs"},
		{title: "Servers"},
		{title: "Enter channel ID"},
	}
	m.cursor = 0
	m.filter = ""
	return m, nil
}

func (m model) onTargetSelected(idx int) (tea.Model, tea.Cmd) {
	switch idx {
	case 0: // DMs
		m.screen = screenLoading
		m.loadingMsg = "loading DMs"
		return m, tea.Batch(m.spinner.Tick, m.loadDMs())
	case 1: // Servers
		m.screen = screenLoading
		m.loadingMsg = "loading servers"
		return m, tea.Batch(m.spinner.Tick, m.loadGuilds())
	case 2: // Channel ID
		m.screen = screenEnterID
		m.input.Placeholder = "channel ID"
		m.input.SetValue("")
		m.input.Focus()
		return m, m.input.Cursor.BlinkCmd()
	}
	return m, nil
}

// ── Screen: DMs ─────────────────────────────────────────────────────────────

func (m model) loadDMs() tea.Cmd {
	return func() tea.Msg {
		dms, err := m.client.GetDMs()
		if err != nil {
			return errMsg{err}
		}
		var filtered []DMChannel
		for _, dm := range dms {
			if dm.Type == 1 || dm.Type == 3 {
				filtered = append(filtered, dm)
			}
		}
		return dmsLoadedMsg(filtered)
	}
}

func (m model) onDMsSelected(indices []int) (tea.Model, tea.Cmd) {
	ids := make([]string, len(indices))
	for i, idx := range indices {
		ids[i] = m.items[idx].id
	}
	m.channelIDs = ids
	if len(ids) == 1 {
		m.targetLabel = m.items[indices[0]].title
	} else {
		m.targetLabel = strconv.Itoa(len(ids)) + " DMs"
	}
	return m.enterOptionsScreen()
}

func dmDisplayName(dm DMChannel) string {
	switch dm.Type {
	case 1:
		if len(dm.Recipients) > 0 {
			r := dm.Recipients[0]
			if r.GlobalName != "" {
				return r.GlobalName
			}
			return r.Username
		}
	case 3:
		if dm.Name != "" {
			return dm.Name
		}
		names := make([]string, len(dm.Recipients))
		for i, r := range dm.Recipients {
			names[i] = r.Username
		}
		return strings.Join(names, ", ")
	}
	return dm.ID
}

// ── Screen: Servers ─────────────────────────────────────────────────────────

func (m model) loadGuilds() tea.Cmd {
	return func() tea.Msg {
		guilds, err := m.client.GetGuilds()
		if err != nil {
			return errMsg{err}
		}
		return guildsLoadedMsg(guilds)
	}
}

func (m model) onServerSelected(idx int) (tea.Model, tea.Cmd) {
	g := m.guilds[idx]
	m.guildID = g.ID
	m.guildName = g.Name
	m.hasGuild = true

	m.screen = screenServerMode
	m.items = []item{
		{title: "Browse channels"},
		{title: "Smart scan", desc: "find your messages"},
	}
	m.cursor = 0
	m.filter = ""
	return m, nil
}

func (m model) onServerModeSelected(idx int) (tea.Model, tea.Cmd) {
	switch idx {
	case 0: // Browse
		m.screen = screenLoading
		m.loadingMsg = "loading channels"
		return m, tea.Batch(m.spinner.Tick, m.loadChannels())
	case 1: // Smart scan
		m.screen = screenSmartScanning
		m.scanFound = 0
		m.scanTotal = 0
		m.scanProg = &sharedScanProg{}
		return m, tea.Batch(m.spinner.Tick, m.runSmartScan(), tickScan())
	}
	return m, nil
}

// ── Screen: Channel Browse ──────────────────────────────────────────────────

func (m model) loadChannels() tea.Cmd {
	return func() tea.Msg {
		channels, err := m.client.GetGuildChannels(m.guildID)
		if err != nil {
			return errMsg{err}
		}
		return channelsLoadedMsg(channels)
	}
}

func (m model) onChannelsSelected(indices []int) (tea.Model, tea.Cmd) {
	ids := make([]string, len(indices))
	for i, idx := range indices {
		ids[i] = m.items[idx].id
	}
	m.channelIDs = ids
	if len(ids) == 1 {
		m.targetLabel = m.items[indices[0]].title + " (" + m.guildName + ")"
	} else {
		m.targetLabel = strconv.Itoa(len(ids)) + " channels (" + m.guildName + ")"
	}
	return m.enterOptionsScreen()
}

// ── Screen: Smart Scan ──────────────────────────────────────────────────────

func tickScan() tea.Cmd {
	return tea.Tick(200*time.Millisecond, func(t time.Time) tea.Msg {
		return scanTickMsg(t)
	})
}

func (m model) runSmartScan() tea.Cmd {
	client := m.client
	guildID := m.guildID
	userID := m.user.ID
	prog := m.scanProg
	return func() tea.Msg {
		counts, total := PreScan(client, guildID, userID, func(found, t int) {
			prog.found.Store(int64(found))
			prog.total.Store(int64(t))
		})
		return smartScanDoneMsg{counts: counts, total: total}
	}
}

func (m model) handleSmartScanDone(result smartScanDoneMsg) (tea.Model, tea.Cmd) {
	m.scanCounts = result.counts
	m.scanTotal = result.total

	if result.total == 0 {
		m.err = fmt.Errorf("no messages found in this server")
		return m.enterTargetScreen()
	}

	// Resolve channel names
	channels, _ := m.client.GetGuildChannels(m.guildID)
	chMap := make(map[string]Channel, len(channels))
	for _, ch := range channels {
		chMap[ch.ID] = ch
	}

	type chCount struct {
		id    string
		name  string
		count int
	}
	var sorted []chCount
	for cid, count := range result.counts {
		name := cid
		if ch, ok := chMap[cid]; ok {
			name = "#" + ch.Name
		}
		sorted = append(sorted, chCount{cid, name, count})
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].count > sorted[j].count })

	m.items = make([]item, len(sorted))
	for i, sc := range sorted {
		m.items[i] = item{
			title: sc.name,
			desc:  "~" + fmtCount(sc.count),
			id:    sc.id,
			count: sc.count,
		}
	}
	m.cursor = 0
	m.selected = make(map[int]bool)
	m.filter = ""
	m.screen = screenSmartResults
	return m, nil
}

func (m model) onSmartResultsSelected(indices []int) (tea.Model, tea.Cmd) {
	ids := make([]string, len(indices))
	for i, idx := range indices {
		ids[i] = m.items[idx].id
	}
	m.channelIDs = ids
	m.targetLabel = strconv.Itoa(len(ids)) + " channels (~" + fmtCount(m.scanTotal) + " msgs, " + m.guildName + ")"
	return m.enterOptionsScreen()
}

// ── Screen: Enter channel ID ────────────────────────────────────────────────

func (m model) updateEnterID(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "enter":
			id := strings.TrimSpace(m.input.Value())
			if id != "" {
				m.channelIDs = []string{id}
				m.targetLabel = "#" + id
				return m.enterOptionsScreen()
			}
		case "esc":
			return m.enterTargetScreen()
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// ── Screen: Options ─────────────────────────────────────────────────────────

func (m model) enterOptionsScreen() (tea.Model, tea.Cmd) {
	m.screen = screenOptions
	m.optCursor = optDryRun
	m.optDryRun = false
	m.optKeyword = ""
	m.optType = 0
	m.optBefore = ""
	m.optAfter = ""
	m.optThreads = false
	m.optExport = true
	m.input.SetValue("")
	m.input.Blur()
	return m, nil
}

func (m model) updateOptions(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		// text fields: forward input when focused
		if m.optCursor == optKeyword || m.optCursor == optBefore || m.optCursor == optAfter {
			switch key.String() {
			case "up":
				m.saveOptText()
				m.input.Blur()
				m.optCursor--
				return m.focusOptField()
			case "down", "tab":
				m.saveOptText()
				m.input.Blur()
				m.optCursor++
				if !m.hasGuild && m.optCursor == optThreads {
					m.optCursor++
				}
				return m.focusOptField()
			case "enter":
				m.saveOptText()
				m.input.Blur()
				m.optCursor++
				if !m.hasGuild && m.optCursor == optThreads {
					m.optCursor++
				}
				return m.focusOptField()
			case "esc":
				return m.enterTargetScreen()
			default:
				var cmd tea.Cmd
				m.input, cmd = m.input.Update(msg)
				return m, cmd
			}
		}

		switch key.String() {
		case "up", "shift+tab":
			if m.optCursor > optDryRun {
				m.optCursor--
				if !m.hasGuild && m.optCursor == optThreads {
					m.optCursor--
				}
			}
			return m.focusOptField()
		case "down", "tab":
			if m.optCursor < optStart {
				m.optCursor++
				if !m.hasGuild && m.optCursor == optThreads {
					m.optCursor++
				}
			}
			return m.focusOptField()
		case "enter":
			if m.optCursor == optStart {
				return m.startPurge()
			}
			// toggle or advance
			switch m.optCursor {
			case optDryRun:
				m.optDryRun = !m.optDryRun
			case optTypeFilter:
				m.optType = (m.optType + 1) % len(typeFilters)
			case optThreads:
				m.optThreads = !m.optThreads
			case optExport:
				m.optExport = !m.optExport
			default:
				m.optCursor++
				if !m.hasGuild && m.optCursor == optThreads {
					m.optCursor++
				}
				return m.focusOptField()
			}
			return m, nil
		case " ":
			switch m.optCursor {
			case optDryRun:
				m.optDryRun = !m.optDryRun
			case optTypeFilter:
				m.optType = (m.optType + 1) % len(typeFilters)
			case optThreads:
				m.optThreads = !m.optThreads
			case optExport:
				m.optExport = !m.optExport
			}
			return m, nil
		case "left":
			if m.optCursor == optTypeFilter {
				m.optType = (m.optType + len(typeFilters) - 1) % len(typeFilters)
			}
			return m, nil
		case "right":
			if m.optCursor == optTypeFilter {
				m.optType = (m.optType + 1) % len(typeFilters)
			}
			return m, nil
		case "esc":
			return m.enterTargetScreen()
		}
	}
	return m, nil
}

func (m model) focusOptField() (tea.Model, tea.Cmd) {
	switch m.optCursor {
	case optKeyword:
		m.input.Placeholder = "keyword or regex:pattern"
		m.input.SetValue(m.optKeyword)
		m.input.Focus()
		m.input.CursorEnd()
		return m, m.input.Cursor.BlinkCmd()
	case optBefore:
		m.input.Placeholder = "YYYY-MM-DD or 30d"
		m.input.SetValue(m.optBefore)
		m.input.Focus()
		m.input.CursorEnd()
		return m, m.input.Cursor.BlinkCmd()
	case optAfter:
		m.input.Placeholder = "YYYY-MM-DD or 30d"
		m.input.SetValue(m.optAfter)
		m.input.Focus()
		m.input.CursorEnd()
		return m, m.input.Cursor.BlinkCmd()
	}
	m.input.Blur()
	return m, nil
}

func (m *model) saveOptText() {
	val := strings.TrimSpace(m.input.Value())
	switch m.optCursor {
	case optKeyword:
		m.optKeyword = val
	case optBefore:
		m.optBefore = val
	case optAfter:
		m.optAfter = val
	}
}

// ── Screen: Running ─────────────────────────────────────────────────────────

func (m model) startPurge() (tea.Model, tea.Cmd) {
	m.buildFilter()
	m.screen = screenConfirm
	return m, nil
}

func (m model) confirmPurge() (tea.Model, tea.Cmd) {

	m.exportPath = ""
	if m.optExport && !m.optDryRun {
		m.exportPath = fmt.Sprintf("purge_export_%s.jsonl", timeNow().Format("20060102_150405"))
	}

	// Thread discovery (async to avoid blocking TUI)
	if m.optThreads && m.guildID != "" {
		m.screen = screenLoading
		m.loadingMsg = "discovering threads"
		client := m.client
		guildID := m.guildID
		chIDs := m.channelIDs
		return m, tea.Batch(m.spinner.Tick, func() tea.Msg {
			extra := DiscoverThreads(client, guildID, chIDs)
			return threadsFoundMsg(extra)
		})
	}

	return m.launchPurge()
}

func (m *model) buildFilter() {
	m.purgeFilter = Filter{
		AuthorID:   m.user.ID,
		TypeFilter: typeFilters[m.optType],
	}
	if m.optBefore != "" {
		m.purgeFilter.Before = parseDate(m.optBefore)
	}
	if m.optAfter != "" {
		m.purgeFilter.After = parseDate(m.optAfter)
	}
	if m.optKeyword != "" {
		if strings.HasPrefix(m.optKeyword, "regex:") {
			re, err := regexp.Compile(m.optKeyword[6:])
			if err == nil {
				m.purgeFilter.Match = re
			}
		} else {
			m.purgeFilter.Match = regexp.MustCompile(`(?i)` + regexp.QuoteMeta(m.optKeyword))
		}
	}
}

func (m model) launchPurge() (tea.Model, tea.Cmd) {
	m.stats = NewStats(len(m.channelIDs))
	m.deleteLog = make(chan string, 100)
	m.recentDeletes = nil
	scanner := NewScanner(m.client, m.purgeFilter, m.workers, m.stats)
	scanner.deleteLog = m.deleteLog

	m.screen = screenRunning

	if m.guildID != "" {
		// Search-based: directly find user's messages via search API (fast)
		return m, tea.Batch(
			runSearchPurgeCmd(scanner, m.guildID, m.channelIDs, m.optDryRun, m.exportPath),
			tickPurge(),
		)
	}

	// Channel-based: paginate all messages (for DMs / manual channel IDs)
	return m, tea.Batch(
		runPurgeCmd(scanner, m.channelIDs, m.optDryRun, m.exportPath),
		tickPurge(),
	)
}

func runSearchPurgeCmd(scanner *Scanner, guildID string, channelIDs []string, dryRun bool, exportPath string) tea.Cmd {
	return func() tea.Msg {
		scanner.ScanAndDeleteViaSearch(guildID, channelIDs, dryRun, exportPath)
		return purgeDoneMsg{}
	}
}

func runPurgeCmd(scanner *Scanner, channelIDs []string, dryRun bool, exportPath string) tea.Cmd {
	return func() tea.Msg {
		scanner.ScanAndDelete(channelIDs, dryRun, exportPath, false)
		return purgeDoneMsg{}
	}
}

func tickPurge() tea.Cmd {
	return tea.Tick(200*time.Millisecond, func(t time.Time) tea.Msg {
		return purgeTickMsg(t)
	})
}

func (m model) updateDone(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "enter", "esc", "q":
			return m, tea.Quit
		}
	}
	return m, nil
}

// ── Generic list update (single select) ─────────────────────────────────────

func (m model) updateList(msg tea.Msg, onSelect func(int) (tea.Model, tea.Cmd)) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	visible := m.visibleItems()

	switch key.String() {
	case "up", "k":
		for {
			if m.cursor > 0 {
				m.cursor--
			}
			if m.cursor < len(visible) && !visible[m.cursor].header {
				break
			}
			if m.cursor == 0 {
				break
			}
		}
	case "down", "j":
		for {
			if m.cursor < len(visible)-1 {
				m.cursor++
			}
			if m.cursor < len(visible) && !visible[m.cursor].header {
				break
			}
			if m.cursor >= len(visible)-1 {
				break
			}
		}
	case "enter":
		if m.cursor < len(visible) && !visible[m.cursor].header {
			// map visible index back to original
			origIdx := m.visibleToOrig(m.cursor)
			return onSelect(origIdx)
		}
	case "esc":
		return m.enterTargetScreen()
	case "backspace":
		if len(m.filter) > 0 {
			m.filter = m.filter[:len(m.filter)-1]
			m.cursor = 0
		}
	default:
		if len(key.String()) == 1 && key.String() >= " " {
			m.filter += key.String()
			m.cursor = 0
		}
	}
	return m, nil
}

// ── Generic multi-select update ─────────────────────────────────────────────

func (m model) updateMultiList(msg tea.Msg, onConfirm func([]int) (tea.Model, tea.Cmd)) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	visible := m.visibleItems()

	switch key.String() {
	case "up", "k":
		for {
			if m.cursor > 0 {
				m.cursor--
			}
			if m.cursor < len(visible) && !visible[m.cursor].header {
				break
			}
			if m.cursor == 0 {
				break
			}
		}
	case "down", "j":
		for {
			if m.cursor < len(visible)-1 {
				m.cursor++
			}
			if m.cursor < len(visible) && !visible[m.cursor].header {
				break
			}
			if m.cursor >= len(visible)-1 {
				break
			}
		}
	case " ":
		if m.cursor < len(visible) && !visible[m.cursor].header {
			orig := m.visibleToOrig(m.cursor)
			m.selected[orig] = !m.selected[orig]
			if !m.selected[orig] {
				delete(m.selected, orig)
			}
		}
	case "a":
		// toggle all
		allSelected := true
		for i, it := range m.items {
			if !it.header && !m.selected[i] {
				allSelected = false
				break
			}
		}
		if allSelected {
			m.selected = make(map[int]bool)
		} else {
			for i, it := range m.items {
				if !it.header {
					m.selected[i] = true
				}
			}
		}
	case "enter":
		var indices []int
		for i := 0; i < len(m.items); i++ {
			if m.selected[i] {
				indices = append(indices, i)
			}
		}
		if len(indices) > 0 {
			return onConfirm(indices)
		}
	case "esc":
		return m.enterTargetScreen()
	case "backspace":
		if len(m.filter) > 0 {
			m.filter = m.filter[:len(m.filter)-1]
			m.cursor = 0
		}
	default:
		if len(key.String()) == 1 && key.String() >= " " && key.String() != "a" {
			m.filter += key.String()
			m.cursor = 0
		}
	}
	return m, nil
}

// ── Helpers ─────────────────────────────────────────────────────────────────

func (m model) visibleItems() []item {
	if m.filter == "" {
		return m.items
	}
	f := strings.ToLower(m.filter)
	var result []item
	for _, it := range m.items {
		if it.header || strings.Contains(strings.ToLower(it.title), f) {
			result = append(result, it)
		}
	}
	return result
}

func (m model) visibleToOrig(visIdx int) int {
	if m.filter == "" {
		return visIdx
	}
	f := strings.ToLower(m.filter)
	count := 0
	for i, it := range m.items {
		if it.header || strings.Contains(strings.ToLower(it.title), f) {
			if count == visIdx {
				return i
			}
			count++
		}
	}
	return visIdx
}

func buildChannelTree(channels []Channel) []item {
	cats := make(map[string]Channel)
	var textChs []Channel
	for _, ch := range channels {
		if ch.Type == 4 {
			cats[ch.ID] = ch
		} else if ch.Type == 0 || ch.Type == 2 || ch.Type == 5 || ch.Type == 13 {
			textChs = append(textChs, ch)
		}
	}

	byParent := make(map[string][]Channel)
	var uncategorized []Channel
	for _, ch := range textChs {
		if ch.ParentID != "" {
			if _, isCat := cats[ch.ParentID]; isCat {
				byParent[ch.ParentID] = append(byParent[ch.ParentID], ch)
				continue
			}
		}
		uncategorized = append(uncategorized, ch)
	}

	sort.Slice(uncategorized, func(i, j int) bool { return uncategorized[i].Position < uncategorized[j].Position })

	var items []item

	for _, ch := range uncategorized {
		prefix := "#"
		if ch.Type == 2 || ch.Type == 13 {
			prefix = "~"
		}
		items = append(items, item{title: prefix + ch.Name, id: ch.ID})
	}

	sortedCats := make([]Channel, 0, len(cats))
	for _, c := range cats {
		sortedCats = append(sortedCats, c)
	}
	sort.Slice(sortedCats, func(i, j int) bool { return sortedCats[i].Position < sortedCats[j].Position })

	for _, cat := range sortedCats {
		chs := byParent[cat.ID]
		if len(chs) == 0 {
			continue
		}
		sort.Slice(chs, func(i, j int) bool { return chs[i].Position < chs[j].Position })
		items = append(items, item{title: cat.Name, header: true})
		for _, ch := range chs {
			prefix := "#"
			if ch.Type == 2 || ch.Type == 13 {
				prefix = "~"
			}
			items = append(items, item{title: prefix + ch.Name, id: ch.ID})
		}
	}

	return items
}

func dmTypeLabel(dm DMChannel) string {
	if dm.Type == 3 {
		return "group"
	}
	return ""
}

// ── View ────────────────────────────────────────────────────────────────────

func (m model) View() string {
	if m.quitting {
		return ""
	}

	var b strings.Builder

	b.WriteString("\n")
	b.WriteString("  " + styleBold.Render("discord-purge") + "\n")

	if m.err != nil {
		b.WriteString("\n  " + styleRed.Render("ERR") + " " + m.err.Error() + "\n")
	}

	switch m.screen {
	case screenLoading:
		b.WriteString(m.viewLoading())
	case screenAccounts:
		b.WriteString(m.viewListScreen("select account", false))
	case screenTarget:
		b.WriteString(m.viewListScreen("purge target", false))
	case screenDMs:
		b.WriteString(m.viewMultiListScreen("select DMs", "space=toggle  a=all  enter=confirm"))
	case screenServers:
		b.WriteString(m.viewListScreen("select server", true))
	case screenServerMode:
		b.WriteString(m.viewListScreen(m.guildName, false))
	case screenChannels:
		b.WriteString(m.viewMultiListScreen(m.guildName, "space=toggle  a=all  enter=confirm"))
	case screenSmartScanning:
		b.WriteString(m.viewSmartScanning())
	case screenSmartResults:
		b.WriteString(m.viewMultiListScreen(
			m.guildName+" — ~"+fmtCount(m.scanTotal)+" messages",
			"space=toggle  a=all  enter=confirm",
		))
	case screenEnterID:
		b.WriteString(m.viewEnterID())
	case screenOptions:
		b.WriteString(m.viewOptions())
	case screenConfirm:
		b.WriteString(m.viewConfirm())
	case screenRunning:
		b.WriteString(m.viewRunning())
	case screenDone:
		b.WriteString(m.viewDone())
	}

	b.WriteString("\n")
	return b.String()
}

func (m model) viewLoading() string {
	msg := m.loadingMsg
	if msg == "" {
		msg = "loading"
	}
	return fmt.Sprintf("\n  %s %s...\n", m.spinner.View(), msg)
}

func (m model) viewListScreen(title string, filterable bool) string {
	var b strings.Builder
	if m.user != nil {
		b.WriteString("  " + styleDim.Render(m.user.Username) + "\n")
	}
	b.WriteString("\n  " + styleBold.Render(title) + "\n\n")

	visible := m.visibleItems()
	// scroll window
	start, end := scrollWindow(m.cursor, len(visible), m.height-10)

	for i := start; i < end; i++ {
		it := visible[i]
		if it.header {
			b.WriteString("    " + styleDim.Render("── "+it.title+" ──") + "\n")
			continue
		}
		cursor := "  "
		style := lipgloss.NewStyle()
		if i == m.cursor {
			cursor = styleCursor.Render("> ")
			style = styleCyan
		}
		line := cursor + style.Render(it.title)
		if it.desc != "" {
			line += "  " + styleDim.Render(it.desc)
		}
		b.WriteString("  " + line + "\n")
	}

	if filterable && m.filter != "" {
		b.WriteString("\n  " + styleDim.Render("filter: ") + styleCyan.Render(m.filter))
	}
	b.WriteString("\n  " + styleDim.Render("↑↓ navigate  enter select  esc back"))
	return b.String()
}

func (m model) viewMultiListScreen(title, hint string) string {
	var b strings.Builder
	if m.user != nil {
		b.WriteString("  " + styleDim.Render(m.user.Username) + "\n")
	}
	b.WriteString("\n  " + styleBold.Render(title) + "\n\n")

	visible := m.visibleItems()
	start, end := scrollWindow(m.cursor, len(visible), m.height-10)

	for i := start; i < end; i++ {
		it := visible[i]
		if it.header {
			b.WriteString("    " + styleDim.Render("── "+it.title+" ──") + "\n")
			continue
		}

		origIdx := m.visibleToOrig(i)
		check := "[ ]"
		if m.selected[origIdx] {
			check = styleGreen.Render("[x]")
		}

		cursor := "  "
		style := lipgloss.NewStyle()
		if i == m.cursor {
			cursor = styleCursor.Render("> ")
			style = styleCyan
		}

		line := cursor + check + " " + style.Render(it.title)
		if it.desc != "" {
			line += "  " + styleDim.Render(it.desc)
		}
		b.WriteString("  " + line + "\n")
	}

	selected := 0
	for _, v := range m.selected {
		if v {
			selected++
		}
	}
	if selected > 0 {
		b.WriteString("\n  " + styleGreen.Render(strconv.Itoa(selected)+" selected"))
	}

	if m.filter != "" {
		b.WriteString("\n  " + styleDim.Render("filter: ") + styleCyan.Render(m.filter))
	}
	b.WriteString("\n  " + styleDim.Render(hint+"  esc back"))
	return b.String()
}

func (m model) viewSmartScanning() string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("\n  %s scanning %s for your messages...\n",
		m.spinner.View(), styleBold.Render(m.guildName)))
	if m.scanTotal > 0 {
		pct := float64(m.scanFound) / float64(m.scanTotal) * 100
		b.WriteString(fmt.Sprintf("\n  %s/%s messages sampled  %.0f%%\n",
			styleCyan.Render(fmtCount(m.scanFound)),
			styleDim.Render(fmtCount(m.scanTotal)),
			pct))
	}
	return b.String()
}

func (m model) viewEnterID() string {
	var b strings.Builder
	b.WriteString("\n  " + styleBold.Render("enter channel ID") + "\n\n")
	b.WriteString("  " + m.input.View() + "\n")
	b.WriteString("\n  " + styleDim.Render("enter confirm  esc back"))
	return b.String()
}

func (m model) viewOptions() string {
	var b strings.Builder
	b.WriteString("  " + styleDim.Render(m.user.Username) + " → " + styleBold.Render(m.targetLabel) + "\n")
	b.WriteString("\n  " + styleBold.Render("options") + "\n\n")

	fields := []struct {
		field optField
		label string
		value string
	}{
		{optDryRun, "dry run", toggleStr(m.optDryRun)},
		{optKeyword, "keyword", textOrEmpty(m.optKeyword, m.optCursor == optKeyword, m.input)},
		{optTypeFilter, "type", "< " + typeFilters[m.optType] + " >"},
		{optBefore, "before", textOrEmpty(m.optBefore, m.optCursor == optBefore, m.input)},
		{optAfter, "after", textOrEmpty(m.optAfter, m.optCursor == optAfter, m.input)},
	}
	if m.hasGuild {
		fields = append(fields, struct {
			field optField
			label string
			value string
		}{optThreads, "threads", toggleStr(m.optThreads)})
	}
	fields = append(fields, struct {
		field optField
		label string
		value string
	}{optExport, "export", toggleStr(m.optExport)})

	for _, f := range fields {
		cursor := "  "
		style := lipgloss.NewStyle()
		if m.optCursor == f.field {
			cursor = styleCursor.Render("> ")
			style = styleCyan
		}
		label := fmt.Sprintf("%-10s", f.label)
		b.WriteString("  " + cursor + style.Render(label) + "  " + f.value + "\n")
	}

	// Start button
	b.WriteString("\n")
	if m.optCursor == optStart {
		b.WriteString("  " + styleCursor.Render("> ") + styleBold.Render("[ start purge ]") + "\n")
	} else {
		b.WriteString("    " + styleDim.Render("[ start purge ]") + "\n")
	}

	b.WriteString("\n  " + styleDim.Render("↑↓ navigate  space toggle  enter confirm  esc back"))
	return b.String()
}

func toggleStr(v bool) string {
	if v {
		return styleGreen.Render("[x] yes")
	}
	return styleDim.Render("[ ] no")
}

func textOrEmpty(stored string, active bool, ti textinput.Model) string {
	if active {
		return ti.View()
	}
	if stored == "" {
		return styleDim.Render("—")
	}
	return stored
}

func (m model) viewConfirm() string {
	var b strings.Builder
	b.WriteString("  " + styleDim.Render(m.user.Username) + "\n")
	b.WriteString("\n  " + styleBold.Render("confirm purge") + "\n\n")

	b.WriteString(fmt.Sprintf("  %-12s %s\n", "target", styleBold.Render(m.targetLabel)))
	b.WriteString(fmt.Sprintf("  %-12s %d\n", "channels", len(m.channelIDs)))
	if m.optDryRun {
		b.WriteString(fmt.Sprintf("  %-12s %s\n", "mode", styleYellow.Render("DRY RUN")))
	}
	if m.optKeyword != "" {
		b.WriteString(fmt.Sprintf("  %-12s %s\n", "keyword", styleCyan.Render(m.optKeyword)))
	}
	if typeFilters[m.optType] != "all" {
		b.WriteString(fmt.Sprintf("  %-12s %s\n", "type", typeFilters[m.optType]))
	}
	if m.optBefore != "" {
		b.WriteString(fmt.Sprintf("  %-12s %s\n", "before", m.optBefore))
	}
	if m.optAfter != "" {
		b.WriteString(fmt.Sprintf("  %-12s %s\n", "after", m.optAfter))
	}
	if m.optThreads {
		b.WriteString(fmt.Sprintf("  %-12s yes\n", "threads"))
	}
	if m.exportPath != "" || m.optExport {
		b.WriteString(fmt.Sprintf("  %-12s yes\n", "export"))
	}

	b.WriteString("\n  " + styleGreen.Render("[enter]") + " start   " + styleDim.Render("[esc] back"))
	return b.String()
}

func (m model) viewRunning() string {
	var b strings.Builder
	s := m.stats

	scanned := s.Scanned.Load()
	matched := s.Matched.Load()
	deleted := s.Deleted.Load()
	failed := s.Failed.Load()
	doneCh := s.DoneChannels.Load()
	elapsed := time.Since(s.StartTime).Truncate(time.Second)
	rate := float64(scanned) / max(time.Since(s.StartTime).Seconds(), 0.1)

	mode := "purging"
	if m.optDryRun {
		mode = styleYellow.Render("dry run")
	}

	b.WriteString("  " + styleDim.Render(m.user.Username) + "\n")
	b.WriteString("\n  " + styleBold.Render(mode) + " → " + m.targetLabel + "\n\n")

	b.WriteString(fmt.Sprintf("  %-10s %s\n", "scanned", styleBold.Render(fmtCount(int(scanned)))))
	b.WriteString(fmt.Sprintf("  %-10s %s\n", "matched", styleCyan.Render(fmtCount(int(matched)))))
	b.WriteString(fmt.Sprintf("  %-10s %s\n", "deleted", styleGreen.Render(fmtCount(int(deleted)))))
	if failed > 0 {
		b.WriteString(fmt.Sprintf("  %-10s %s\n", "failed", styleRed.Render(fmtCount(int(failed)))))
	}
	if s.TotalCh > 1000 {
		// Search-based mode: TotalCh = total messages
		b.WriteString(fmt.Sprintf("  %-10s %s/%s\n", "progress", fmtCount(int(doneCh)), fmtCount(int(s.TotalCh))))
	} else {
		b.WriteString(fmt.Sprintf("  %-10s %d/%d\n", "channels", doneCh, s.TotalCh))
	}
	b.WriteString(fmt.Sprintf("  %-10s ~%.0f/s\n", "rate", rate))
	if m.client != nil {
		delDelay := m.client.delPacer.currentDelay()
		delRate := 1.0 / delDelay.Seconds()
		b.WriteString(fmt.Sprintf("  %-10s ~%.1f del/s %s\n", "pacer",
			delRate, styleDim.Render("("+delDelay.Truncate(time.Millisecond).String()+")")))
	}
	b.WriteString(fmt.Sprintf("  %-10s %s\n", "elapsed", elapsed))

	// Progress bar
	if s.TotalCh > 0 {
		pct := float64(doneCh) / float64(s.TotalCh)
		b.WriteString("\n  " + m.progress.ViewAs(pct) + "\n")
	}

	// Recent deletions
	if len(m.recentDeletes) > 0 {
		b.WriteString("\n")
		for _, preview := range m.recentDeletes {
			b.WriteString("    " + styleDim.Render("× "+preview) + "\n")
		}
	}

	b.WriteString("\n  " + styleDim.Render("ctrl+c to stop"))
	return b.String()
}

func (m model) viewDone() string {
	var b strings.Builder
	s := m.stats

	scanned := s.Scanned.Load()
	matched := s.Matched.Load()
	deleted := s.Deleted.Load()
	failed := s.Failed.Load()
	elapsed := m.purgeElapsed.Truncate(time.Millisecond)
	rate := float64(scanned) / max(m.purgeElapsed.Seconds(), 0.1)

	b.WriteString("\n  " + styleBold.Render("done") + "\n\n")
	b.WriteString(fmt.Sprintf("  %-10s %s\n", "scanned", styleBold.Render(fmtCount(int(scanned)))))
	b.WriteString(fmt.Sprintf("  %-10s %s\n", "matched", styleCyan.Render(fmtCount(int(matched)))))
	b.WriteString(fmt.Sprintf("  %-10s %s\n", "deleted", styleGreen.Render(fmtCount(int(deleted)))))
	if failed > 0 {
		b.WriteString(fmt.Sprintf("  %-10s %s\n", "failed", styleRed.Render(fmtCount(int(failed)))))
	}
	b.WriteString(fmt.Sprintf("  %-10s %d\n", "channels", s.TotalCh))
	b.WriteString(fmt.Sprintf("  %-10s %s\n", "time", elapsed))
	b.WriteString(fmt.Sprintf("  %-10s ~%.0f msgs/sec\n", "rate", rate))
	if m.exportPath != "" {
		b.WriteString(fmt.Sprintf("  %-10s %s\n", "export", styleDim.Render(m.exportPath)))
	}

	b.WriteString("\n  " + styleDim.Render("press any key to exit"))
	return b.String()
}

// scrollWindow returns visible range [start, end) for scrolling
func scrollWindow(cursor, total, maxVisible int) (int, int) {
	if maxVisible <= 0 {
		maxVisible = 20
	}
	if total <= maxVisible {
		return 0, total
	}
	half := maxVisible / 2
	start := cursor - half
	if start < 0 {
		start = 0
	}
	end := start + maxVisible
	if end > total {
		end = total
		start = end - maxVisible
		if start < 0 {
			start = 0
		}
	}
	return start, end
}

// ── Entry point ─────────────────────────────────────────────────────────────

func tuiMain(tokenFlag string, workers int) {
	m := newModel(tokenFlag, workers)
	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "TUI error: %v\n", err)
		os.Exit(1)
	}
}
