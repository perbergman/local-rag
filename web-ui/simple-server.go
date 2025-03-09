package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	// Parse command line flags
	port := flag.Int("port", 8000, "Port to listen on")
	mainBinary := flag.String("main", "../main", "Path to the main binary")
	flag.Parse()

	// Print current working directory for debugging
	cwd, err := os.Getwd()
	if err != nil {
		log.Printf("Error getting current working directory: %v", err)
	} else {
		log.Printf("Current working directory: %s", cwd)
	}

	// Resolve absolute paths
	absMainBinary, err := filepath.Abs(*mainBinary)
	if err != nil {
		log.Fatalf("Error resolving main binary path: %v", err)
	}

	// Check if main binary exists
	if _, err := os.Stat(absMainBinary); os.IsNotExist(err) {
		log.Fatalf("Main binary not found at %s", absMainBinary)
	}

	// Create server
	logger := log.New(os.Stdout, "SIMPLE-SERVER: ", log.LstdFlags)
	server := &SimpleServer{
		mainBinary: absMainBinary,
		logger:     logger,
	}

	// Set up routes
	http.HandleFunc("/", server.handleRoot)
	http.HandleFunc("/api/test-search", server.handleTestSearch)
	http.HandleFunc("/api/llm-query", server.handleLLMQuery)

	// Start server
	addr := fmt.Sprintf(":%d", *port)
	logger.Printf("Starting server on %s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

// SimpleServer handles HTTP requests
type SimpleServer struct {
	mainBinary string
	logger     *log.Logger
}

// handleRoot serves the simple.html file
func (s *SimpleServer) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.ServeFile(w, r, filepath.Join(".", r.URL.Path))
		return
	}
	http.ServeFile(w, r, "simple.html")
}

// handleTestSearch executes the main binary with search arguments
func (s *SimpleServer) handleTestSearch(w http.ResponseWriter, r *http.Request) {
	// Set CORS headers
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	// Handle OPTIONS request
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Get query parameters
	query := r.URL.Query().Get("query")
	if query == "" {
		http.Error(w, "Missing query parameter", http.StatusBadRequest)
		return
	}

	language := r.URL.Query().Get("language")
	minScore := r.URL.Query().Get("min_score")
	if minScore == "" {
		minScore = "0.1"
	}

	// Build command arguments with properly quoted query string
	args := []string{"--query", "--query-string", fmt.Sprintf("%q", query)}

	// Add language filter if provided
	if language != "" {
		args = append(args, "--languages", language)
	}

	// Add min score
	args = append(args, "--min-score", minScore)

	// Log the command
	s.logger.Printf("Executing command: go run %s %s", filepath.Base(s.mainBinary), strings.Join(args, " "))

	// Create command - use 'go run' instead of direct execution
	mainDir := filepath.Dir(s.mainBinary)
	mainFile := filepath.Base(s.mainBinary)
	allArgs := append([]string{"run", mainFile}, args...)
	cmd := exec.Command("go", allArgs...)
	cmd.Dir = mainDir
	cmd.Env = os.Environ()

	// Execute command
	output, err := cmd.CombinedOutput()
	if err != nil {
		s.logger.Printf("Error executing search: %v, Output: %s", err, string(output))
		http.Error(w, fmt.Sprintf("Error executing search: %v\nOutput: %s", err, string(output)), http.StatusInternalServerError)
		return
	}

	// Return output
	w.Header().Set("Content-Type", "text/plain")
	w.Write(output)
}

// handleLLMQuery executes the main binary with LLM query arguments
func (s *SimpleServer) handleLLMQuery(w http.ResponseWriter, r *http.Request) {
	// Set CORS headers
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	// Handle OPTIONS request
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Get query parameters
	query := r.URL.Query().Get("query")
	if query == "" {
		http.Error(w, "Missing query parameter", http.StatusBadRequest)
		return
	}

	language := r.URL.Query().Get("language")
	minScore := r.URL.Query().Get("min_score")
	if minScore == "" {
		minScore = "0.1"
	}

	// Set a longer timeout for LLM queries to accommodate LMStudio's single-threaded processing
	timeoutDuration := 3 * time.Minute
	
	// Build command arguments with properly quoted query string
	args := []string{"--query", "--llm-response", "--query-string", fmt.Sprintf("%q", query)}

	// Add language filter if provided
	if language != "" {
		args = append(args, "--languages", language)
	}

	// Add min score
	args = append(args, "--min-score", minScore)

	// Log the command
	s.logger.Printf("Executing LLM query command: %s %s", s.mainBinary, strings.Join(args, " "))

	// Create command with timeout - execute the binary directly
	cmd := exec.Command(s.mainBinary, args...)
	cmd.Dir = filepath.Dir(s.mainBinary)
	cmd.Env = os.Environ()

	// Execute command with timeout
	outputChan := make(chan struct {
		output []byte
		err    error
	})

	go func() {
		output, err := cmd.CombinedOutput()
		outputChan <- struct {
			output []byte
			err    error
		}{output, err}
	}()

	// Wait for command to complete or timeout
	select {
	case result := <-outputChan:
		if result.err != nil {
			s.logger.Printf("Error executing LLM query: %v, Output: %s", result.err, string(result.output))
			http.Error(w, fmt.Sprintf("Error executing LLM query: %v\nOutput: %s", result.err, string(result.output)), http.StatusInternalServerError)
			return
		}
		// Return output
		w.Header().Set("Content-Type", "text/plain")
		w.Write(result.output)
	case <-time.After(timeoutDuration):
		// Kill the process if it times out
		cmd.Process.Kill()
		s.logger.Printf("LLM query timed out after %v", timeoutDuration)
		http.Error(w, fmt.Sprintf("LLM query timed out after %v", timeoutDuration), http.StatusGatewayTimeout)
	}
}
