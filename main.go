package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Global Config & Stats
var (
	host           string
	port           string
	apiUrl         string
	uuid           string
	verbose        bool
	reset          bool
	recordFile     string
	replayFile     string
	replaySpeed    float64
	apiClient      = &http.Client{Timeout: 5 * time.Second}
	configFileName = ".bar_uuid"
	maxRetryAge    = 60 * time.Second
)

type RecordedEvent struct {
	Timestamp time.Time              `json:"t"`
	Data      map[string]interface{} `json:"d"`
}

type retryItem struct {
	payload   []byte
	timestamp time.Time
}

type EventBatcher struct {
	mu            sync.Mutex
	buffer        []map[string]interface{}
	idleTimer     *time.Timer
	batchTimer    *time.Timer
	isInBatchMode bool
	softTimeout   time.Duration
	hardTimeout   time.Duration
	uuid          string
	apiUrl        string
	apiClient     *http.Client
	verbose       bool
	startTime     time.Time
	totalEvents   int64
	totalRequests int64
	totalBytes    int64
	droppedEvents int64
	invalidEvents int64 // TRACKING JSON ERRORS
	recorder      *json.Encoder
	recordMu      sync.Mutex
	retryQueue    []retryItem
	retryMu       sync.Mutex
}

// --- Logic ---

func NewEventBatcher(u, url string, v bool, recPath string) *EventBatcher {
	eb := &EventBatcher{
		buffer:      make([]map[string]interface{}, 0),
		softTimeout: 100 * time.Millisecond,
		hardTimeout: 250 * time.Millisecond,
		uuid:        u,
		apiUrl:      url,
		apiClient:   apiClient,
		verbose:     v,
		startTime:   time.Now(),
		retryQueue:  make([]retryItem, 0),
	}

	if recPath != "" {
		if recPath == "auto" {
			recPath = fmt.Sprintf("session_%s.jsonl", time.Now().Format("2006-01-02_15-04-05"))
		}
		f, err := os.OpenFile(recPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			fmt.Printf("⚠️  Record error: %v\n", err)
		} else {
			eb.recorder = json.NewEncoder(f)
			fmt.Printf("📂 Recording to: %s\n", recPath)
		}
	}
	go eb.retryWorker()
	return eb
}

func (b *EventBatcher) retryWorker() {
	for {
		time.Sleep(5 * time.Second)
		b.retryMu.Lock()
		if len(b.retryQueue) == 0 {
			b.retryMu.Unlock()
			continue
		}
		var valid []retryItem
		now := time.Now()
		for _, item := range b.retryQueue {
			if now.Sub(item.timestamp) < maxRetryAge {
				valid = append(valid, item)
			} else {
				b.mu.Lock()
				b.droppedEvents++
				b.mu.Unlock()
			}
		}
		b.retryQueue = valid
		if len(b.retryQueue) > 0 {
			item := b.retryQueue[0]
			b.retryQueue = b.retryQueue[1:]
			b.retryMu.Unlock()
			b.sendToAPI(item.payload, true)
		} else {
			b.retryMu.Unlock()
		}
	}
}

func (b *EventBatcher) Add(jsonStr string) {
	var data map[string]interface{}
	// FIX: Report JSON unmarshalling errors
	if err := json.Unmarshal([]byte(jsonStr), &data); err != nil {
		b.mu.Lock()
		b.invalidEvents++
		b.mu.Unlock()

		if b.verbose {
			fmt.Printf("\n[!] JSON Parse Error: %v | Input: %q\n", err, jsonStr)
		}
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	b.totalEvents++

	if b.recorder != nil {
		b.recordMu.Lock()
		b.recorder.Encode(RecordedEvent{Timestamp: time.Now(), Data: data})
		b.recordMu.Unlock()
	}

	b.buffer = append(b.buffer, data)

	if len(b.buffer) == 1 {
		b.isInBatchMode = false
		if b.idleTimer != nil {
			b.idleTimer.Stop()
		}
		b.idleTimer = time.AfterFunc(b.softTimeout, b.onSoftTimeout)
	} else if !b.isInBatchMode {
		b.isInBatchMode = true
		if b.idleTimer != nil {
			b.idleTimer.Stop()
		}
		if b.batchTimer == nil {
			b.batchTimer = time.AfterFunc(b.hardTimeout, b.onHardTimeout)
		}
	}
}

func (b *EventBatcher) onSoftTimeout() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.buffer) == 0 {
		return
	}
	if len(b.buffer) == 1 {
		b.flushUnsafe()
	} else {
		b.isInBatchMode = true
		if b.batchTimer == nil {
			b.batchTimer = time.AfterFunc(b.hardTimeout, b.onHardTimeout)
		}
	}
}

func (b *EventBatcher) onHardTimeout() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.flushUnsafe()
}

func (b *EventBatcher) flushUnsafe() {
	if len(b.buffer) == 0 {
		return
	}
	if b.idleTimer != nil {
		b.idleTimer.Stop()
		b.idleTimer = nil
	}
	if b.batchTimer != nil {
		b.batchTimer.Stop()
		b.batchTimer = nil
	}

	for i := range b.buffer {
		b.buffer[i]["uuid"] = b.uuid
	}

	var payload []byte
	if len(b.buffer) == 1 {
		payload, _ = json.Marshal(b.buffer[0])
	} else {
		payload, _ = json.Marshal(b.buffer)
	}

	go b.sendToAPI(payload, false)
	b.buffer = make([]map[string]interface{}, 0)
	b.isInBatchMode = false

	kb := float64(b.totalBytes) / 1024.0
	fmt.Printf("\r🚀 [Relay] Events: %-6d | Req: %-4d | Sent: %-7.2f KB", b.totalEvents, b.totalRequests, kb)
}

