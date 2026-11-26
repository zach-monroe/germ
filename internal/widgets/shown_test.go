package widgets

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeWidget counts fetches and draws.
type fakeWidget struct {
	updates, draws atomic.Int32
	every, tick    time.Duration
}

func (f *fakeWidget) Update(context.Context) error { f.updates.Add(1); return nil }
func (f *fakeWidget) Every() time.Duration         { return f.every }
func (f *fakeWidget) Tick() time.Duration          { return f.tick }
func (f *fakeWidget) Draw(w, h int, err error) []string {
	f.draws.Add(1)
	return []string{"x"}
}

type syncBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(2 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestHiddenWidgetIsDormant(t *testing.T) {
	f := &fakeWidget{every: 20 * time.Millisecond, tick: 20 * time.Millisecond}
	builtins["test-fake"] = func(Options) Widget { return f }
	defer delete(builtins, "test-fake")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	shown := NewShown(false)
	size := func() (int, int) { return 10, 2 }
	go RunInProcess(ctx, "test-fake", nil, &syncBuf{}, size, make(chan struct{}), shown)

	time.Sleep(150 * time.Millisecond) // many periods
	if u, d := f.updates.Load(), f.draws.Load(); u != 0 || d != 0 {
		t.Fatalf("hidden widget: %d updates, %d draws; want none", u, d)
	}

	shown.Set(true)
	eventually(t, "a fetch and a draw once shown", func() bool { return f.updates.Load() >= 1 && f.draws.Load() >= 1 })
	eventually(t, "ticks and refreshes while shown", func() bool { return f.updates.Load() >= 3 && f.draws.Load() >= 3 })

	shown.Set(false)
	time.Sleep(30 * time.Millisecond) // let anything in flight land
	u, d := f.updates.Load(), f.draws.Load()
	time.Sleep(150 * time.Millisecond)
	if u2, d2 := f.updates.Load(), f.draws.Load(); u2 != u || d2 != d {
		t.Fatalf("hidden again: updates %d -> %d, draws %d -> %d; want no change", u, u2, d, d2)
	}

	shown.Set(true)
	eventually(t, "a redraw when shown again", func() bool { return f.draws.Load() > d })
}

func TestShownAgainSkipsFreshFetch(t *testing.T) {
	f := &fakeWidget{every: time.Hour}
	builtins["test-fake"] = func(Options) Widget { return f }
	defer delete(builtins, "test-fake")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	shown := NewShown(true)
	go RunInProcess(ctx, "test-fake", nil, &syncBuf{}, func() (int, int) { return 10, 2 }, make(chan struct{}), shown)
	eventually(t, "the first fetch", func() bool { return f.updates.Load() == 1 })

	shown.Set(false)
	shown.Set(true) // data is minutes fresh, not an hour: no refetch
	d := f.draws.Load()
	eventually(t, "a redraw", func() bool { return f.draws.Load() > d })
	time.Sleep(50 * time.Millisecond)
	if u := f.updates.Load(); u != 1 {
		t.Fatalf("updates = %d, want 1 (data still fresh)", u)
	}
}
