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
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	defaultHost    = "127.0.0.1"
	defaultPort    = "5005"
	defaultAPIUrl  = "https://barapi.bobmitch.com/push"
	defaultVerbose = false
)

var (
	host    string
	port    string
	apiUrl  string
	uuid    string
	verbose bool
	reset   bool
)

var apiClient = &http.Client{
	Timeout: 10 * time.Second,
}

const configFileName = ".bar_uuid"

// EventBatcher handles intelligent batching of events
type EventBatcher struct {
	mu              sync.Mutex
	buffer          []map[string]interface{}
	
	idleTimer       *time.Timer      // Soft timeout: send isolated events quickly
	batchTimer      *time.Timer      // Hard timeout: force flush during saturation
	
	firstEventTime  time.Time
	isInBatchMode   bool
	
	// Configuration (tuned for game dynamics)
	softTimeout     time.Duration    // 100ms: detect if more events coming
	hardTimeout     time.Duration    // 250ms: maximum batch wait during saturation
	
	// Context
	uuid            string
	apiUrl          string
	apiClient       *http.Client
	verbose         bool
}

func NewEventBatcher(uuid, apiUrl string, verbose bool) *EventBatcher {
	return &EventBatcher{
		buffer:      make([]map[string]interface{}, 0),
		softTimeout: 100 * time.Millisecond,
		hardTimeout: 250 * time.Millisecond,
		uuid:        uuid,
		apiUrl:      apiUrl,
		apiClient:   apiClient,
		verbose:     verbose,
	}
}

// Add adds an event to the batcher and manages timing
func (b *EventBatcher) Add(eventJSON string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	var event map[string]interface{}
	if err := json.Unmarshal([]byte(eventJSON), &event); err != nil {
		fmt.Printf("Error parsing event JSON: %v\n", err)
		return
	}

	// First event in buffer
	if len(b.buffer) == 0 {
		b.firstEventTime = time.Now()
		b.isInBatchMode = false
		b.buffer = append(b.buffer, event)

		if b.verbose {
			fmt.Printf("[Batcher] First event received, starting soft timer (100ms)\n")
		}

		// Start soft timer to detect isolated vs persistent events
		if b.idleTimer != nil {
			b.idleTimer.Stop()
		}
		b.idleTimer = time.AfterFunc(b.softTimeout, b.onSoftTimeout)
		return
	}

	// Subsequent events
	b.buffer = append(b.buffer, event)

	// If in soft window (< 100ms) and now have 2+ events, transition to batch mode
	if !b.isInBatchMode && time.Since(b.firstEventTime) < b.softTimeout {
		if len(b.buffer) >= 2 {
			if b.verbose {
				fmt.Printf("[Batcher] Multiple events detected during soft window, switching to batch mode\n")
			}
			b.isInBatchMode = true
			// Stop soft timer and start hard timer
			if b.idleTimer != nil {
				b.idleTimer.Stop()
			}
			b.batchTimer = time.AfterFunc(b.hardTimeout, b.onHardTimeout)
		}
	}

	// If in batch mode, refresh the hard timer (keep extending deadline while events arrive)
	if b.isInBatchMode {
		if b.batchTimer != nil {
			b.batchTimer.Stop()
		}
		b.batchTimer = time.AfterFunc(b.hardTimeout, b.onHardTimeout)
	}
}

// onSoftTimeout fires when 100ms passes in soft window
// Decision: single event? send immediately. multiple? switch to batch mode
func (b *EventBatcher) onSoftTimeout() {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.buffer) == 0 {
		return
	}

	if b.verbose {
		fmt.Printf("[Batcher] Soft timeout fired. Buffer size: %d\n", len(b.buffer))
	}

	// Only 1 event: send it immediately (isolated event)
	if len(b.buffer) == 1 {
		if b.verbose {
			fmt.Printf("[Batcher] Single event, sending immediately\n")
		}
		b.flushUnsafe()
		return
	}

	// Multiple events: switch to batch mode with hard timeout
	if b.verbose {
		fmt.Printf("[Batcher] Multiple events detected, entering batch mode with 250ms deadline\n")
	}
	b.isInBatchMode = true
	b.batchTimer = time.AfterFunc(b.hardTimeout, b.onHardTimeout)
}

// onHardTimeout fires when 250ms passes during batch mode
// Force flush regardless of incoming events
func (b *EventBatcher) onHardTimeout() {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.buffer) > 0 {
		if b.verbose {
			fmt.Printf("[Batcher] Hard timeout fired, flushing %d events\n", len(b.buffer))
		}
		b.flushUnsafe()
	}
}

