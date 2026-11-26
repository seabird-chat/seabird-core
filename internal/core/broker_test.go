package core

import (
	"context"
	"io"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/alecthomas/assert/v2"
	"github.com/seabird-chat/seabird-core/internal/pb"
)

// nextTestEvent returns the next event on sub, failing the test if none
// arrives in time.
func nextTestEvent(t *testing.T, sub EventSubscription) *pb.Event {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	event, err := sub.Next(ctx)
	assert.NoError(t, err)
	return event
}

// subscribeTestBroker subscribes to broker until the test ends.
func subscribeTestBroker(t *testing.T, broker *MemoryBroker) EventSubscription {
	t.Helper()

	sub, err := broker.Subscribe(context.Background())
	assert.NoError(t, err)
	t.Cleanup(func() { sub.Close() })
	return sub
}

func TestMemoryBrokerFanout(t *testing.T) {
	t.Parallel()

	var broker MemoryBroker
	assert.NoError(t, broker.Publish(context.Background(), &pb.Event{}), "publish without subscribers")

	first := subscribeTestBroker(t, &broker)
	setupCtx, cancel := context.WithCancel(context.Background())
	second, err := broker.Subscribe(setupCtx)
	assert.NoError(t, err)
	t.Cleanup(func() { second.Close() })
	cancel()

	for i := range 3 {
		event := &pb.Event{Tags: map[string]string{"sequence": strconv.Itoa(i)}}
		assert.NoError(t, broker.Publish(context.Background(), event))
		for _, sub := range []EventSubscription{first, second} {
			got := nextTestEvent(t, sub)
			assert.True(t, got == event, "got event %v, want %v", got, event)
		}
	}
}

func TestMemoryBrokerWakesReader(t *testing.T) {
	t.Parallel()

	var broker MemoryBroker
	sub := subscribeTestBroker(t, &broker)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	started := make(chan struct{})
	done := runAsync(func() (*pb.Event, error) {
		close(started)
		return sub.Next(ctx)
	})
	<-started

	want := &pb.Event{}
	assert.NoError(t, broker.Publish(ctx, want))
	got := waitFor(t, done, "the reader to wake after publish")
	assert.NoError(t, got.err)
	assert.True(t, got.value == want, "next: got %v, want %v", got.value, want)
}

