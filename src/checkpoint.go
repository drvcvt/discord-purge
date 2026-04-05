package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Checkpoint tracks purge progress for resume after interrupt.
type Checkpoint struct {
	mu       sync.Mutex
	data     map[string]checkpointEntry
	path     string
	counter  int
	saveEvery int
}

type checkpointEntry struct {
	LastID string `json:"last_id"`
	TS     string `json:"ts"`
}

func NewCheckpoint() *Checkpoint {
	dir := filepath.Join(os.Getenv("USERPROFILE"), ".config", "discord-purger")
	os.MkdirAll(dir, 0700)
	return &Checkpoint{
		data:      make(map[string]checkpointEntry),
		path:      filepath.Join(dir, "checkpoint.json"),
		saveEvery: 50,
	}
}

func (c *Checkpoint) Load() map[string]string {
	data, err := os.ReadFile(c.path)
	if err != nil {
		return nil
	}
	if err := json.Unmarshal(data, &c.data); err != nil {
		return nil
	}
	result := make(map[string]string, len(c.data))
	for k, v := range c.data {
		if v.LastID != "" {
			result[k] = v.LastID
		}
	}
	return result
}

func (c *Checkpoint) Get(channelID string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.data[channelID]; ok {
		return e.LastID
	}
	return ""
}

func (c *Checkpoint) Update(channelID, messageID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data[channelID] = checkpointEntry{
		LastID: messageID,
		TS:     time.Now().UTC().Format(time.RFC3339),
	}
	c.counter++
	if c.counter >= c.saveEvery {
		c.flush()
		c.counter = 0
	}
}

func (c *Checkpoint) Save() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.data) > 0 {
		c.flush()
	}
}

func (c *Checkpoint) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	os.Remove(c.path)
	c.data = make(map[string]checkpointEntry)
}

func (c *Checkpoint) flush() {
	data, _ := json.MarshalIndent(c.data, "", "  ")
	os.WriteFile(c.path, data, 0600)
}
