package web

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/meta-programming/bazelmop/gen/bazelmop/v1/bazelmopv1connect"
	"github.com/meta-programming/bazelmop/pkg/bazelcas"
	"github.com/meta-programming/bazelmop/pkg/dedupe"
	"github.com/meta-programming/bazelmop/pkg/runlog"
)

// Go embed directives to bundle frontend files into the binary.
//
//go:embed assets/index.html assets/marked.min.js
var assetsFS embed.FS

// Server serves the web dashboard and the daemon's API.
//
// The API is [bazelmopv1connect.BazelmopServiceHandler], served with Connect
// so that browsers can call it as JSON over HTTP/1.1 and Go clients can use
// gRPC. The older /api/* JSON and server-sent event routes are kept for the
// current dashboard page.
type Server struct {
	host string
	port string

	mu                 sync.RWMutex
	reportMarkdown     string
	instances          []*bazelcas.Instance
	instancesUpdatedAt time.Time
	updatedAt          time.Time
	nextScanAt         time.Time
	status             string
	progress           dedupe.Progress
	runStart           time.Time
	phaseStart         time.Time

	// Set before Start; read without the lock.
	root   string      // cache root, for disk usage
	runLog *runlog.Log // run history; nil if not kept

	// watchers are signalled, without blocking, whenever state changes.
	// Each reads the state it needs itself.
	watchersMu sync.Mutex
	watchers   map[chan struct{}]struct{}
}

// NewServer initializes a new Web Server instance.
func NewServer(host, port string) *Server {
	return &Server{
		host:     host,
		port:     port,
		watchers: make(map[chan struct{}]struct{}),
	}
}

// UpdateReport updates the report content and schedules the next scan time,
// then notifies watchers.
func (s *Server) UpdateReport(markdown string, nextScan time.Time) {
	s.mu.Lock()
	s.reportMarkdown = markdown
	s.updatedAt = time.Now()
	s.nextScanAt = nextScan
	s.status = "Idle"
	s.mu.Unlock()
	s.notify()
}

// UpdateInstances replaces the list of output bases and repository caches
// and notifies watchers.
func (s *Server) UpdateInstances(instances []*bazelcas.Instance) {
	s.mu.Lock()
	s.instances = instances
	s.instancesUpdatedAt = time.Now()
	s.mu.Unlock()
	s.notify()
}

// UpdateNextScan updates the next scan time and notifies watchers.
func (s *Server) UpdateNextScan(nextScan time.Time) {
	s.mu.Lock()
	s.nextScanAt = nextScan
	s.mu.Unlock()
	s.notify()
}

// SetRoot sets the cache root whose filesystem's usage is reported. Call it
// before Start.
func (s *Server) SetRoot(root string) {
	s.root = root
}

// SetRunLog sets where ListRuns reads run history. Call it before Start.
func (s *Server) SetRunLog(l *runlog.Log) {
	s.runLog = l
}

// UpdateStatus updates the server's status and notifies watchers.
func (s *Server) UpdateStatus(status string) {
	s.mu.Lock()
	s.status = status
	s.mu.Unlock()
	s.notify()
}

// UpdateProgress records how far the current run has got and notifies
// watchers. A progress in a non-idle phase after an idle one starts a run.
func (s *Server) UpdateProgress(p dedupe.Progress) {
	now := time.Now()
	s.mu.Lock()
	if p.Phase != s.progress.Phase {
		s.phaseStart = now
		if s.progress.Phase == dedupe.PhaseIdle {
			s.runStart = now
		}
	}
	if p.Phase == dedupe.PhaseIdle {
		s.runStart, s.phaseStart = time.Time{}, time.Time{}
	}
	s.progress = p
	s.status = p.Message
	s.mu.Unlock()
	s.notify()
}

// watch registers a watcher. The returned channel receives a value after
// each state change; changes that arrive while one is pending are merged.
// Call the returned function to unregister.
func (s *Server) watch() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	s.watchersMu.Lock()
	s.watchers[ch] = struct{}{}
	s.watchersMu.Unlock()
	return ch, func() {
		s.watchersMu.Lock()
		delete(s.watchers, ch)
		s.watchersMu.Unlock()
	}
}

