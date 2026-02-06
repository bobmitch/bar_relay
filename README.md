# BAR Event Relay

A lightweight TCP relay server for [Beyond All Reason](https://www.beyondallreason.info/) that forwards in-game JSON events to a web-based announcer application.

## Overview

This relay sits between the Beyond All Reason game client and a web API, capturing game events (kills, deaths, objectives, etc.) and forwarding them to your custom announcer web app for real-time commentary and effects.

## Installation

### Windows (Recommended)

1. Download the latest release from the [Releases](../../releases) page
2. Extract `bar-relay.exe` to a folder of your choice (e.g., `C:\Program Files\BAR-Relay\`)
3. Run `bar-relay.exe` - you'll be prompted for your UUID on first launch

### Linux/macOS

1. Download the appropriate binary for your platform from [Releases](../../releases)
2. Make it executable: `chmod +x bar-relay`
3. Run: `./bar-relay`

### Building from Source

Requires [Go 1.16+](https://golang.org/dl/)

```bash
git clone https://github.com/yourusername/bar-relay.git
cd bar-relay
go build -o bar-relay.exe  # Windows
go build -o bar-relay      # Linux/macOS
```

## Configuration

### Getting Your UUID

1. Visit the announcer web app at [https://barapi.bobmitch.com](https://barapi.bobmitch.com) (or your custom instance)
2. Copy your unique UUID from the dashboard
3. On first run, paste it when prompted

The UUID is automatically saved to `~/.bar_uuid` for future sessions.

### Command Line Options

```
bar-relay [options]

Options:
  -host string
        The IP address to listen on (default "127.0.0.1")
  -port string
        The TCP port to listen on (default "5005")
  -url string
        The destination Web API URL (default "https://barapi.bobmitch.com/push")
  -uuid string
        Your UUID from the web app (overrides saved UUID)
  -v    Enable detailed logging
  -reset
        Clear the stored UUID and prompt for a new one
```

### Examples

**Basic usage** (uses saved UUID):
```cmd
bar-relay.exe
```

**Custom port**:
```cmd
bar-relay.exe -port 8080
```

**Verbose logging** (useful for debugging):
```cmd
bar-relay.exe -v
```

**One-time UUID override**:
```cmd
bar-relay.exe -uuid your-uuid-here
```

**Reset stored UUID**:
```cmd
bar-relay.exe -reset
```

## Game Configuration

Configure Beyond All Reason to send events to the relay:

1. Open your BAR game settings
2. Navigate to the event output configuration
3. Set the target to `127.0.0.1:5005` (or your custom host/port)
4. Enable event streaming

## How It Works

1. **Game Events**: BAR sends JSON-formatted game events to the relay via TCP
2. **UUID Injection**: The relay adds your UUID to each event
3. **API Forward**: Events are forwarded to the web API via HTTP POST
4. **Web App**: Your announcer web app receives and processes the events in real-time

```
[BAR Game] --TCP--> [Relay] --HTTPS--> [Web API] --WebSocket--> [Announcer App]
            JSON              JSON+UUID              Real-time
```

## Troubleshooting

**"No UUID found" on every launch**
- Run with `-reset` to clear any corrupted UUID file
- Manually create `~/.bar_uuid` with your UUID (Windows: `%USERPROFILE%\.bar_uuid`)

**Events not appearing in web app**
- Verify the relay is running (`--- BAR Relay Active ---` message)
- Check game configuration points to correct host/port
- Enable verbose mode with `-v` to see incoming events
- Verify UUID matches your web app dashboard

**Connection refused errors**
- Ensure no other application is using port 5005
- Check Windows Firewall isn't blocking the relay
- Try a different port with `-port 8080`

**API errors (4xx/5xx responses)**
- Verify your UUID is correct
- Check internet connectivity
- Confirm API URL is accessible

## Advanced Usage

### Running as a Windows Service

For persistent background operation, use [NSSM](https://nssm.cc/):

```cmd
nssm install BARRelay "C:\Program Files\BAR-Relay\bar-relay.exe"
nssm set BARRelay AppDirectory "C:\Program Files\BAR-Relay"
nssm start BARRelay
```

### Custom API Endpoint

If hosting your own announcer backend:

```cmd
bar-relay.exe -url https://your-api.example.com/events
```

## Development

### Testing Locally

Send a test event:

```bash
echo '{"event":"kill","player":"TestPlayer"}' | nc localhost 5005
```

### Event Format

Incoming events from BAR are standard JSON. The relay adds a `uuid` field:

```json
{
  "event": "kill",
  "player": "PlayerName",
  "victim": "EnemyName",
  "timestamp": 123456,
  "uuid": "your-uuid-here"
}
```

## License

[Your License Here]

## Contributing

Contributions welcome! Please open an issue or submit a pull request.

## Support

- **Issues**: [GitHub Issues](../../issues)
- **BAR Community**: [Beyond All Reason Discord](https://discord.gg/beyond-all-reason)
