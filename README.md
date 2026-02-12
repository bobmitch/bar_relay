This is a perfect way to wrap up the project. A solid README makes it much easier to jump back into the code a few months from now without having to dig through the source to remember the flags.

I have included a section on the **Batching Logic**, the **60s Retry Buffer**, and the **Replay Speed** functionality.

---

# BAR Event Relay & Recorder

A high-performance Go-based relay for **Beyond All Reason (BAR)**. This tool accepts JSON events from the game via TCP, batches them intelligently to save bandwidth, and relays them to a web API. It also features a session recorder and a time-accurate replayer.

## 🚀 Features

* **Intelligent Batching**: Groups rapid-fire game events into single HTTP requests using soft (100ms) and hard (250ms) timeouts.
* **Session Recording**: Saves game events to `.jsonl` format with high-precision timestamps.
* **Smart Replay**: Play back recorded sessions at **1x** or **2x+** speeds with original timing preserved.
* **Resilience**: Implements a **60-second Retry Buffer**. If the API is down, data is cached and retried, but stale data (>60s) is dropped to prevent outdated audio/visual cues.
* **Live Stats**: Real-time terminal tracking of events, requests sent, and KB throughput.

## 🛠 Installation

1. Ensure you have [Go](https://go.dev/dl/) installed.
2. Clone this repository or save `main.go`.
3. Build the binary:
```bash
go build -o bar-relay main.go

```



## 📖 Usage

### Basic Relay

Start the relay with your UUID. It will listen on `127.0.0.1:5005` by default.

```bash
./bar-relay -uuid YOUR_UUID_HERE

```

### Recording Sessions

Use the `-record` flag to save your session.

```bash
# Auto-generate a filename based on the current timestamp
./bar-relay -record auto

# Or specify a custom filename
./bar-relay -record my_game_session.jsonl

```

### Replaying Sessions

Feed a recorded file back through the relay logic (and to the API).

```bash
# Normal speed
./bar-relay -replay session_2026-02-11.jsonl

# 2x Speed
./bar-relay -replay session_2026-02-11.jsonl -speed 2.0

```

## ⚙️ Configuration Flags

| Flag | Default | Description |
| --- | --- | --- |
| `-host` | `127.0.0.1` | The IP address to listen on. |
| `-port` | `5005` | The TCP port to listen on. |
| `-url` | `.../push` | The destination Web API URL. |
| `-uuid` | `""` | Your unique identifier (stored in `.bar_uuid`). |
| `-record` | `""` | File path to record (`auto` for timestamped names). |
| `-replay` | `""` | File path of a session to play back. |
| `-speed` | `1.0` | Playback speed multiplier (e.g., `2.0`, `0.5`). |
| `-v` | `false` | Enable verbose logging for debugging. |
| `-reset` | `false` | Clear the stored UUID and prompt for a new one. |

## 📊 Performance & Reliability

### Batching Logic

The relay uses a dual-timer approach:

1. **Soft Timeout (100ms)**: If a single event arrives and nothing follows, it sends immediately.
2. **Hard Timeout (250ms)**: If events are flooding in, it forces a batch send every quarter-second to maintain a balance between "real-time" feel and server efficiency.

### Graceful Shutdown

Pressing `Ctrl+C` will trigger a **Final Session Summary**, showing:

* Total session duration.
* Total events processed.
* Actual HTTP requests made (efficiency tracking).
* Total data transferred in KB.
* Number of events dropped due to API downtime > 60s.

---

Would you like me to help you set up a **GitHub Action** so that this binary is automatically compiled for Windows, Mac, and Linux every time you push code?