func (s *Server) notify() {
	s.watchersMu.Lock()
	defer s.watchersMu.Unlock()
	for ch := range s.watchers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// deleteOutputBase removes one output base from disk and from the list.
func (s *Server) deleteOutputBase(id string) error {
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
		return errNotFound
	}
	if target.Path != "" {
		if err := bazelcas.RemoveAll(target.Path); err != nil {
			s.mu.Unlock()
			return fmt.Errorf("removing %s: %w", target.Path, err)
		}
	}
	s.instances = remaining
	s.instancesUpdatedAt = time.Now()
	s.mu.Unlock()
	s.notify()
	return nil
}

var errNotFound = fmt.Errorf("output base not found")

// legacyPayload is the JSON body of /api/report and each /api/events event.
func (s *Server) legacyPayload() map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()
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
	return payload
}

// Handler returns the HTTP handler for the dashboard, the Connect API and
// the older /api/* routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	apiPath, apiHandler := bazelmopv1connect.NewBazelmopServiceHandler(&apiServer{s: s})
	mux.Handle(apiPath, apiHandler)

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
		_, _ = w.Write(data)
	})

	mux.HandleFunc("/assets/marked.min.js", func(w http.ResponseWriter, r *http.Request) {
		data, err := assetsFS.ReadFile("assets/marked.min.js")
		if err != nil {
			http.Error(w, "Failed to read marked.min.js asset", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/javascript")
		_, _ = w.Write(data)
	})

	mux.HandleFunc("/api/report", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.legacyPayload())
	})

	mux.HandleFunc("/api/instances", func(w http.ResponseWriter, r *http.Request) {
		s.mu.RLock()
		insts := s.instances
		s.mu.RUnlock()
		if insts == nil {
			insts = []*bazelcas.Instance{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(insts)
	})

	outputBasesHandler := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete && r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/api/output-bases")
		id = strings.TrimPrefix(id, "/")
		if id == "" || id == "delete" {
			id = r.URL.Query().Get("id")
		}
		if id == "" {
			http.Error(w, "Missing output base id", http.StatusBadRequest)
			return
		}
		if err := s.deleteOutputBase(id); err == errNotFound {
			http.Error(w, "Output base not found", http.StatusNotFound)
			return
		} else if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"id":      id,
			"message": fmt.Sprintf("Deleted output base %s", id),
		})
	}
	mux.HandleFunc("/api/output-bases", outputBasesHandler)
	mux.HandleFunc("/api/output-bases/", outputBasesHandler)

	mux.HandleFunc("/api/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		changed, stop := s.watch()
		defer stop()
		for {
			data, err := json.Marshal(s.legacyPayload())
			if err != nil {
				log.Printf("Failed to marshal event data: %v", err)
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				return
			case <-changed:
			}
		}
	})

	return localOnlyForMutations(mux)
}

// mutatingPaths are the routes that change the disk. They are refused
// unless the request comes from this machine and names it as the host.
var mutatingPaths = []string{
	bazelmopv1connect.BazelmopServiceDeleteOutputBasesProcedure,
	"/api/output-bases",
}

// localOnlyForMutations refuses mutating requests that do not come from a
// loopback address, or whose Host header is not a loopback name. The Host
// check stops a web page from reaching the API through DNS rebinding.
func localOnlyForMutations(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, p := range mutatingPaths {
			if strings.HasPrefix(r.URL.Path, p) && !isLocalRequest(r) {
				http.Error(w, "Only callers on this machine may change the cache", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func isLocalRequest(r *http.Request) bool {
	remote, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	if ip := net.ParseIP(remote); ip == nil || !ip.IsLoopback() {
		return false
	}
	host := r.Host
	if h, _, err := net.SplitHostPort(r.Host); err == nil {
		host = h
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// Start serves until ctx is done. It accepts HTTP/1.1 and unencrypted
// HTTP/2, which gRPC clients need.
func (s *Server) Start(ctx context.Context) error {
	addr := net.JoinHostPort(s.host, s.port)
	srv := &http.Server{
		Addr:      addr,
		Handler:   s.Handler(),
		Protocols: new(http.Protocols),
	}
	srv.Protocols.SetHTTP1(true)
	srv.Protocols.SetUnencryptedHTTP2(true)

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
