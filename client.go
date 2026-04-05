package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	apiBase   = "https://discord.com/api/v10"
	userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
)

// Client is a Discord REST client with per-route rate limit tracking.
type Client struct {
	token      string
	http       *http.Client
	buckets    sync.Map // route string -> *bucket
	globalMu   sync.Mutex
	globalWait time.Time
	delPacer   deletePacer
}

type bucket struct {
	mu        sync.Mutex
	remaining int
	reset     time.Time
}

// deletePacer implements AIMD-style adaptive rate control for message deletion.
// Instead of blindly hitting the rate limit and eating 429 penalties, it proactively
// spaces requests based on the remaining/reset headers from Discord.
type deletePacer struct {
	mu    sync.Mutex
	delay time.Duration
	last  time.Time
}

func (p *deletePacer) wait() {
	p.mu.Lock()
	d := p.delay
	elapsed := time.Since(p.last)
	p.mu.Unlock()

	if elapsed < d {
		time.Sleep(d - elapsed)
	}
}

// record updates pacing based on rate limit bucket state after a successful delete.
func (p *deletePacer) record(remaining int, resetAfter time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.last = time.Now()

	if remaining > 0 && resetAfter > 0 {
		// Spread remaining requests evenly across the reset window + 15% buffer
		optimal := time.Duration(float64(resetAfter) / float64(remaining) * 1.15)
		p.delay = optimal
	}

	// Additive decrease: gradually speed up after sustained success
	if p.delay > 200*time.Millisecond {
		p.delay = time.Duration(float64(p.delay) * 0.98)
	}

	// Clamp
	if p.delay < 100*time.Millisecond {
		p.delay = 100 * time.Millisecond
	}
	if p.delay > 10*time.Second {
		p.delay = 10 * time.Second
	}
}

// currentDelay returns the current pacing delay.
func (p *deletePacer) currentDelay() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.delay
}

// backoff doubles the delay after a 429 (multiplicative decrease).
func (p *deletePacer) backoff() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.delay *= 2
	if p.delay < 500*time.Millisecond {
		p.delay = 500 * time.Millisecond
	}
	if p.delay > 10*time.Second {
		p.delay = 10 * time.Second
	}
}

func NewClient(token string) *Client {
	return &Client{
		token: token,
		http: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     90 * time.Second,
			},
		},
		delPacer: deletePacer{delay: 200 * time.Millisecond},
	}
}

// routeKey returns the rate limit bucket key for a given method+path.
// Discord scopes rate limits by major parameters (channel_id, guild_id).
func routeKey(method, path string) string {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	// Extract major params: /channels/{id}/... or /guilds/{id}/...
	var major string
	for i, p := range parts {
		if (p == "channels" || p == "guilds") && i+1 < len(parts) {
			major = p + "/" + parts[i+1]
			break
		}
	}
	// For delete messages, each channel has its own bucket
	if method == "DELETE" && strings.Contains(path, "/messages/") {
		return method + ":" + major + ":messages"
	}
	return method + ":" + major
}

// Do executes an HTTP request with rate limit handling.
func (c *Client) Do(method, path string) ([]byte, int, error) {
	rk := routeKey(method, path)
	b := c.getBucket(rk)
	isDeleteMsg := method == "DELETE" && strings.Contains(path, "/messages/")

	for attempt := 0; attempt < 5; attempt++ {
		// Adaptive pacing for DELETE messages — proactive, not reactive
		if isDeleteMsg {
			c.delPacer.wait()
		}

		// Wait for global rate limit
		c.globalMu.Lock()
		if wait := time.Until(c.globalWait); wait > 0 {
			c.globalMu.Unlock()
			time.Sleep(wait)
		} else {
			c.globalMu.Unlock()
		}

		// Wait for per-route rate limit
		b.mu.Lock()
		if b.remaining == 0 && !b.reset.IsZero() {
			wait := time.Until(b.reset)
			if wait > 0 {
				b.mu.Unlock()
				time.Sleep(wait + 100*time.Millisecond) // small buffer
				b.mu.Lock()
			}
		}
		b.mu.Unlock()

		req, err := http.NewRequest(method, apiBase+path, nil)
		if err != nil {
			return nil, 0, err
		}
		req.Header.Set("Authorization", c.token)
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.http.Do(req)
		if err != nil {
			if attempt < 4 {
				time.Sleep(time.Duration(attempt+1) * time.Second)
				continue
			}
			return nil, 0, err
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		// Update bucket from headers
		c.updateBucket(b, resp.Header)

		switch {
		case resp.StatusCode == 429:
			var rl RateLimitResponse
			json.Unmarshal(body, &rl)
			retryAfter := rl.RetryAfter
			if retryAfter == 0 {
				if ra := resp.Header.Get("Retry-After"); ra != "" {
					retryAfter, _ = strconv.ParseFloat(ra, 64)
				}
			}
			if retryAfter == 0 {
				retryAfter = 5
			}
			wait := time.Duration(retryAfter*1000+500) * time.Millisecond

			if rl.Global {
				c.globalMu.Lock()
				c.globalWait = time.Now().Add(wait)
				c.globalMu.Unlock()
			}
			if isDeleteMsg {
				c.delPacer.backoff()
			}
			time.Sleep(wait)
			continue

		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			if isDeleteMsg {
				b.mu.Lock()
				remaining := b.remaining
				resetAfter := time.Until(b.reset)
				b.mu.Unlock()
				c.delPacer.record(remaining, resetAfter)
			}
			return body, resp.StatusCode, nil

		case resp.StatusCode == 404:
			return nil, 404, fmt.Errorf("not found: %s", path)

		case resp.StatusCode == 403:
			return nil, 403, fmt.Errorf("forbidden: %s", path)

		case resp.StatusCode >= 500:
			if attempt < 4 {
				time.Sleep(time.Duration(attempt+1) * 2 * time.Second)
				continue
			}
			return nil, resp.StatusCode, fmt.Errorf("server error %d: %s", resp.StatusCode, path)

		default:
			return body, resp.StatusCode, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
		}
	}
	return nil, 0, fmt.Errorf("max retries exceeded: %s %s", method, path)
}

func (c *Client) getBucket(key string) *bucket {
	if b, ok := c.buckets.Load(key); ok {
		return b.(*bucket)
	}
	b := &bucket{remaining: 1}
	actual, _ := c.buckets.LoadOrStore(key, b)
	return actual.(*bucket)
}

func (c *Client) updateBucket(b *bucket, h http.Header) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if rem := h.Get("X-RateLimit-Remaining"); rem != "" {
		b.remaining, _ = strconv.Atoi(rem)
	}
	if reset := h.Get("X-RateLimit-Reset"); reset != "" {
		f, _ := strconv.ParseFloat(reset, 64)
		b.reset = time.Unix(int64(f), int64((f-float64(int64(f)))*1e9))
	} else if resetAfter := h.Get("X-RateLimit-Reset-After"); resetAfter != "" {
		f, _ := strconv.ParseFloat(resetAfter, 64)
		b.reset = time.Now().Add(time.Duration(f*1000) * time.Millisecond)
	}
}

