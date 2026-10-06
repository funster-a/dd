package mq

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// DeclareQueue объявляет долговечную кворумную очередь (ADR 030): её копии
// живут на трёх узлах кластера, и она переживает потерю одного. С deadLetter
// объявляется и очередь <name>.dead, куда RabbitMQ перекладывает отклонённые
// сообщения. Аргументы очереди должны совпадать при каждом объявлении,
// поэтому и издатель, и потребитель объявляют её этой функцией.
//
// Очередь, объявленная раньше классической, заменяется кворумной, если она
// пуста; непустая — ошибка: сообщения в ней нужно сначала обработать.
// Объявление идёт в своём канале: несовпадение аргументов закрывает канал.
func DeclareQueue(conn *amqp.Connection, name string, deadLetter bool) error {
	if deadLetter {
		dead := name + ".dead"
		if err := declareQuorum(conn, dead, nil); err != nil {
			return err
		}
		// at-least-once: сообщение не теряется по пути в очередь .dead, даже
		// если узел упал посреди перекладывания.
		return declareQuorum(conn, name, amqp.Table{
			"x-dead-letter-exchange": "", "x-dead-letter-routing-key": dead,
			"x-dead-letter-strategy": "at-least-once", "x-overflow": "reject-publish",
		})
	}
	return declareQuorum(conn, name, nil)
}

func declareQuorum(conn *amqp.Connection, name string, args amqp.Table) error {
	all := amqp.Table{"x-queue-type": "quorum"}
	for k, v := range args {
		all[k] = v
	}
	err := withChannel(conn, func(ch *amqp.Channel) error {
		_, err := ch.QueueDeclare(name, true, false, false, false, all)
		return err
	})
	if !isPreconditionFailed(err) {
		if err != nil {
			return fmt.Errorf("declare queue %q: %w", name, err)
		}
		return nil
	}
	// Очередь уже есть с другими аргументами — классическая из прежних версий.
	err = withChannel(conn, func(ch *amqp.Channel) error {
		if _, err := ch.QueueDelete(name, false, true, false); err != nil {
			return err
		}
		_, err := ch.QueueDeclare(name, true, false, false, false, all)
		return err
	})
	if err != nil {
		return fmt.Errorf("replace queue %q with a quorum queue (it must be empty): %w", name, err)
	}
	return nil
}

func withChannel(conn *amqp.Connection, f func(*amqp.Channel) error) error {
	ch, err := conn.Channel()
	if err != nil {
		return fmt.Errorf("open channel: %w", err)
	}
	defer func() { _ = ch.Close() }() // канал уже мог закрыться вместе с ошибкой
	return f(ch)
}

func isPreconditionFailed(err error) bool {
	e, ok := errors.AsType[*amqp.Error](err)
	return ok && e.Code == amqp.PreconditionFailed
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
		conn, err := p.c.Get(ctx)
		if err != nil {
			return err
		}
		if err := DeclareQueue(conn, queue, deadLetter); err != nil {
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
