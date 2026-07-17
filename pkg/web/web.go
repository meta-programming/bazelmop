package web

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"time"
)

// Go embed directives to bundle frontend files into the binary.
//go:embed assets/index.html assets/marked.min.js
var assetsFS embed.FS

// Server handles serving the web-based report viewer dashboard.
type Server struct {
	host string
	port string

	mu             sync.RWMutex
	reportMarkdown string
	updatedAt      time.Time
	nextScanAt     time.Time

	clientsMu sync.Mutex
	clients   map[chan string]bool
}

// NewServer initializes a new Web Server instance.
func NewServer(host, port string) *Server {
	return &Server{
		host:    host,
		port:    port,
		clients: make(map[chan string]bool),
	}
}

// UpdateReport updates the report content and schedules the next scan time,
// then broadcasts the update to all connected SSE clients.
func (s *Server) UpdateReport(markdown string, nextScan time.Time) {
	s.mu.Lock()
	s.reportMarkdown = markdown
	s.updatedAt = time.Now()
	s.nextScanAt = nextScan
	s.mu.Unlock()

	s.broadcast()
}

// UpdateNextScan updates the next scan time and broadcasts to clients.
func (s *Server) UpdateNextScan(nextScan time.Time) {
	s.mu.Lock()
	s.nextScanAt = nextScan
	s.mu.Unlock()

	s.broadcast()
}

// broadcast sends the current state payload to all connected SSE client channels.
func (s *Server) broadcast() {
	s.mu.RLock()
	payload := map[string]interface{}{
		"report":     s.reportMarkdown,
		"updated_at": "",
		"next_scan":  "",
	}
	if !s.updatedAt.IsZero() {
		payload["updated_at"] = s.updatedAt.Format(time.RFC3339)
	}
	if !s.nextScanAt.IsZero() {
		payload["next_scan"] = s.nextScanAt.Format(time.RFC3339)
	}
	s.mu.RUnlock()

	data, err := json.Marshal(payload)
	if err != nil {
		log.Printf("Failed to marshal event broadcast data: %v", err)
		return
	}

	s.clientsMu.Lock()
	defer s.clientsMu.Unlock()
	for ch := range s.clients {
		select {
		case ch <- string(data):
		default:
			// Client queue is full, skip
		}
	}
}

// Start spawns the HTTP listener in the background, closing when context is done.
func (s *Server) Start(ctx context.Context) error {
	mux := http.NewServeMux()

	// 1. Root route: Serve HTML dashboard
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		data, err := assetsFS.ReadFile("assets/index.html")
		if err != nil {
			http.Error(w, "Failed to read index.html asset", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	})

	// 2. Asset route: Serve marked.min.js
	mux.HandleFunc("/assets/marked.min.js", func(w http.ResponseWriter, r *http.Request) {
		data, err := assetsFS.ReadFile("assets/marked.min.js")
		if err != nil {
			http.Error(w, "Failed to read marked.min.js asset", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/javascript")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	})

	// 3. API route: Serve latest report (for fallback/direct querying)
	mux.HandleFunc("/api/report", func(w http.ResponseWriter, r *http.Request) {
		s.mu.RLock()
		report := s.reportMarkdown
		updated := s.updatedAt
		nextScan := s.nextScanAt
		s.mu.RUnlock()

		payload := map[string]interface{}{
			"report":     report,
			"updated_at": "",
			"next_scan":  "",
		}
		if !updated.IsZero() {
			payload["updated_at"] = updated.Format(time.RFC3339)
		}
		if !nextScan.IsZero() {
			payload["next_scan"] = nextScan.Format(time.RFC3339)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(payload)
	})

	// 4. SSE route: Stream real-time updates to client
	mux.HandleFunc("/api/events", func(w http.ResponseWriter, r *http.Request) {
		// Set headers required for Server-Sent Events
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("Access-Control-Allow-Origin", "*")

		// Create a channel for this client connection
		ch := make(chan string, 10)
		s.clientsMu.Lock()
		s.clients[ch] = true
		s.clientsMu.Unlock()

		// Ensure channel cleanup on client disconnect
		defer func() {
			s.clientsMu.Lock()
			delete(s.clients, ch)
			s.clientsMu.Unlock()
			close(ch)
		}()

		// Send the initial report immediately upon connection
		s.mu.RLock()
		initPayload := map[string]interface{}{
			"report":     s.reportMarkdown,
			"updated_at": "",
			"next_scan":  "",
		}
		if !s.updatedAt.IsZero() {
			initPayload["updated_at"] = s.updatedAt.Format(time.RFC3339)
		}
		if !s.nextScanAt.IsZero() {
			initPayload["next_scan"] = s.nextScanAt.Format(time.RFC3339)
		}
		s.mu.RUnlock()

		initData, err := json.Marshal(initPayload)
		if err == nil {
			fmt.Fprintf(w, "data: %s\n\n", string(initData))
			w.(http.Flusher).Flush()
		}

		// Keep connection open and push event stream updates
		for {
			select {
			case <-r.Context().Done():
				return
			case msg := <-ch:
				fmt.Fprintf(w, "data: %s\n\n", msg)
				w.(http.Flusher).Flush()
			}
		}
	})

	addr := net.JoinHostPort(s.host, s.port)
	srv := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	errChan := make(chan error, 1)

	go func() {
		log.Printf("Starting web server on http://%s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errChan <- err
		}
		close(errChan)
	}()

	select {
	case <-ctx.Done():
		log.Println("Shutting down web server gracefully...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errChan:
		return fmt.Errorf("web server failed to start: %w", err)
	}
}
