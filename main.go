package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
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

var (
	defaultHost    = "127.0.0.1"
	defaultPort    = "5005"
	defaultAPIUrl  = "https://barapi.bobmitch.com/push"
	defaultVerbose = false
)

var (
	host        string
	port        string
	apiUrl      string
	uuid        string
	verbose     bool
	reset       bool
	recordFile  string
	replayFile  string
	replaySpeed float64
)

var apiClient = &http.Client{
	Timeout: 5 * time.Second,
}

const (
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
	mu         sync.Mutex
	buffer     []map[string]interface{}
	idleTimer  *time.Timer
	batchTimer *time.Timer

	isInBatchMode bool

	softTimeout time.Duration
	hardTimeout time.Duration

	uuid      string
	apiUrl    string
	apiClient *http.Client
	verbose   bool

	// Stats
	startTime     time.Time
	totalEvents   int64
	totalRequests int64
	totalBytes    int64
	droppedEvents int64

	// Recording
	recorder *json.Encoder
	recordMu sync.Mutex

	// Retry Queue
	retryQueue []retryItem
	retryMu    sync.Mutex
}

func NewEventBatcher(uuid, apiUrl string, verbose bool, recordPath string) *EventBatcher {
	eb := &EventBatcher{
		buffer:      make([]map[string]interface{}, 0),
		softTimeout: 100 * time.Millisecond,
		hardTimeout: 250 * time.Millisecond,
		uuid:        uuid,
		apiUrl:      apiUrl,
		apiClient:   apiClient,
		verbose:     verbose,
		startTime:   time.Now(),
		retryQueue:  make([]retryItem, 0),
	}

	if recordPath != "" {
		if recordPath == "auto" {
			recordPath = fmt.Sprintf("session_%s.jsonl", time.Now().Format("2006-01-02_15-04-05"))
		}
		f, err := os.OpenFile(recordPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			fmt.Printf("⚠️  Failed to open record file: %v\n", err)
		} else {
			eb.recorder = json.NewEncoder(f)
			fmt.Printf("📂 Recording session to: %s\n", recordPath)
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

		var validItems []retryItem
		now := time.Now()
		for _, item := range b.retryQueue {
			if now.Sub(item.timestamp) < maxRetryAge {
				validItems = append(validItems, item)
			} else {
				b.mu.Lock()
				b.droppedEvents++
				b.mu.Unlock()
			}
		}
		b.retryQueue = validItems

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

func (b *EventBatcher) Add(eventJSON string) {
	var event map[string]interface{}
	if err := json.Unmarshal([]byte(eventJSON), &event); err != nil {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	b.totalEvents++

	if b.recorder != nil {
		b.recordMu.Lock()
		b.recorder.Encode(RecordedEvent{Timestamp: time.Now(), Data: event})
		b.recordMu.Unlock()
	}

	b.buffer = append(b.buffer, event)

	// If this is the very first event in a new batch
	if len(b.buffer) == 1 {
		b.isInBatchMode = false
		if b.idleTimer != nil { b.idleTimer.Stop() }
		b.idleTimer = time.AfterFunc(b.softTimeout, b.onSoftTimeout)
	} else if !b.isInBatchMode {
		// Second event arrived within the soft window
		b.isInBatchMode = true
		if b.idleTimer != nil { b.idleTimer.Stop() }
		// FIXED: Only start the hard timer ONCE per batch. Do not reset it.
		if b.batchTimer == nil {
			b.batchTimer = time.AfterFunc(b.hardTimeout, b.onHardTimeout)
		}
	}
}

func (b *EventBatcher) onSoftTimeout() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.buffer) == 0 { return }
	
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
	if len(b.buffer) == 0 { return }

	if b.idleTimer != nil { b.idleTimer.Stop(); b.idleTimer = nil }
	if b.batchTimer != nil { b.batchTimer.Stop(); b.batchTimer = nil }

	for i := range b.buffer { b.buffer[i]["uuid"] = b.uuid }

	var payload []byte
	if len(b.buffer) == 1 {
		payload, _ = json.Marshal(b.buffer[0])
	} else {
		payload, _ = json.Marshal(b.buffer)
	}

	go b.sendToAPI(payload, false)

	b.buffer = make([]map[string]interface{}, 0)
	b.isInBatchMode = false

	kbSent := float64(b.totalBytes) / 1024.0
	fmt.Printf("\r🚀 [Relay] Events: %-6d | Requests: %-4d | Sent: %-7.2f KB", 
		b.totalEvents, b.totalRequests, kbSent)
}

func (b *EventBatcher) sendToAPI(payload []byte, isRetry bool) {
	req, err := http.NewRequest("POST", b.apiUrl, bytes.NewBuffer(payload))
	if err != nil { return }
	req.Header.Set("Content-Type", "application/json")
	
	resp, err := b.apiClient.Do(req)
	if err != nil || (resp != nil && resp.StatusCode >= 400) {
		if !isRetry {
			b.retryMu.Lock()
			b.retryQueue = append(b.retryQueue, retryItem{payload: payload, timestamp: time.Now()})
			b.retryMu.Unlock()
		}
		if resp != nil { resp.Body.Close() }
		return
	}
	defer resp.Body.Close()

	b.mu.Lock()
	b.totalRequests++
	b.totalBytes += int64(len(payload))
	b.mu.Unlock()
}

func (b *EventBatcher) PrintFinalSummary() {
	duration := time.Since(b.startTime).Round(time.Second)
	kbSent := float64(b.totalBytes) / 1024.0
	fmt.Println("\n\n--- 🏁 Final Session Summary ---")
	fmt.Printf("⏱️  Duration:     %v\n", duration)
	fmt.Printf("📈 Total Events: %d\n", b.totalEvents)
	fmt.Printf("🌐 API Requests: %d\n", b.totalRequests)
	fmt.Printf("💾 Total Sent:   %.2f KB\n", kbSent)
	if b.droppedEvents > 0 {
		fmt.Printf("🗑️  Dropped:      %d events (stale)\n", b.droppedEvents)
	}
	fmt.Println("--------------------------------")
}

func ReplaySession(filePath string, batcher *EventBatcher, speed float64) {
	f, err := os.Open(filePath)
	if err != nil { return }
	defer f.Close()

	scanner := bufio.NewScanner(f)
	var lastTime time.Time
	for scanner.Scan() {
		var rec RecordedEvent
		json.Unmarshal(scanner.Bytes(), &rec)
		if !lastTime.IsZero() {
			time.Sleep(time.Duration(float64(rec.Timestamp.Sub(lastTime)) / speed))
		}
		raw, _ := json.Marshal(rec.Data)
		batcher.Add(string(raw))
		lastTime = rec.Timestamp
	}
}

type ConnectionHandler struct {
	conn    net.Conn
	batcher *EventBatcher
}

func (ch *ConnectionHandler) Handle() {
	defer ch.conn.Close()
	scanner := bufio.NewScanner(ch.conn)
	for scanner.Scan() {
		ch.batcher.Add(scanner.Text())
		ch.conn.Write([]byte("ACK\n"))
	}
}

func getUUID() string {
	home, _ := os.UserHomeDir()
	configPath := filepath.Join(home, configFileName)
	data, _ := os.ReadFile(configPath)
	if len(data) > 0 { return strings.TrimSpace(string(data)) }
	fmt.Print("Paste UUID: ")
	var input string
	fmt.Scanln(&input)
	os.WriteFile(configPath, []byte(input), 0600)
	return input
}

func main() {
	flag.Parse()
	if reset {
		home, _ := os.UserHomeDir()
		os.Remove(filepath.Join(home, configFileName))
	}
	u := uuid
	if u == "" { u = getUUID() }

	batcher := NewEventBatcher(u, apiUrl, verbose, recordFile)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		batcher.PrintFinalSummary()
		os.Exit(0)
	}()

	if replayFile != "" {
		go ReplaySession(replayFile, batcher, replaySpeed)
	}

	l, _ := net.Listen("tcp", net.JoinHostPort(host, port))
	fmt.Printf("--- BAR Relay Active ---\n")
	for {
		conn, _ := l.Accept()
		go (&ConnectionHandler{conn: conn, batcher: batcher}).Handle()
	}
}
