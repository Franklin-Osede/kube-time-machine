// Package health exposes minimal HTTP liveness/readiness endpoints for
// the in-cluster agent. Kubernetes probes hit these directly; no Service
// is required.
package health

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"
)

// Server serves /healthz (liveness), /readyz (readiness), and optionally
// /metrics in Prometheus text format.
type Server struct {
	addr      string
	readyFn   func() bool
	metricsFn func() string // nil = no /metrics endpoint

	mu sync.RWMutex
	ln net.Listener
}

// New constructs a health server bound to addr. Pass an empty addr to
// disable the server (useful for local-first laptop runs).
func New(addr string, readyFn func() bool) *Server {
	return &Server{
		addr:    addr,
		readyFn: readyFn,
	}
}

// WithMetrics enables a /metrics endpoint. fn is called on every request
// and must return a valid Prometheus text-format string. Keeping the
// formatter outside the health package avoids pulling agent types into it.
func (s *Server) WithMetrics(fn func() string) *Server {
	s.metricsFn = fn
	return s
}

// Run listens until ctx is cancelled. It returns listener/server failures
// so the agent fails loudly if Kubernetes probes cannot bind.
func (s *Server) Run(ctx context.Context) error {
	if s.addr == "" {
		<-ctx.Done()
		return ctx.Err()
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/readyz", s.handleReadyz)
	if s.metricsFn != nil {
		mux.HandleFunc("/metrics", s.handleMetrics)
	}

	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("health: listen %s: %w", s.addr, err)
	}
	s.mu.Lock()
	s.ln = ln
	s.mu.Unlock()

	srv := &http.Server{Handler: mux}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("health: listening", "addr", ln.Addr().String())
		errCh <- srv.Serve(ln)
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			slog.Warn("health: shutdown", "err", err)
		}
		err := <-errCh
		if errors.Is(err, http.ErrServerClosed) {
			return ctx.Err()
		}
		return err
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// handleHealthz is the liveness probe: if the process can answer HTTP,
// it is alive.
func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

// handleMetrics serves Prometheus-format metrics. The body is generated
// by the metricsFn provided via WithMetrics — the health package has no
// dependency on the agent or Prometheus client library.
func (s *Server) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = io.WriteString(w, s.metricsFn())
}

// handleReadyz is the readiness probe: the agent is ready once informer
// caches have synced and it can take its first snapshot.
func (s *Server) handleReadyz(w http.ResponseWriter, _ *http.Request) {
	if s.readyFn != nil && s.readyFn() {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
		return
	}
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte("not ready\n"))
}

// Addr returns the configured bind address (empty when disabled).
func (s *Server) Addr() string {
	return s.addr
}

// ListenAddr returns the actual bound address after Run, or nil if the
// server is disabled or has not started listening yet.
func (s *Server) ListenAddr() net.Addr {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.ln == nil {
		return nil
	}
	return s.ln.Addr()
}

// Ready reports whether ch has been closed (non-blocking).
func Ready(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}
