package lifecycle_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/islamghany/goplate/internal/lifecycle"
)

type recorder struct {
	mu     sync.Mutex
	events []string
}

func (r *recorder) add(e string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *recorder) get() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.events)
}

func component(rec *recorder, name string, startErr error) lifecycle.Component {
	return lifecycle.Func(name,
		func(context.Context) error { rec.add("start " + name); return startErr },
		func(context.Context) error { rec.add("stop " + name); return nil },
	)
}

type faulty struct {
	lifecycle.Component
	faults chan error
}

func (f faulty) Faults() <-chan error { return f.faults }

func newManager() *lifecycle.Manager {
	return lifecycle.New(slog.New(slog.DiscardHandler), lifecycle.Config{ShutdownTimeout: time.Second})
}

func TestStartsInOrderAndStopsInReverse(t *testing.T) {
	rec := &recorder{}
	m := newManager()
	m.Add(component(rec, "db", nil), component(rec, "jobs", nil), component(rec, "http", nil))

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error)
	go func() { done <- m.Run(ctx) }()

	waitFor(t, m.Ready)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("clean shutdown returned %v", err)
	}
	want := []string{"start db", "start jobs", "start http", "stop http", "stop jobs", "stop db"}
	if got := rec.get(); !slices.Equal(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
}

func TestStartFailureStopsWhatAlreadyStarted(t *testing.T) {
	rec := &recorder{}
	m := newManager()
	m.Add(component(rec, "db", nil), component(rec, "http", errors.New("port in use")), component(rec, "never", nil))

	err := m.Run(t.Context())
	if err == nil {
		t.Fatal("expected start error")
	}
	want := []string{"start db", "start http", "stop db"}
	if got := rec.get(); !slices.Equal(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
}

func TestFaultTriggersShutdown(t *testing.T) {
	rec := &recorder{}
	faults := make(chan error, 1)
	m := newManager()
	m.Add(component(rec, "db", nil), faulty{component(rec, "http", nil), faults})

	done := make(chan error)
	go func() { done <- m.Run(t.Context()) }()
	waitFor(t, m.Ready)

	faults <- errors.New("serve: listener closed")
	if err := <-done; err == nil {
		t.Fatal("fault must be returned")
	}
	if got := rec.get(); got[len(got)-1] != "stop db" {
		t.Fatalf("everything must be stopped, events = %v", got)
	}
}

func TestReadiness(t *testing.T) {
	m := newManager()
	healthy := true
	m.AddChecker(lifecycle.CheckFunc("db", func(context.Context) error {
		if healthy {
			return nil
		}
		return errors.New("down")
	}))

	probe := func() int {
		rec := httptest.NewRecorder()
		m.ReadyHandler(true).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		return rec.Code
	}

	if probe() != http.StatusServiceUnavailable {
		t.Fatal("not ready before Run")
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { _ = m.Run(ctx) }()
	waitFor(t, m.Ready)

	if probe() != http.StatusOK {
		t.Fatal("ready after start with healthy checks")
	}

	healthy = false
	time.Sleep(1100 * time.Millisecond) // results are cached for 1s
	if probe() != http.StatusServiceUnavailable {
		t.Fatal("failing check must make readiness fail")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
