package awsx

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCacheServesTheSecondCallFromMemory(t *testing.T) {
	var calls int32
	c := &Cache{TTL: time.Minute}
	load := func(context.Context) (any, error) {
		atomic.AddInt32(&calls, 1)
		return "value", nil
	}

	for i := 0; i < 5; i++ {
		got, err := c.Do(context.Background(), "k", load)
		if err != nil {
			t.Fatal(err)
		}
		if got != "value" {
			t.Fatalf("got %v", got)
		}
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("loader ran %d times, want 1", n)
	}
}

// Releasing the lock before the loader runs is what let every concurrent miss
// issue its own AWS call — a stampede that lands precisely when the dashboard
// is busiest.
func TestCacheCollapsesConcurrentMisses(t *testing.T) {
	var calls int32
	release := make(chan struct{})
	c := &Cache{TTL: time.Minute}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = c.Do(context.Background(), "k", func(context.Context) (any, error) {
				atomic.AddInt32(&calls, 1)
				<-release
				return "v", nil
			})
		}()
	}

	// Give every goroutine a chance to arrive at the miss before the loader
	// is allowed to finish.
	time.Sleep(30 * time.Millisecond)
	close(release)
	wg.Wait()

	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("loader ran %d times for 50 concurrent misses, want 1", n)
	}
}

func TestCacheExpiresValues(t *testing.T) {
	now := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	var calls int32
	c := &Cache{TTL: 30 * time.Second, Now: func() time.Time { return now }}
	load := func(context.Context) (any, error) {
		atomic.AddInt32(&calls, 1)
		return "v", nil
	}

	if _, err := c.Do(context.Background(), "k", load); err != nil {
		t.Fatal(err)
	}
	now = now.Add(29 * time.Second)
	if _, err := c.Do(context.Background(), "k", load); err != nil {
		t.Fatal(err)
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("loader ran %d times within the TTL", n)
	}

	now = now.Add(2 * time.Second)
	if _, err := c.Do(context.Background(), "k", load); err != nil {
		t.Fatal(err)
	}
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Errorf("loader ran %d times after expiry, want 2", n)
	}
}

// A failure that is never cached is retried at full rate on every tick, which
// turns a transient throttle into a sustained one.
func TestCacheRemembersFailuresBriefly(t *testing.T) {
	now := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	var calls int32
	boom := errors.New("throttled")
	c := &Cache{TTL: time.Minute, ErrorTTL: 5 * time.Second, Now: func() time.Time { return now }}
	load := func(context.Context) (any, error) {
		atomic.AddInt32(&calls, 1)
		return nil, boom
	}

	for i := 0; i < 10; i++ {
		if _, err := c.Do(context.Background(), "k", load); !errors.Is(err, boom) {
			t.Fatalf("error = %v, want the loader's", err)
		}
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("loader ran %d times for a cached failure, want 1", n)
	}

	// The failure window is short, so a recovered dependency is noticed
	// quickly rather than being locked out for the full success TTL.
	now = now.Add(6 * time.Second)
	if _, err := c.Do(context.Background(), "k", load); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Errorf("loader ran %d times after the error TTL, want 2", n)
	}
}

func TestCacheKeysAreIndependent(t *testing.T) {
	var calls int32
	c := &Cache{TTL: time.Minute}
	load := func(context.Context) (any, error) {
		return atomic.AddInt32(&calls, 1), nil
	}

	a, _ := c.Do(context.Background(), "a", load)
	b, _ := c.Do(context.Background(), "b", load)
	aAgain, _ := c.Do(context.Background(), "a", load)

	if a == b {
		t.Error("two keys shared one value")
	}
	if a != aAgain {
		t.Errorf("key a returned %v then %v", a, aAgain)
	}
}

func TestCacheRespectsContextCancellationWhileWaiting(t *testing.T) {
	c := &Cache{TTL: time.Minute}
	release := make(chan struct{})
	started := make(chan struct{})

	go func() {
		_, _ = c.Do(context.Background(), "k", func(context.Context) (any, error) {
			close(started)
			<-release
			return "v", nil
		})
	}()
	<-started

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Do(ctx, "k", func(context.Context) (any, error) { return "other", nil }); !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
	close(release)
}