func TestMemoryBrokerLag(t *testing.T) {
	t.Parallel()

	var broker MemoryBroker
	slow := subscribeTestBroker(t, &broker)
	fast := subscribeTestBroker(t, &broker)
	events := make([]*pb.Event, memoryBrokerCapacity+3)

	for i := range events {
		events[i] = &pb.Event{Tags: map[string]string{"sequence": strconv.Itoa(i)}}
		assert.NoError(t, broker.Publish(context.Background(), events[i]))
		got := nextTestEvent(t, fast)
		assert.True(t, got == events[i], "fast subscriber got %v, want %v", got, events[i])
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := slow.Next(canceled)
	assert.IsError(t, err, context.Canceled)

	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	event, err := slow.Next(ctx)
	assert.IsError(t, err, ErrLagged)
	assert.Zero(t, event)
	for _, want := range events[3:] {
		got := nextTestEvent(t, slow)
		assert.True(t, got == want, "lagging subscriber got %v, want %v", got, want)
	}

	next := &pb.Event{Tags: map[string]string{"sequence": "next"}}
	assert.NoError(t, broker.Publish(context.Background(), next), "publish after lag")
	got := nextTestEvent(t, slow)
	assert.True(t, got == next, "subscriber after lag got %v, want %v", got, next)
}

func TestMemoryBrokerCancellation(t *testing.T) {
	t.Parallel()

	var broker MemoryBroker
	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := broker.Subscribe(canceled)
	assert.IsError(t, err, context.Canceled, "canceled subscribe")
	sub := subscribeTestBroker(t, &broker)
	assert.IsError(t, broker.Publish(canceled, &pb.Event{}), context.Canceled, "canceled publish")
	assert.Error(t, broker.Publish(context.Background(), nil), "nil event")

	waitCtx, stopWait := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer stopWait()
	_, err = sub.Next(waitCtx)
	assert.IsError(t, err, context.DeadlineExceeded, "empty subscription")

	want := &pb.Event{}
	assert.NoError(t, broker.Publish(context.Background(), want))
	_, err = sub.Next(canceled)
	assert.IsError(t, err, context.Canceled, "canceled next")
	got := nextTestEvent(t, sub)
	assert.True(t, got == want, "got %v after canceled next, want %v", got, want)
}

func TestMemoryBrokerClose(t *testing.T) {
	t.Parallel()

	var broker MemoryBroker
	sub := subscribeTestBroker(t, &broker)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	started := make(chan struct{})
	done := runAsync(func() (*pb.Event, error) {
		close(started)
		return sub.Next(ctx)
	})
	<-started

	assert.NoError(t, sub.Close())
	assert.NoError(t, sub.Close(), "close again")
	got := waitFor(t, done, "the blocked reader to wake after close")
	assert.IsError(t, got.err, io.EOF)

	assert.NoError(t, broker.Publish(context.Background(), &pb.Event{}), "publish after close")
}

func TestMemoryBrokerConcurrentPublishOrder(t *testing.T) {
	t.Parallel()

	var broker MemoryBroker
	first := subscribeTestBroker(t, &broker)
	second := subscribeTestBroker(t, &broker)

	const publishers = 16
	start := make(chan struct{})
	errs := make(chan error, publishers)
	var wg sync.WaitGroup
	for i := range publishers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			event := &pb.Event{Tags: map[string]string{"sequence": strconv.Itoa(i)}}
			errs <- broker.Publish(context.Background(), event)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		assert.NoError(t, err)
	}

	seen := make(map[string]bool)
	for range publishers {
		a := nextTestEvent(t, first)
		b := nextTestEvent(t, second)
		assert.True(t, a == b, "subscribers received events in different orders: %v, %v", a, b)
		seen[a.GetTags()["sequence"]] = true
	}
	assert.Equal(t, publishers, len(seen), "distinct events")
}

func TestMemoryBrokerConcurrentClose(t *testing.T) {
	t.Parallel()

	var broker MemoryBroker
	sub := subscribeTestBroker(t, &broker)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := make(chan struct{})
	done := make(chan error, 2)
	go func() {
		<-start
		for range 200 {
			if err := broker.Publish(ctx, &pb.Event{}); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	go func() {
		<-start
		done <- sub.Close()
	}()
	close(start)

	for range 2 {
		assert.NoError(t, waitFor(t, done, "concurrent publish and close"))
	}
	_, err := sub.Next(ctx)
	assert.IsError(t, err, io.EOF, "next after close")
}

func TestMemoryBrokerCloseDiscardsUnreadEvents(t *testing.T) {
	t.Parallel()

	var broker MemoryBroker
	sub := subscribeTestBroker(t, &broker)

	assert.NoError(t, broker.Publish(context.Background(), &pb.Event{}))
	assert.NoError(t, sub.Close())

	event, err := sub.Next(context.Background())
	assert.IsError(t, err, io.EOF)
	assert.Zero(t, event)
}

func TestMemoryBrokerLagReportedOncePerGap(t *testing.T) {
	t.Parallel()

	var broker MemoryBroker
	sub := subscribeTestBroker(t, &broker)

	// Overflow several times before the reader looks.
	for range memoryBrokerCapacity * 3 {
		assert.NoError(t, broker.Publish(context.Background(), &pb.Event{}))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := sub.Next(ctx)
	assert.IsError(t, err, ErrLagged, "first read")

	// The retained events follow without another ErrLagged.
	for i := range memoryBrokerCapacity {
		_, err := sub.Next(ctx)
		assert.NoError(t, err, "retained event %d", i)
	}

	// And then the queue is empty, not lagged again.
	short, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer stop()
	_, err = sub.Next(short)
	assert.IsError(t, err, context.DeadlineExceeded, "after draining")
}
