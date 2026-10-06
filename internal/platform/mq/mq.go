// Package mq управляет подключением к RabbitMQ.
package mq

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Conn хранит одно соединение с RabbitMQ и переподключается,
// если оно закрылось. Узлов кластера может быть несколько (ADR 030):
// соединение идёт к первому доступному, а после обрыва — начиная со
// следующего узла, чтобы не ждать упавший.
type Conn struct {
	urls []string

	mu   sync.Mutex
	conn *amqp.Connection
	last int // узел текущего или последнего соединения
}

// New создаёт Conn без подключения. url — адрес узла или несколько адресов
// узлов кластера через запятую. Подключение происходит при первом вызове
// Get или Ping.
func New(url string) *Conn {
	var urls []string
	for u := range strings.SplitSeq(url, ",") {
		if u = strings.TrimSpace(u); u != "" {
			urls = append(urls, u)
		}
	}
	if len(urls) == 0 {
		urls = []string{""}
	}
	return &Conn{urls: urls}
}

// Get возвращает открытое соединение, при необходимости подключаясь заново.
func (c *Conn) Get(ctx context.Context) (*amqp.Connection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil && !c.conn.IsClosed() {
		return c.conn, nil
	}
	start := c.last
	if c.conn != nil {
		start++ // прежнее соединение оборвалось: его узел пробуем последним
	}
	var errs []error
	for i := range c.urls {
		n := (start + i) % len(c.urls)
		conn, err := amqp.DialConfig(c.urls[n], amqp.Config{Dial: dialer(ctx)})
		if err != nil {
			if _, ok := errors.AsType[*url.Error](err); ok {
				return nil, errors.New("dial rabbitmq: invalid URL")
			}
			errs = append(errs, err)
			if ctx.Err() != nil {
				break
			}
			continue
		}
		c.conn, c.last = conn, n
		return conn, nil
	}
	return nil, fmt.Errorf("dial rabbitmq: %w", errors.Join(errs...))
}

// Ping проверяет, что соединение открыто или может быть открыто.
func (c *Conn) Ping(ctx context.Context) error {
	_, err := c.Get(ctx)
	return err
}

// closeTimeout ограничивает ожидание ответа сервера при закрытии.
const closeTimeout = 5 * time.Second

// Close закрывает соединение, если оно открыто. Ответа сервера ждёт
// не дольше closeTimeout, чтобы зависший RabbitMQ не держал выход процесса.
func (c *Conn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil || c.conn.IsClosed() {
		return nil
	}
	if err := c.conn.CloseDeadline(time.Now().Add(closeTimeout)); err != nil && !errors.Is(err, amqp.ErrClosed) {
		return fmt.Errorf("close rabbitmq: %w", err)
	}
	return nil
}
