# discord-purge

Fast concurrent Discord message scanner & deleter with an interactive TUI.

## Features

- **Interactive TUI** — arrow key navigation, live filtering, progress bars (bubbletea)
- **Smart Scan** — uses Discord's search API to find your messages across a server
- **AIMD Rate Pacer** — adaptive delete pacing that avoids rate limits without being slow
- **Multi-pass deletion** — ensures all messages are found even after search index updates
- **Auto token detection** — finds Discord tokens via Windows DPAPI (no manual token entry needed)
- **CLI mode** — fully scriptable with flags for automation
- **Export** — saves messages to JSONL before deletion
- **Resume** — checkpoint-based resume after interruption
- **Filters** — by date, keyword/regex, type (attachments/links/embeds/text)

## Install

Download the latest release from the [Releases](../../releases) page:
- **Installer** — `discord-purge-setup.exe` (NSIS installer, adds to Start Menu)
- **Portable** — `discord-purge.exe` (standalone binary, no install needed)

## Build from source

Requires Go 1.22+.

```bash
git clone https://github.com/drvcvt/discord-purge.git
cd discord-purge
go build -o purge.exe .
```

## Usage

### Interactive mode (TUI)

```bash
purge
```

Arrow keys to navigate, space to select, enter to confirm. The TUI walks you through:
1. Account selection (auto-detected from installed Discord clients)
2. Target selection (DMs / Servers / Channel ID)
3. Channel selection (browse or smart scan)
4. Filter options (dry run, keyword, date range, type)
5. Confirmation + live progress with delete log

### CLI mode

```bash
purge --guild <server-id> [flags]
```

| Flag | Description |
|------|-------------|
| `--guild` | Server ID (required for CLI mode) |
| `--channels` | Comma-separated channel IDs (default: all) |
| `--token` | Discord token (default: auto-detect) |
| `--before` | Only messages before date (`YYYY-MM-DD` or `30d/2w/6m/1y`) |
| `--after` | Only messages after date |
| `--match` | Keyword filter (prefix `regex:` for regex) |
| `--type` | `all`, `attachments`, `links`, `embeds`, `text` |
| `--dry-run` | Preview only, don't delete |
| `--count` | Count matching messages without deleting |
| `--workers` | Concurrent scanner goroutines (default: 10) |
| `--threads` | Include threads in scan |
| `--export` | Export to custom .jsonl path |
| `--resume` | Resume from checkpoint |

### Examples

```bash
# Interactive TUI
purge

# Delete all your messages in a server
purge --guild 123456789

# Dry run on specific channels
purge --guild 123 --channels 456,789 --dry-run

# Delete messages older than 30 days
purge --guild 123 --before 30d

# Only attachments, with keyword filter
purge --guild 123 --type attachments --match "screenshot"
```

## How it works

1. **Token detection** — scans Discord client LevelDB storage, decrypts tokens via Windows DPAPI
2. **Smart Scan** — uses `/guilds/{id}/messages/search` API to sample which channels contain your messages
3. **Search-based deletion** — searches for your messages directly instead of paginating through every message in a channel
4. **AIMD pacing** — reads `X-RateLimit-Remaining` / `X-RateLimit-Reset` headers to calculate optimal request spacing. Backs off on 429s (multiplicative decrease), speeds up on success (additive increase)

## License

MIT
