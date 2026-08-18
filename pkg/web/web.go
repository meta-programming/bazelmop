package web

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/meta-programming/bazelmop/pkg/bazelcas"
)

// Go embed directives to bundle frontend files into the binary.
//go:embed assets/index.html assets/marked.min.js
var assetsFS embed.FS

// Server handles serving the web-based report viewer dashboard and output bases API.
type Server struct {
	host string
	port string

	mu             sync.RWMutex
	reportMarkdown string
	instances      []*bazelcas.Instance
	updatedAt      time.Time
	nextScanAt     time.Time
	status         string

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
	s.status = "Idle"
	s.mu.Unlock()

	s.broadcast()
}

// UpdateInstances updates the active list of output base and repo cache instances and broadcasts it.
func (s *Server) UpdateInstances(instances []*bazelcas.Instance) {
	s.mu.Lock()
	s.instances = instances
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

// UpdateStatus updates the server's status and broadcasts it to clients.
func (s *Server) UpdateStatus(status string) {
	s.mu.Lock()
	s.status = status
	s.mu.Unlock()

	s.broadcast()
}

// broadcast sends the current state payload to all connected SSE client channels.
func (s *Server) broadcast() {
	s.mu.RLock()
	st := s.status
	if st == "" {
		st = "Idle"
	}

	payload := map[string]interface{}{
		"report":     s.reportMarkdown,
		"instances":  s.instances,
		"updated_at": "",
		"next_scan":  "",
		"status":     st,
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
		insts := s.instances
		updated := s.updatedAt
		nextScan := s.nextScanAt
		st := s.status
		s.mu.RUnlock()

		if st == "" {
			st = "Idle"
		}

		payload := map[string]interface{}{
			"report":     report,
			"instances":  insts,
			"updated_at": "",
			"next_scan":  "",
			"status":     st,
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

	// 4. API route: Serve instances list as JSON
	mux.HandleFunc("/api/instances", func(w http.ResponseWriter, r *http.Request) {
		s.mu.RLock()
		insts := s.instances
		s.mu.RUnlock()

		if insts == nil {
			insts = []*bazelcas.Instance{}
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(insts)
	})

	// 5. API route: Delete output base by ID
	outputBasesHandler := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete && r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		id := strings.TrimPrefix(r.URL.Path, "/api/output-bases/")
		id = strings.TrimPrefix(id, "/api/output-bases")
		id = strings.TrimPrefix(id, "/")
		if id == "" || id == "delete" {
			id = r.URL.Query().Get("id")
		}
		if id == "" {
			http.Error(w, "Missing output base id", http.StatusBadRequest)
			return
		}

		s.mu.Lock()
		var target *bazelcas.Instance
		var remaining []*bazelcas.Instance
		for _, inst := range s.instances {
			if inst.ID == id {
				target = inst
			} else {
				remaining = append(remaining, inst)
			}
		}

		if target == nil {
			s.mu.Unlock()
			http.Error(w, "Output base not found", http.StatusNotFound)
			return
		}

		if target.Path != "" {
			_ = os.RemoveAll(target.Path)
		}

		s.instances = remaining
		s.mu.Unlock()

		s.broadcast()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"id":      id,
			"message": fmt.Sprintf("Successfully deleted output base %s", id),
		})
	}

	mux.HandleFunc("/api/output-bases", outputBasesHandler)
	mux.HandleFunc("/api/output-bases/", outputBasesHandler)

	// 6. SSE route: Stream real-time updates to client
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

		// Send initial payload immediately upon connection
		s.mu.RLock()
		st := s.status
		if st == "" {
			st = "Idle"
		}

		initPayload := map[string]interface{}{
			"report":     s.reportMarkdown,
			"instances":  s.instances,
			"updated_at": "",
			"next_scan":  "",
			"status":     st,
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
