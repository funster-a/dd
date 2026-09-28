package mq

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Интеграционные тесты: нужен запущенный RabbitMQ, например
// RABBITMQ_TEST_URL=amqp://dd:dd@localhost:5672/ go test ./internal/platform/mq/
func testURL(t *testing.T) string {
	t.Helper()
	url := os.Getenv("RABBITMQ_TEST_URL")
	if url == "" {
		t.Skip("RABBITMQ_TEST_URL is not set")
	}
	return url
}

func publish(t *testing.T, c *Conn, queue string, bodies ...string) {
	t.Helper()
	conn, err := c.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ch, err := conn.Channel()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ch.Close() }()
	if _, err := ch.QueueDeclare(queue, true, false, false, false, nil); err != nil {
		t.Fatal(err)
	}
	for _, b := range bodies {
		if err := ch.PublishWithContext(context.Background(), "", queue, false, false, amqp.Publishing{Body: []byte(b)}); err != nil {
			t.Fatal(err)
		}
	}
}

func queueDepth(t *testing.T, c *Conn, queue string) int {
	t.Helper()
	conn, err := c.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ch, err := conn.Channel()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ch.Close() }()
	q, err := ch.QueueDeclarePassive(queue, true, false, false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	return q.Messages
}

func deleteQueue(t *testing.T, c *Conn, queue string) {
	t.Helper()
	t.Cleanup(func() {
		conn, err := c.Get(context.Background())
		if err != nil {
			return
		}
		if ch, err := conn.Channel(); err == nil {
			_, _ = ch.QueueDelete(queue, false, false, false)
			_ = ch.Close()
		}
		_ = c.Close()
	})
}

func TestConsumeAcksAndStops(t *testing.T) {
	c := New(testURL(t))
	queue := "test." + rand.Text()
	deleteQueue(t, c, queue)
	publish(t, c, queue, "one", "two", "three")

	var (
		mu  sync.Mutex
		got []string
	)
	received := make(chan struct{}, 3)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		Consume(ctx, c, ConsumerConfig{Queue: queue, Prefetch: 10, Tag: "test"}, slog.New(slog.DiscardHandler),
			func(_ context.Context, d *amqp.Delivery) error {
				mu.Lock()
				got = append(got, string(d.Body))
				mu.Unlock()
				received <- struct{}{}
				return nil
			})
		close(done)
	}()

	for range 3 {
		select {
		case <-received:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for messages")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Consume did not return after cancel")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 3 {
		t.Errorf("received %v, want 3 messages", got)
	}
	if n := queueDepth(t, c, queue); n != 0 {
		t.Errorf("queue depth = %d after ack, want 0", n)
	}
}

func TestConsumeFinishesInFlightMessageOnCancel(t *testing.T) {
	c := New(testURL(t))
	queue := "test." + rand.Text()
	deleteQueue(t, c, queue)
	publish(t, c, queue, "slow", "left-1", "left-2")

	started := make(chan struct{})
	var handled []string
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		Consume(ctx, c, ConsumerConfig{Queue: queue, Prefetch: 10, Tag: "test"}, slog.New(slog.DiscardHandler),
			func(hctx context.Context, d *amqp.Delivery) error {
				handled = append(handled, string(d.Body))
				if string(d.Body) == "slow" {
					close(started)
					time.Sleep(500 * time.Millisecond)
					if hctx.Err() != nil {
						return errors.New("handler context was cancelled")
					}
				}
				return nil
			})
		close(done)
	}()

	<-started
	cancel()
	<-done

	if len(handled) != 1 || handled[0] != "slow" {
		t.Errorf("handled = %v, want only the in-flight message", handled)
	}
	// Первое сообщение подтверждено, два оставшихся вернулись в очередь.
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		n := queueDepth(t, c, queue)
		if n == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("queue depth = %d, want 2 unacked messages returned", n)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestConsumeRejectsFailedMessage(t *testing.T) {
	c := New(testURL(t))
	queue := "test." + rand.Text()
	deleteQueue(t, c, queue)
	publish(t, c, queue, "bad")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	calls := make(chan struct{}, 10)
	go func() {
		Consume(ctx, c, ConsumerConfig{Queue: queue, Prefetch: 1, Tag: "test"}, slog.New(slog.DiscardHandler),
			func(context.Context, *amqp.Delivery) error {
				calls <- struct{}{}
				return errors.New("boom")
			})
		close(done)
	}()

	<-calls
	time.Sleep(300 * time.Millisecond) // дать шанс повторной доставке, если бы она была
	cancel()
	<-done

	if n := len(calls); n != 0 {
		t.Errorf("message redelivered %d more times, want 0", n)
	}
	if n := queueDepth(t, c, queue); n != 0 {
		t.Errorf("queue depth = %d, want 0 (message dropped)", n)
	}
}
