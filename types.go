package main

import (
	"regexp"
	"strconv"
	"sync/atomic"
	"time"
)

// Discord API v10 types — only what we need.

type User struct {
	ID         string `json:"id"`
	Username   string `json:"username"`
	GlobalName string `json:"global_name"`
}

type Guild struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Channel struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     int    `json:"type"`
	Position int    `json:"position"`
	ParentID string `json:"parent_id"`
}

type Message struct {
	ID        string `json:"id"`
	ChannelID string `json:"channel_id"`
	Content   string `json:"content"`
	Timestamp string `json:"timestamp"`
	Author    User   `json:"author"`
	Attachments []Attachment `json:"attachments"`
	Embeds    []any  `json:"embeds"`
	Hit       bool   `json:"hit"`
}

type Attachment struct {
	Filename string `json:"filename"`
	URL      string `json:"url"`
}

type DMChannel struct {
	ID         string `json:"id"`
	Type       int    `json:"type"`
	Name       string `json:"name"`
	Recipients []User `json:"recipients"`
}

type SearchResponse struct {
	TotalResults int         `json:"total_results"`
	Messages     [][]Message `json:"messages"`
}

type Thread struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	ParentID string `json:"parent_id"`
	Type     int    `json:"type"`
}

type ThreadsResponse struct {
	Threads []Thread `json:"threads"`
}

type RateLimitResponse struct {
	RetryAfter float64 `json:"retry_after"`
	Global     bool    `json:"global"`
}

// Filter controls which messages to match.
type Filter struct {
	AuthorID   string
	Before     *time.Time
	After      *time.Time
	Match      *regexp.Regexp
	TypeFilter string // "all", "attachments", "links", "embeds", "text"
}

// Stats tracks progress atomically across goroutines.
type Stats struct {
	Scanned      atomic.Int64
	Matched      atomic.Int64
	Deleted      atomic.Int64
	Failed       atomic.Int64
	Filtered     atomic.Int64
	TotalCh      int64
	DoneChannels atomic.Int64
	StartTime    time.Time
}

func NewStats(totalChannels int) *Stats {
	return &Stats{
		TotalCh:   int64(totalChannels),
		StartTime: time.Now(),
	}
}

// Snowflake helpers — Discord IDs encode timestamps.
const discordEpoch = 1420070400000 // 2015-01-01T00:00:00Z in ms

func TimeToSnowflake(t time.Time) string {
	ms := t.UnixMilli() - discordEpoch
	if ms < 0 {
		ms = 0
	}
	return strconv.FormatInt(ms<<22, 10)
}

func SnowflakeToTime(sf string) time.Time {
	n, _ := strconv.ParseInt(sf, 10, 64)
	return time.UnixMilli((n >> 22) + discordEpoch)
}