// flushUnsafe sends buffered events to API (must hold mutex)
func (b *EventBatcher) flushUnsafe() {
	if len(b.buffer) == 0 {
		return
	}

	// Stop any pending timers
	if b.idleTimer != nil {
		b.idleTimer.Stop()
		b.idleTimer = nil
	}
	if b.batchTimer != nil {
		b.batchTimer.Stop()
		b.batchTimer = nil
	}

	// Add UUID to all events
	for i := range b.buffer {
		b.buffer[i]["uuid"] = b.uuid
	}

	// Determine payload format
	var payload []byte
	if len(b.buffer) == 1 {
		// Single event: send as object
		payload, _ = json.Marshal(b.buffer[0])
		if b.verbose {
			fmt.Printf("[Batcher] Sending single event: %s\n", string(payload))
		}
	} else {
		// Multiple events: send as array
		payload, _ = json.Marshal(b.buffer)
		if b.verbose {
			fmt.Printf("[Batcher] Sending batch of %d events\n", len(b.buffer))
		}
	}

	b.sendToAPI(payload)

	// Clear buffer and reset state
	b.buffer = make([]map[string]interface{}, 0)
	b.isInBatchMode = false
}

// sendToAPI performs the HTTP POST to the PHP endpoint
func (b *EventBatcher) sendToAPI(payload []byte) {
	req, err := http.NewRequest("POST", b.apiUrl, bytes.NewBuffer(payload))
	if err != nil {
		fmt.Printf("Request Creation Error: %v\n", err)
		return
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := b.apiClient.Do(req)
	if err != nil {
		fmt.Printf("API Error: %v\n", err)
		return
	}
	defer resp.Body.Close()

	if b.verbose || resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		fmt.Printf("API Response (%d): %s\n", resp.StatusCode, string(body))
	}
}

// ConnectionHandler manages a single connection from LUA widget
type ConnectionHandler struct {
	conn    net.Conn
	batcher *EventBatcher
	verbose bool
}

func NewConnectionHandler(conn net.Conn, batcher *EventBatcher, verbose bool) *ConnectionHandler {
	return &ConnectionHandler{
		conn:    conn,
		batcher: batcher,
		verbose: verbose,
	}
}

func (ch *ConnectionHandler) Handle() {
	defer ch.conn.Close()
	fmt.Printf("New connection from: %s\n", ch.conn.RemoteAddr())

	scanner := bufio.NewScanner(ch.conn)
	for scanner.Scan() {
		jsonData := scanner.Text()

		if ch.verbose {
			fmt.Printf("Received: %s\n", jsonData)
		}

		// Add to batcher (handles intelligent buffering)
		ch.batcher.Add(jsonData)

		// Send ACK to LUA
		ch.conn.Write([]byte("ACK\n"))
	}

	if err := scanner.Err(); err != nil {
		fmt.Printf("Connection error: %v\n", err)
	}
	fmt.Printf("Connection closed for: %s\n", ch.conn.RemoteAddr())
}

// UUID Management
func getUUID() string {
	home, _ := os.UserHomeDir()
	configPath := filepath.Join(home, configFileName)

	data, err := os.ReadFile(configPath)
	if err == nil {
		return strings.TrimSpace(string(data))
	}

	fmt.Print("No UUID found. Please paste your UUID: ")
	reader := bufio.NewReader(os.Stdin)
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(input)

	if input == "" {
		fmt.Println("Error: UUID cannot be empty.")
		os.Exit(1)
	}

	os.WriteFile(configPath, []byte(input), 0600)
	fmt.Printf("UUID saved to %s\n", configPath)

	return input
}

func init() {
	flag.StringVar(&host, "host", defaultHost, "The IP address to listen on")
	flag.StringVar(&port, "port", defaultPort, "The TCP port to listen on")
	flag.StringVar(&apiUrl, "url", defaultAPIUrl, "The destination Web API URL")
	flag.StringVar(&uuid, "uuid", "", "Your UUID from the web app")
	flag.BoolVar(&verbose, "v", defaultVerbose, "Enable detailed logging")
	flag.BoolVar(&reset, "reset", false, "Clear the stored UUID and prompt for a new one")
}

func main() {
	flag.Parse()

	if reset {
		home, _ := os.UserHomeDir()
		os.Remove(filepath.Join(home, configFileName))
		fmt.Println("Stored UUID cleared.")
	}

	if uuid == "" {
		uuid = getUUID()
	}

	// Create shared batcher for all connections
	batcher := NewEventBatcher(uuid, apiUrl, verbose)

	address := net.JoinHostPort(host, port)
	listener, err := net.Listen("tcp", address)
	if err != nil {
		fmt.Printf("Error: Could not start server: %v\n", err)
		return
	}
	defer listener.Close()

	fmt.Printf("--- BAR Relay Active ---\n")
	fmt.Printf("Listening on: %s\n", address)
	fmt.Printf("Relaying to : %s\n", apiUrl)
	fmt.Printf("Batching    : Soft timeout=100ms, Hard timeout=250ms\n")
	fmt.Printf("Verbose     : %v\n", verbose)
	fmt.Println("------------------------")

	for {
		conn, err := listener.Accept()
		if err != nil {
			fmt.Printf("Accept error: %v\n", err)
			continue
		}
		
		handler := NewConnectionHandler(conn, batcher, verbose)
		go handler.Handle()
	}
}
