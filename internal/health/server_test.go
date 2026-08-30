package health_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/Franklin-Osede/kube-time-machine/internal/health"
)

func TestHealthzAlwaysOK(t *testing.T) {
	ready := make(chan struct{})
	srv := health.New("127.0.0.1:0", func() bool { return health.Ready(ready) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := runServer(ctx, srv)
	defer assertStopsOnCancel(t, cancel, errCh)

	base := waitForListen(t, srv)

	resp, err := http.Get(base + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: want 200, got %d", resp.StatusCode)
	}
}

func TestReadyzWaitsForReady(t *testing.T) {
	ready := make(chan struct{})
	srv := health.New("127.0.0.1:0", func() bool { return health.Ready(ready) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := runServer(ctx, srv)
	defer assertStopsOnCancel(t, cancel, errCh)

	base := waitForListen(t, srv)

	resp, err := http.Get(base + "/readyz")
	if err != nil {
		t.Fatalf("GET /readyz (not ready): %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status before sync: want 503, got %d", resp.StatusCode)
	}

	close(ready)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err = http.Get(base + "/readyz")
		if err != nil {
			t.Fatalf("GET /readyz (ready): %v", err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			if string(b) != "ok\n" {
				t.Fatalf("body: want ok\\n, got %q", b)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("readyz never returned 200 after ready channel closed")
}

func TestDisabledWhenAddrEmpty(t *testing.T) {
	srv := health.New("", func() bool { return true })
	ctx, cancel := context.WithCancel(context.Background())
	errCh := runServer(ctx, srv)
	if addr := srv.ListenAddr(); addr != nil {
		t.Fatalf("disabled server should not bind, got %s", addr)
	}
	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run after cancel: want context.Canceled, got %v", err)
	}
}

func runServer(ctx context.Context, srv *health.Server) <-chan error {
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Run(ctx)
	}()
	return errCh
}

func assertStopsOnCancel(t *testing.T, cancel context.CancelFunc, errCh <-chan error) {
	t.Helper()
	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run after cancel: want context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("health server did not stop after cancellation")
	}
}

func waitForListen(t *testing.T, srv *health.Server) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if addr := srv.ListenAddr(); addr != nil {
			return "http://" + addr.String()
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("health server did not start listening")
	return ""
}
