package main

import (
	"context"
	"errors"
	"time"

	"github.com/funster-a/dd/internal/catalog"
	"github.com/funster-a/dd/internal/platform/storage"
)

// objectStore связывает общий клиент хранилища с интерфейсом, который
// объявил каталог: сам каталог от platform/storage не зависит.
type objectStore struct {
	c *storage.Client
}

func (s objectStore) PresignUpload(ctx context.Context, key, _ string, ttl time.Duration) (string, error) {
	return s.c.PresignUpload(ctx, key, ttl)
}

func (s objectStore) Stat(ctx context.Context, key string) (catalog.ObjectInfo, error) {
	info, err := s.c.Stat(ctx, key)
	if errors.Is(err, storage.ErrNotFound) {
		return catalog.ObjectInfo{}, catalog.ErrObjectNotFound
	}
	if err != nil {
		return catalog.ObjectInfo{}, err
	}
	return catalog.ObjectInfo{Size: info.Size, ContentType: info.ContentType}, nil
}
