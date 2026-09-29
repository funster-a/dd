package mq

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Издатель доставляет сообщение с подтверждением, а сообщение, которое
// обработчик отклонил, попадает в очередь <имя>.dead, а не пропадает.
func TestPublishAndDeadLetter(t *testing.T) {
	c := New(testURL(t))
	queue := "test.dl." + strings.ToLower(rand.Text()[:8])
	deleteQueue(t, c, queue)
	deleteQueue(t, c, queue+".dead")

	pub := NewPublisher(c)
	defer pub.Close()
	for _, body := range []string{`{"n":1}`, `{"n":2}`} {
		if err := pub.Publish(t.Context(), queue, true, "m-"+body, []byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if n := queueDepth(t, c, queue); n != 2 {
		t.Fatalf("queue depth = %d, want 2", n)
	}

	ctx, cancel := context.WithCancel(t.Context())
	handled := make(chan string, 2)
	done := make(chan struct{})
	go func() {
		Consume(ctx, c, ConsumerConfig{Queue: queue, Prefetch: 1, Tag: "test", DeadLetter: true}, slog.New(slog.DiscardHandler),
			func(_ context.Context, d *amqp.Delivery) error {
				handled <- d.MessageId
				if strings.Contains(string(d.Body), `"n":2`) {
					return errors.New("poison")
				}
				return nil
			})
		close(done)
	}()
	for range 2 {
		select {
		case <-handled:
		case <-time.After(10 * time.Second):
			t.Fatal("messages were not consumed")
		}
	}
	cancel()
	<-done

	deadline := time.Now().Add(5 * time.Second)
	for queueDepth(t, c, queue+".dead") != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("dead-letter depth = %d, want 1", queueDepth(t, c, queue+".dead"))
		}
		time.Sleep(50 * time.Millisecond)
	}
	if n := queueDepth(t, c, queue); n != 0 {
		t.Errorf("queue depth = %d, want 0", n)
	}
}
