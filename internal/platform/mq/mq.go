// Package mq управляет подключением к RabbitMQ.
package mq

import (
	"context"
	"errors"
	"fmt"
	"sync"

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

// Close закрывает соединение, если оно открыто.
func (c *Conn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil || c.conn.IsClosed() {
		return nil
	}
	if err := c.conn.Close(); err != nil && !errors.Is(err, amqp.ErrClosed) {
		return fmt.Errorf("close rabbitmq: %w", err)
	}
	return nil
}
