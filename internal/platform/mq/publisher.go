package mq

import (
	"context"
	"fmt"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// DeclareQueue объявляет долговечную очередь. С deadLetter объявляется и
// очередь <name>.dead, куда RabbitMQ перекладывает отклонённые сообщения.
// Аргументы очереди должны совпадать при каждом объявлении, поэтому и
// издатель, и потребитель объявляют её этой функцией.
func DeclareQueue(ch *amqp.Channel, name string, deadLetter bool) error {
	var args amqp.Table
	if deadLetter {
		dead := name + ".dead"
		if _, err := ch.QueueDeclare(dead, true, false, false, false, nil); err != nil {
			return fmt.Errorf("declare queue %q: %w", dead, err)
		}
		args = amqp.Table{"x-dead-letter-exchange": "", "x-dead-letter-routing-key": dead}
	}
	if _, err := ch.QueueDeclare(name, true, false, false, false, args); err != nil {
		return fmt.Errorf("declare queue %q: %w", name, err)
	}
	return nil
}

// Publisher публикует сообщения с подтверждением брокера: Publish
// возвращает nil, только когда RabbitMQ принял сообщение на хранение.
type Publisher struct {
	c *Conn

	mu       sync.Mutex
	ch       *amqp.Channel
	declared map[string]bool
}

// NewPublisher создаёт издателя поверх соединения c.
func NewPublisher(c *Conn) *Publisher {
	return &Publisher{c: c}
}

// confirmTimeout — сколько ждать подтверждения брокера.
const confirmTimeout = 10 * time.Second

// Publish кладёт сообщение в очередь queue (через обменник по умолчанию)
// как persistent и ждёт подтверждения. messageID попадает в свойства
// сообщения и помогает найти его в логах.
func (p *Publisher) Publish(ctx context.Context, queue string, deadLetter bool, messageID string, body []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	ch, err := p.channel(ctx)
	if err != nil {
		return err
	}
	if !p.declared[queue] {
		if err := DeclareQueue(ch, queue, deadLetter); err != nil {
			p.reset()
			return err
		}
		p.declared[queue] = true
	}

	ctx, cancel := context.WithTimeout(ctx, confirmTimeout)
	defer cancel()
	conf, err := ch.PublishWithDeferredConfirmWithContext(ctx, "", queue, true, false, amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		MessageId:    messageID,
		Timestamp:    time.Now().UTC(),
		Body:         body,
	})
	if err != nil {
		p.reset()
		return fmt.Errorf("publish to %q: %w", queue, err)
	}
	ok, err := conf.WaitContext(ctx)
	if err != nil {
		p.reset()
		return fmt.Errorf("wait confirm from %q: %w", queue, err)
	}
	if !ok {
		return fmt.Errorf("publish to %q: broker rejected the message", queue)
	}
	return nil
}

func (p *Publisher) channel(ctx context.Context) (*amqp.Channel, error) {
	if p.ch != nil && !p.ch.IsClosed() {
		return p.ch, nil
	}
	conn, err := p.c.Get(ctx)
	if err != nil {
		return nil, err
	}
	ch, err := conn.Channel()
	if err != nil {
		return nil, fmt.Errorf("open channel: %w", err)
	}
	if err := ch.Confirm(false); err != nil {
		_ = ch.Close()
		return nil, fmt.Errorf("enable confirms: %w", err)
	}
	p.ch = ch
	p.declared = map[string]bool{}
	return ch, nil
}

// reset закрывает канал после ошибки: следующий Publish откроет новый.
func (p *Publisher) reset() {
	if p.ch != nil {
		_ = p.ch.Close() // канал уже мог закрыться вместе с ошибкой
		p.ch = nil
	}
}

// Close закрывает канал издателя. Соединение закрывает его владелец.
func (p *Publisher) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reset()
}
