# BAR Relay

A lightweight TCP-to-HTTP relay server that forwards JSON messages to a web API with bearer token authentication.

## Overview

BAR Relay listens on a local TCP port and relays incoming JSON data to a remote API endpoint. It's designed to bridge local applications that send JSON over TCP with web APIs that require HTTP requests and authentication.

## Features

- TCP server accepts JSON messages on a configurable host/port
- Forwards messages to a web API via HTTP POST
- Supports bearer token authentication
- Persistent token storage in `~/.bar_token`
- Concurrent connection handling
- Optional verbose logging

## Installation

```bash
go build -o bar-relay main.go
```

## Usage

### First Run

On first run, you'll be prompted to enter your Personal Access Token:

```bash
./bar-relay
```

The token is securely stored in `~/.bar_token` for future use.

### Command Line Options

```bash
./bar-relay [options]

Options:
  -host string      IP address to listen on (default "127.0.0.1")
  -port string      TCP port to listen on (default "5005")
  -url string       Destination API URL (default "https://barapi.bobmitch.com/push")
  -token string     Personal Access Token (overrides stored token)
  -reset            Clear stored token and prompt for new one
  -v                Enable verbose logging
```

### Examples

Start with default settings:
```bash
./bar-relay
```

Listen on a different port:
```bash
./bar-relay -port 8080
```

Use a different API endpoint:
```bash
./bar-relay -url https://api.example.com/data
```

Enable verbose logging:
```bash
./bar-relay -v
```

Reset stored token:
```bash
./bar-relay -reset
```

## How It Works

1. Listens for TCP connections on the specified host:port
2. Receives JSON data from connected clients
3. Forwards the JSON to the configured API URL via HTTP POST
4. Adds `Authorization: Bearer <token>` header for authentication
5. Logs API responses (always shown for errors, optional with `-v` flag)

## License

MIT