// --- API Methods ---

func (c *Client) GetMe() (*User, error) {
	body, _, err := c.Do("GET", "/users/@me")
	if err != nil {
		return nil, err
	}
	var u User
	if err := json.Unmarshal(body, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

func (c *Client) GetGuilds() ([]Guild, error) {
	body, _, err := c.Do("GET", "/users/@me/guilds")
	if err != nil {
		return nil, err
	}
	var guilds []Guild
	return guilds, json.Unmarshal(body, &guilds)
}

func (c *Client) GetGuildChannels(guildID string) ([]Channel, error) {
	body, _, err := c.Do("GET", "/guilds/"+guildID+"/channels")
	if err != nil {
		return nil, err
	}
	var channels []Channel
	if err := json.Unmarshal(body, &channels); err != nil {
		return nil, err
	}
	// Filter to text-like channels
	var result []Channel
	for _, ch := range channels {
		switch ch.Type {
		case 0, 2, 4, 5, 13: // text, voice, category, announcement, stage
			result = append(result, ch)
		}
	}
	return result, nil
}

func (c *Client) GetDMs() ([]DMChannel, error) {
	body, _, err := c.Do("GET", "/users/@me/channels")
	if err != nil {
		return nil, err
	}
	var channels []DMChannel
	return channels, json.Unmarshal(body, &channels)
}

// GetMessages fetches up to 100 messages from a channel.
// Pass before="" for the latest messages.
func (c *Client) GetMessages(channelID string, limit int, before string) ([]Message, error) {
	path := fmt.Sprintf("/channels/%s/messages?limit=%d", channelID, limit)
	if before != "" {
		path += "&before=" + before
	}
	body, code, err := c.Do("GET", path)
	if err != nil {
		return nil, err
	}
	if code == 403 || code == 404 {
		return nil, nil
	}
	var msgs []Message
	return msgs, json.Unmarshal(body, &msgs)
}

func (c *Client) DeleteMessage(channelID, messageID string) error {
	_, _, err := c.Do("DELETE", fmt.Sprintf("/channels/%s/messages/%s", channelID, messageID))
	return err
}

func (c *Client) SearchGuildMessages(guildID, authorID string, offset int, opts ...SearchOpt) (*SearchResponse, error) {
	path := fmt.Sprintf("/guilds/%s/messages/search?author_id=%s&include_nsfw=true&offset=%d",
		guildID, authorID, offset)
	for _, o := range opts {
		if o.MinID != "" {
			path += "&min_id=" + o.MinID
		}
		if o.MaxID != "" {
			path += "&max_id=" + o.MaxID
		}
	}
	body, _, err := c.Do("GET", path)
	if err != nil {
		return nil, err
	}
	var sr SearchResponse
	return &sr, json.Unmarshal(body, &sr)
}

// SearchOpt passes optional filters to SearchGuildMessages.
type SearchOpt struct {
	MinID string // snowflake — only messages after this ID
	MaxID string // snowflake — only messages before this ID
}

func (c *Client) GetActiveThreads(guildID string) ([]Thread, error) {
	body, code, err := c.Do("GET", "/guilds/"+guildID+"/threads/active")
	if err != nil || code == 403 {
		return nil, err
	}
	var tr ThreadsResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, err
	}
	return tr.Threads, nil
}

func (c *Client) GetArchivedThreads(channelID, kind string) ([]Thread, error) {
	path := fmt.Sprintf("/channels/%s/threads/archived/%s", channelID, kind)
	body, code, err := c.Do("GET", path)
	if err != nil || code == 403 || code == 404 {
		return nil, nil
	}
	var tr ThreadsResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, err
	}
	return tr.Threads, nil
}