func (b *EventBatcher) sendToAPI(p []byte, retry bool) {
	req, err := http.NewRequest("POST", b.apiUrl, bytes.NewBuffer(p))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := apiClient.Do(req)
	if err != nil || (resp != nil && resp.StatusCode >= 400) {
		if !retry {
			b.retryMu.Lock()
			b.retryQueue = append(b.retryQueue, retryItem{payload: p, timestamp: time.Now()})
			b.retryMu.Unlock()
		}
		if resp != nil {
			resp.Body.Close()
		}
		return
	}
	defer resp.Body.Close()

	b.mu.Lock()
	b.totalRequests++
	b.totalBytes += int64(len(p))
	b.mu.Unlock()

	if b.verbose {
		fmt.Printf("\n[v] Sent %d bytes (Status: %d)\n", len(p), resp.StatusCode)
	}
}

func (b *EventBatcher) PrintFinalSummary() {
	kb := float64(b.totalBytes) / 1024.0
	fmt.Println("\n\n--- 🏁 Session Summary ---")
	fmt.Printf("📈 Events:   %d\n🌐 Requests: %d\n💾 Data:     %.2f KB\n", b.totalEvents, b.totalRequests, kb)
	if b.droppedEvents > 0 {
		fmt.Printf("🗑️  Dropped:  %d (stale)\n", b.droppedEvents)
	}
	if b.invalidEvents > 0 {
		fmt.Printf("❌ Invalid:  %d (JSON errors)\n", b.invalidEvents)
	}
	fmt.Println("--------------------------")
}

func getUUID() string {
	home, _ := os.UserHomeDir()
	path := filepath.Join(home, configFileName)
	data, _ := os.ReadFile(path)
	if len(data) > 0 {
		return strings.TrimSpace(string(data))
	}
	fmt.Print("🔑 Paste UUID: ")
	var input string
	fmt.Scanln(&input)
	os.WriteFile(path, []byte(input), 0600)
	return input
}

func main() {
	// 1. Setup Flags
	flag.StringVar(&host, "host", "127.0.0.1", "IP to listen on")
	flag.StringVar(&port, "port", "5005", "Port to listen on")
	flag.StringVar(&apiUrl, "url", "https://barapi.bobmitch.com/push", "Web API URL")
	flag.StringVar(&uuid, "uuid", "", "Your unique UUID")
	flag.BoolVar(&verbose, "v", false, "Enable verbose logging")
	flag.BoolVar(&reset, "reset", false, "Reset stored UUID")
	flag.StringVar(&recordFile, "record", "", "Record to file (or 'auto')")
	flag.StringVar(&replayFile, "replay", "", "Replay a .jsonl file")
	flag.Float64Var(&replaySpeed, "speed", 1.0, "Replay speed (e.g. 2.0)")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage of BAR Relay:\n")
		flag.PrintDefaults()
	}

	flag.Parse()

	// 2. Logic
	if reset {
		home, _ := os.UserHomeDir()
		os.Remove(filepath.Join(home, configFileName))
		fmt.Println("✅ UUID cleared.")
	}

	u := uuid
	if u == "" {
		u = getUUID()
	}

	batcher := NewEventBatcher(u, apiUrl, verbose, recordFile)

	// Shutdown handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		batcher.PrintFinalSummary()
		os.Exit(0)
	}()

	// Updated Replay logic to handle unmarshal errors
	if replayFile != "" {
		go func() {
			f, err := os.Open(replayFile)
			if err != nil {
				fmt.Printf("❌ Replay error: %v\n", err)
				return
			}
			defer f.Close()

			reader := bufio.NewReader(f)
			var last time.Time
			for {
				line, err := reader.ReadString('\n')
				if err != nil {
					break
				}
				var rec RecordedEvent
				if err := json.Unmarshal([]byte(line), &rec); err != nil {
					batcher.mu.Lock()
					batcher.invalidEvents++
					batcher.mu.Unlock()
					if verbose {
						fmt.Printf("\n[!] Replay JSON Error: %v | Line: %q\n", err, line)
					}
					continue
				}
				if !last.IsZero() {
					time.Sleep(time.Duration(float64(rec.Timestamp.Sub(last)) / replaySpeed))
				}
				raw, _ := json.Marshal(rec.Data)
				batcher.Add(string(raw))
				last = rec.Timestamp
			}
			fmt.Println("\n🏁 Replay finished.")
		}()
	}

	addr := net.JoinHostPort(host, port)
	l, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Printf("❌ Fatal: %v\n", err)
		return
	}
	fmt.Printf("📡 BAR Relay: %s\n", addr)

	for {
		conn, err := l.Accept()
		if err == nil {
			go func(c net.Conn) {
				defer c.Close()
				reader := bufio.NewReader(c)
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						if err != io.EOF && verbose {
							fmt.Printf("\n[!] Conn Error: %v\n", err)
						}
						break
					}

					line = strings.TrimSpace(line)
					if line == "" {
						continue
					}

					batcher.Add(line)
					c.Write([]byte("ACK\n"))
				}
			}(conn)
		}
	}
}
