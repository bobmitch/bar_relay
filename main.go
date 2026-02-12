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
	Timeout: 10 * time.Second,
}

const configFileName = ".bar_uuid"

type RecordedEvent struct {
	Timestamp time.Time              `json:"t"`
	Data      map[string]interface{} `json:"d"`
}

type EventBatcher struct {
	mu         sync.Mutex
	buffer     []map[string]interface{}
	idleTimer  *time.Timer
	batchTimer *time.Timer

	firstEventTime time.Time
	isInBatchMode  bool

	softTimeout time.Duration
	hardTimeout time.Duration

	uuid      string
	apiUrl    string
	apiClient *http.Client
	verbose   bool

	// Stats counters
	startTime     time.Time
	totalEvents   int64
	totalRequests int64
	totalBytes    int64

	// Recording fields
	recorder *json.Encoder
	recordMu sync.Mutex
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

	return eb
}

func (b *EventBatcher) Add(eventJSON string) {
	var event map[string]interface{}
	if err := json.Unmarshal([]byte(eventJSON), &event); err != nil {
		fmt.Printf("\nError parsing event JSON: %v\n", err)
		return
	}

	b.mu.Lock()
	b.totalEvents++
	b.mu.Unlock()

	if b.recorder != nil {
		b.recordMu.Lock()
		b.recorder.Encode(RecordedEvent{
			Timestamp: time.Now(),
			Data:      event,
		})
		b.recordMu.Unlock()
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.buffer) == 0 {
		b.firstEventTime = time.Now()
		b.isInBatchMode = false
		b.buffer = append(b.buffer, event)
		if b.idleTimer != nil { b.idleTimer.Stop() }
		b.idleTimer = time.AfterFunc(b.softTimeout, b.onSoftTimeout)
		return
	}

	b.buffer = append(b.buffer, event)

	if !b.isInBatchMode && time.Since(b.firstEventTime) < b.softTimeout {
		if len(b.buffer) >= 2 {
			b.isInBatchMode = true
			if b.idleTimer != nil { b.idleTimer.Stop() }
			b.batchTimer = time.AfterFunc(b.hardTimeout, b.onHardTimeout)
		}
	}

	if b.isInBatchMode {
		if b.batchTimer != nil { b.batchTimer.Stop() }
		b.batchTimer = time.AfterFunc(b.hardTimeout, b.onHardTimeout)
	}
}

func (b *EventBatcher) onSoftTimeout() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.buffer) == 0 { return }
	if len(b.buffer) == 1 {
		b.flushUnsafe()
		return
	}
	b.isInBatchMode = true
	b.batchTimer = time.AfterFunc(b.hardTimeout, b.onHardTimeout)
}

func (b *EventBatcher) onHardTimeout() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.buffer) > 0 { b.flushUnsafe() }
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

	b.sendToAPI(payload)
	b.buffer = make([]map[string]interface{}, 0)
	b.isInBatchMode = false

	// Pulse Log
	kbSent := float64(b.totalBytes) / 1024.0
	fmt.Printf("\r🚀 [Relay] Events: %-6d | Requests: %-4d | Sent: %-7.2f KB", 
		b.totalEvents, b.totalRequests, kbSent)
}

func (b *EventBatcher) sendToAPI(payload []byte) {
	req, err := http.NewRequest("POST", b.apiUrl, bytes.NewBuffer(payload))
	if err != nil { return }
	req.Header.Set("Content-Type", "application/json")
	
	resp, err := b.apiClient.Do(req)
	b.totalRequests++
	b.totalBytes += int64(len(payload))

	if err != nil {
		fmt.Printf("\n❌ API Error: %v\n", err)
		return
	}
	defer resp.Body.Close()

	if b.verbose {
		fmt.Printf("\n[Verbose] Payload: %d bytes | Status: %d\n", len(payload), resp.StatusCode)
	}
}

// PrintFinalSummary displays the grand totals
func (b *EventBatcher) PrintFinalSummary() {
	duration := time.Since(b.startTime).Round(time.Second)
	kbSent := float64(b.totalBytes) / 1024.0

	fmt.Println("\n\n--- 🏁 Final Session Summary ---")
	fmt.Printf("⏱️  Duration:     %v\n", duration)
	fmt.Printf("📈 Total Events: %d\n", b.totalEvents)
	fmt.Printf("🌐 API Requests: %d\n", b.totalRequests)
	fmt.Printf("💾 Total Sent:   %.2f KB\n", kbSent)
	fmt.Println("--------------------------------")
}

func ReplaySession(filePath string, batcher *EventBatcher, speed float64) {
	f, err := os.Open(filePath)
	if err != nil {
		fmt.Printf("\nReplay Error: %v\n", err)
		return
	}
	defer f.Close()

	fmt.Printf("▶️  Replaying: %s (%.1fx speed)\n", filePath, speed)
	scanner := bufio.NewScanner(f)
	var lastTime time.Time

	for scanner.Scan() {
		var rec RecordedEvent
		if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil { continue }
		if !lastTime.IsZero() {
			delay := rec.Timestamp.Sub(lastTime)
			time.Sleep(time.Duration(float64(delay) / speed))
		}
		dataStr, _ := json.Marshal(rec.Data)
		batcher.Add(string(dataStr))
		lastTime = rec.Timestamp
	}
	fmt.Println("\n🏁 Replay finished.")
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
	data, err := os.ReadFile(configPath)
	if err == nil { return strings.TrimSpace(string(data)) }
	fmt.Print("No UUID found. Please paste your UUID: ")
	reader := bufio.NewReader(os.Stdin)
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(input)
	if input == "" { os.Exit(1) }
	os.WriteFile(configPath, []byte(input), 0600)
	return input
}

func init() {
	flag.StringVar(&host, "host", defaultHost, "IP to listen on")
	flag.StringVar(&port, "port", defaultPort, "Port to listen on")
	flag.StringVar(&apiUrl, "url", defaultAPIUrl, "Web API URL")
	flag.StringVar(&uuid, "uuid", "", "Your UUID")
	flag.BoolVar(&verbose, "v", defaultVerbose, "Verbose logging")
	flag.BoolVar(&reset, "reset", false, "Clear UUID")
	flag.StringVar(&recordFile, "record", "", "Record session ('auto' or filename)")
	flag.StringVar(&replayFile, "replay", "", "Replay file")
	flag.Float64Var(&replaySpeed, "speed", 1.0, "Replay speed")
}

func main() {
	flag.Parse()

	if reset {
		home, _ := os.UserHomeDir()
		os.Remove(filepath.Join(home, configFileName))
		fmt.Println("UUID Reset.")
	}

	if uuid == "" { uuid = getUUID() }

	batcher := NewEventBatcher(uuid, apiUrl, verbose, recordFile)

	// --- Signal Handling for Graceful Shutdown ---
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

	address := net.JoinHostPort(host, port)
	listener, err := net.Listen("tcp", address)
	if err != nil {
		fmt.Printf("Fatal Error: %v\n", err)
		return
	}
	defer listener.Close()

	fmt.Printf("--- BAR Relay Active ---\n")
	if replayFile == "" {
		fmt.Printf("📡 Listening on: %s\n", address)
	}

	for {
		conn, err := listener.Accept()
		if err != nil { continue }
		handler := &ConnectionHandler{conn: conn, batcher: batcher}
		go handler.Handle()
	}
}
