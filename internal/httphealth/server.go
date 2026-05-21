package httphealth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"sync/atomic"
	"time"

	"github.com/vitalvas/systemd-supervisord/internal/config"
	"github.com/vitalvas/systemd-supervisord/internal/statemanager"
	"github.com/vitalvas/systemd-supervisord/internal/systemd"
)

type StatusProvider interface {
	GetStatus(unit string) *statemanager.UnitStatus
}

type CriticalProvider interface {
	CriticalUnits() []string
}

type Server struct {
	cfg      config.HTTPConfig
	status   StatusProvider
	critical CriticalProvider
	ready    atomic.Bool
	httpSrv  *http.Server
	listener net.Listener
}

type unitView struct {
	UnitName    string `json:"unit_name"`
	ActiveState string `json:"active_state,omitempty"`
	SubState    string `json:"sub_state,omitempty"`
	Healthy     *bool  `json:"healthy,omitempty"`
}

type healthResponse struct {
	Status    string     `json:"status"`
	Ready     bool       `json:"ready"`
	Timestamp time.Time  `json:"timestamp"`
	Units     []unitView `json:"units,omitempty"`
}

func New(cfg config.HTTPConfig, status StatusProvider, critical CriticalProvider) *Server {
	return &Server{
		cfg:      cfg,
		status:   status,
		critical: critical,
	}
}

func (s *Server) MarkReady() {
	s.ready.Store(true)
}

func (s *Server) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/ready", s.handleReady)
	mux.HandleFunc("/live", s.handleLive)

	s.httpSrv = &http.Server{
		Handler:      mux,
		ReadTimeout:  s.cfg.ReadTimeout,
		WriteTimeout: s.cfg.WriteTimeout,
	}

	ln, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", s.cfg.Listen, err)
	}

	s.listener = ln

	go func() {
		<-ctx.Done()
		s.shutdown()
	}()

	go func() {
		slog.Info("http health server started", "listen", s.cfg.Listen)

		if err := s.httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("http health server stopped", "error", err)
		}
	}()

	return nil
}

func (s *Server) shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.ShutdownTimeout)
	defer cancel()

	if err := s.httpSrv.Shutdown(ctx); err != nil {
		slog.Error("http health server shutdown", "error", err)
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if !allowedMethod(w, r) {
		return
	}

	units := s.criticalView()
	healthy := allHealthy(units)

	resp := healthResponse{
		Status:    statusString(healthy),
		Ready:     s.ready.Load(),
		Timestamp: time.Now(),
		Units:     units,
	}

	status := http.StatusOK
	if !healthy {
		status = http.StatusServiceUnavailable
	}

	writeJSON(w, status, resp)
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if !allowedMethod(w, r) {
		return
	}

	ready := s.ready.Load()

	resp := healthResponse{
		Status:    statusString(ready),
		Ready:     ready,
		Timestamp: time.Now(),
	}

	status := http.StatusOK
	if !ready {
		status = http.StatusServiceUnavailable
	}

	writeJSON(w, status, resp)
}

func (s *Server) handleLive(w http.ResponseWriter, r *http.Request) {
	if !allowedMethod(w, r) {
		return
	}

	resp := healthResponse{
		Status:    "ok",
		Ready:     s.ready.Load(),
		Timestamp: time.Now(),
	}

	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) criticalView() []unitView {
	names := s.critical.CriticalUnits()
	sort.Strings(names)

	views := make([]unitView, 0, len(names))

	for _, name := range names {
		status := s.status.GetStatus(name)
		if status == nil {
			views = append(views, unitView{UnitName: name})

			continue
		}

		views = append(views, unitView{
			UnitName:    name,
			ActiveState: status.ActiveState,
			SubState:    status.SubState,
			Healthy:     status.Healthy,
		})
	}

	return views
}

func allHealthy(units []unitView) bool {
	for _, u := range units {
		if u.ActiveState != systemd.ActiveStateActive {
			return false
		}

		if u.Healthy != nil && !*u.Healthy {
			return false
		}
	}

	return true
}

func statusString(ok bool) string {
	if ok {
		return "ok"
	}

	return "unhealthy"
}

func allowedMethod(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return true
	}

	w.Header().Set("Allow", "GET, HEAD")
	w.WriteHeader(http.StatusMethodNotAllowed)

	return false
}

func writeJSON(w http.ResponseWriter, status int, body interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Error("encoding health response", "error", err)
	}
}
