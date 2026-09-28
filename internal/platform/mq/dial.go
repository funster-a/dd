package mq

import (
	"context"
	"net"
	"time"
)

const defaultDialTimeout = 5 * time.Second

// dialer возвращает функцию установки TCP-соединения, которая уважает
// дедлайн и отмену ctx.
func dialer(ctx context.Context) func(network, addr string) (net.Conn, error) {
	return func(network, addr string) (net.Conn, error) {
		d := net.Dialer{Timeout: defaultDialTimeout}
		conn, err := d.DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		// Дедлайн нужен на время AMQP-рукопожатия; amqp091 снимает его после.
		// Он не позже дедлайна ctx, чтобы проверка готовности укладывалась в свой бюджет.
		deadline := time.Now().Add(defaultDialTimeout)
		if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
			deadline = ctxDeadline
		}
		if err := conn.SetDeadline(deadline); err != nil {
			_ = conn.Close()
			return nil, err
		}
		return conn, nil
	}
}