// The dashboard's intermittent "sometimes it loads, sometimes it errors".
//
// The browser aborts the in-flight page request whenever the selection changes,
// and the aborted request is usually the one holding the cache key. Its
// "context canceled" used to be stored as a cached failure and handed to the
// request that replaced it — and because the window is floored to the period, a
// 1m period keeps the key identical for a full minute, so the retry landed on
// the same poisoned entry.
func TestCacheDoesNotPoisonTheKeyWhenItsOwnCallerIsCancelled(t *testing.T) {
	c := &Cache{TTL: time.Minute}
	ctx, cancel := context.WithCancel(context.Background())

	// The first caller goes away mid-load, exactly as an aborted fetch does.
	_, err := c.Do(ctx, "k", func(ctx context.Context) (any, error) {
		cancel()
		return nil, ctx.Err()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("first caller error = %v, want context.Canceled", err)
	}
	if n := c.Len(); n != 0 {
		t.Errorf("cache holds %d entries after a cancelled load, want 0", n)
	}

	// The next caller has its own live context and must get a real answer.
	got, err := c.Do(context.Background(), "k", func(context.Context) (any, error) {
		return "value", nil
	})
	if err != nil {
		t.Fatalf("second caller error = %v", err)
	}
	if got != "value" {
		t.Errorf("second caller got %v, want value", got)
	}
}

// A caller already blocked on the entry has to loop round and reload rather
// than be handed the cancellation of the caller it was waiting on.
func TestCacheReloadsForAWaiterWhenTheLoadingCallerIsCancelled(t *testing.T) {
	c := &Cache{TTL: time.Minute}
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	release := make(chan struct{})

	go func() {
		_, _ = c.Do(ctx, "k", func(ctx context.Context) (any, error) {
			close(started)
			<-release
			return nil, ctx.Err()
		})
	}()
	<-started

	waited := make(chan error, 1)
	go func() {
		got, err := c.Do(context.Background(), "k", func(context.Context) (any, error) {
			return "value", nil
		})
		if err == nil && got != "value" {
			err = errors.New("waiter got " + got.(string))
		}
		waited <- err
	}()

	// Let the waiter arrive at the entry before the loading caller dies.
	time.Sleep(30 * time.Millisecond)
	cancel()
	close(release)

	select {
	case err := <-waited:
		if err != nil {
			t.Errorf("waiter error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the waiter never returned")
	}
}

// A failure that is not the caller's doing is still worth remembering briefly,
// so a failing dependency is not re-attempted at full rate.
func TestCacheStillRemembersFailuresFromALiveCaller(t *testing.T) {
	var calls int32
	c := &Cache{TTL: time.Minute, ErrorTTL: time.Minute}
	boom := errors.New("throttled")
	load := func(context.Context) (any, error) {
		atomic.AddInt32(&calls, 1)
		return nil, boom
	}

	for i := 0; i < 3; i++ {
		if _, err := c.Do(context.Background(), "k", load); !errors.Is(err, boom) {
			t.Fatalf("error = %v", err)
		}
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("loader ran %d times, want 1", n)
	}
}

// An unbounded map that is never swept is a leak waiting for a new key format.
func TestCacheEvictsExpiredEntries(t *testing.T) {
	now := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	c := &Cache{TTL: 10 * time.Second, Now: func() time.Time { return now }}
	load := func(context.Context) (any, error) { return "v", nil }

	for i := 0; i < 100; i++ {
		if _, err := c.Do(context.Background(), string(rune('a'+i%26))+string(rune('a'+i/26)), load); err != nil {
			t.Fatal(err)
		}
	}
	before := c.Len()
	if before == 0 {
		t.Fatal("nothing was cached")
	}

	now = now.Add(time.Minute)
	if _, err := c.Do(context.Background(), "fresh", load); err != nil {
		t.Fatal(err)
	}
	if after := c.Len(); after >= before {
		t.Errorf("cache held %d entries before the sweep and %d after", before, after)
	}
}

func TestCacheInvalidate(t *testing.T) {
	var calls int32
	c := &Cache{TTL: time.Minute}
	load := func(context.Context) (any, error) {
		atomic.AddInt32(&calls, 1)
		return "v", nil
	}

	if _, err := c.Do(context.Background(), "k", load); err != nil {
		t.Fatal(err)
	}
	c.Invalidate()
	if _, err := c.Do(context.Background(), "k", load); err != nil {
		t.Fatal(err)
	}
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Errorf("loader ran %d times across an invalidation, want 2", n)
	}
}

func TestCachedIsTyped(t *testing.T) {
	c := &Cache{TTL: time.Minute}
	got, err := Cached(context.Background(), c, "k", func(context.Context) ([]string, error) {
		return []string{"a", "b"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "a" {
		t.Errorf("got %v", got)
	}

	// A cached failure must surface as the zero value plus the error, not as a
	// silently empty success.
	boom := errors.New("nope")
	_, err = Cached(context.Background(), c, "e", func(context.Context) (int, error) {
		return 0, boom
	})
	if !errors.Is(err, boom) {
		t.Errorf("error = %v", err)
	}
}
