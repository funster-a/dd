// Package mq управляет подключением к RabbitMQ.
package mq

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Conn хранит одно соединение с RabbitMQ и переподключается,
// если оно закрылось.
type Conn struct {
	url string

	mu   sync.Mutex
	conn *amqp.Connection
}

// New создаёт Conn без подключения. Подключение происходит при первом
// вызове Get или Ping.
func New(url string) *Conn {
	return &Conn{url: url}
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

	conn, err := amqp.DialConfig(c.url, amqp.Config{Dial: dialer(ctx)})
	if err != nil {
		// Ошибка разбора URL цитирует его целиком вместе с паролем.
		if _, ok := errors.AsType[*url.Error](err); ok {
			return nil, errors.New("dial rabbitmq: invalid URL")
		}
		return nil, fmt.Errorf("dial rabbitmq: %w", err)
	}
	c.conn = conn
	return conn, nil
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
