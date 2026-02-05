package main

import (
	"bufio"
	"bytes"
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
	token   string // <--- New variable for the JWT
	verbose bool
	reset   bool
)

var apiClient = &http.Client{
	Timeout: 10 * time.Second,
}

const configFileName = ".bar_token"

func init() {
	flag.StringVar(&host, "host", defaultHost, "The IP address to listen on")
	flag.StringVar(&port, "port", defaultPort, "The TCP port to listen on")
	flag.StringVar(&apiUrl, "url", defaultAPIUrl, "The destination Web API URL")
	flag.StringVar(&token, "token", "", "Your Personal Access Token from the web app") // <--- New Flag
	flag.BoolVar(&verbose, "v", defaultVerbose, "Enable detailed logging")
	flag.BoolVar(&reset, "reset", false, "Clear the stored token and prompt for a new one") // <--- Moved here
}

func getToken() string {
	// 1. Try to find the token in the home directory
	home, _ := os.UserHomeDir()
	configPath := filepath.Join(home, configFileName)

	data, err := os.ReadFile(configPath)
	if err == nil {
		return strings.TrimSpace(string(data))
	}

	// 2. If not found, ask the user interactively
	fmt.Print("No token found. Please paste your Personal Access Token: ")
	reader := bufio.NewReader(os.Stdin)
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(input)

	if input == "" {
		fmt.Println("Error: Token cannot be empty.")
		os.Exit(1)
	}

	// 3. Save it for next time
	os.WriteFile(configPath, []byte(input), 0600) // 0600 = Read/Write for owner only
	fmt.Printf("Token saved to %s\n", configPath)
	
	return input
}

func main() {
	flag.Parse()

	if reset {
		home, _ := os.UserHomeDir()
		os.Remove(filepath.Join(home, configFileName))
		fmt.Println("Stored token cleared.")
	}

	// Logic: If -token flag is provided, use it. Otherwise, use stored/interactive.
	if token == "" {
		token = getToken()
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
	
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		fmt.Printf("Read error: %v\n", err)
		return
	}
	
	jsonData := string(buf[:n])
	if verbose {
		fmt.Printf("Received: %s\n", jsonData)
	}
	
	relayToAPI(jsonData)
}

func relayToAPI(jsonData string) {
	// Create a new request manually to add headers
	req, err := http.NewRequest("POST", apiUrl, bytes.NewBufferString(jsonData))
	if err != nil {
		fmt.Printf("Request Creation Error: %v\n", err)
		return
	}

	// Set Headers
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

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
