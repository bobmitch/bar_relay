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

func init() {
	flag.StringVar(&host, "host", defaultHost, "The IP address to listen on")
	flag.StringVar(&port, "port", defaultPort, "The TCP port to listen on")
	flag.StringVar(&apiUrl, "url", defaultAPIUrl, "The destination Web API URL")
	flag.StringVar(&uuid, "uuid", "", "Your UUID from the web app")
	flag.BoolVar(&verbose, "v", defaultVerbose, "Enable detailed logging")
	flag.BoolVar(&reset, "reset", false, "Clear the stored UUID and prompt for a new one")
}

func getUUID() string {
	// 1. Try to find the UUID in the home directory
	home, _ := os.UserHomeDir()
	configPath := filepath.Join(home, configFileName)

	data, err := os.ReadFile(configPath)
	if err == nil {
		return strings.TrimSpace(string(data))
	}

	// 2. If not found, ask the user interactively
	fmt.Print("No UUID found. Please paste your UUID: ")
	reader := bufio.NewReader(os.Stdin)
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(input)

	if input == "" {
		fmt.Println("Error: UUID cannot be empty.")
		os.Exit(1)
	}

	// 3. Save it for next time
	os.WriteFile(configPath, []byte(input), 0600) // 0600 = Read/Write for owner only
	fmt.Printf("UUID saved to %s\n", configPath)
	
	return input
}

func main() {
	flag.Parse()

	if reset {
		home, _ := os.UserHomeDir()
		os.Remove(filepath.Join(home, configFileName))
		fmt.Println("Stored UUID cleared.")
	}

	// Logic: If -uuid flag is provided, use it. Otherwise, use stored/interactive.
	if uuid == "" {
		uuid = getUUID()
	}


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
	fmt.Println("------------------------")

	for {
		conn, err := listener.Accept()
		if err != nil {
			fmt.Printf("Accept error: %v\n", err)
			continue
		}
		go handleConnection(conn) // Added 'go' keyword to handle multiple sessions
	}
}

func handleConnection(conn net.Conn) {
	defer conn.Close()
	fmt.Printf("New connection from: %s\n", conn.RemoteAddr())

	// Use a scanner to read line by line (since Lua adds \n)
	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		jsonData := scanner.Text()
		
		if verbose {
			fmt.Printf("Received: %s\n", jsonData)
		}
		
		// Run the relay in a goroutine so the socket isn't blocked 
		// by slow web API responses
		go relayToAPI(jsonData)
		
		// Optional: Send an "OK" back to Lua so it knows we got it
		conn.Write([]byte("ACK\n"))
	}

	if err := scanner.Err(); err != nil {
		fmt.Printf("Connection error: %v\n", err)
	}
	fmt.Printf("Connection closed for: %s\n", conn.RemoteAddr())
}

func relayToAPI(jsonData string) {
	// Parse the incoming JSON
	var payload map[string]interface{}
	err := json.Unmarshal([]byte(jsonData), &payload)
	if err != nil {
		fmt.Printf("JSON Parse Error: %v\n", err)
		return
	}

	// Add the UUID as a property in the JSON
	if uuid != "" {
		payload["uuid"] = uuid
	}

	// Marshal back to JSON
	modifiedJSON, err := json.Marshal(payload)
	if err != nil {
		fmt.Printf("JSON Marshal Error: %v\n", err)
		return
	}

	if verbose {
		fmt.Printf("Sending: %s\n", string(modifiedJSON))
	}

	// Create a new request
	req, err := http.NewRequest("POST", apiUrl, bytes.NewBuffer(modifiedJSON))
	if err != nil {
		fmt.Printf("Request Creation Error: %v\n", err)
		return
	}

	// Set Content-Type header
	req.Header.Set("Content-Type", "application/json")

	resp, err := apiClient.Do(req)
	if err != nil {
		fmt.Printf("API Error: %v\n", err)
		return
	}
	defer resp.Body.Close()

	if verbose || resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		fmt.Printf("API Response (%d): %s\n", resp.StatusCode, string(body))
	}
}
