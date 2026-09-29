// Package storage — клиент S3-совместимого хранилища файлов (ADR 009):
// ссылки для загрузки из браузера, проверка загруженных файлов и публичные
// адреса. Работает с любым S3: SeaweedFS локально, облачный S3 в эксплуатации.
package storage

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// ErrNotFound — файла с таким ключом нет.
var ErrNotFound = errors.New("object not found")

// Config — параметры подключения.
type Config struct {
	// Endpoint — адрес S3 для api (внутри сети).
	Endpoint string
	// PublicEndpoint — адрес S3 для браузера: по нему подписываются ссылки
	// загрузки и строятся публичные адреса файлов.
	PublicEndpoint string
	AccessKey      string
	SecretKey      string
	Bucket         string
	Region         string
}

// Info — метаданные файла.
type Info struct {
	Size        int64
	ContentType string
}

// Client — клиент хранилища.
type Client struct {
	internal   *minio.Client
	public     *minio.Client
	bucket     string
	publicBase string
}

// New создаёт клиент. Сеть не используется: подключение проверяет Ping.
func New(cfg Config) (*Client, error) {
	internal, err := newMinio(cfg.Endpoint, cfg)
	if err != nil {
		return nil, fmt.Errorf("S3 endpoint: %w", err)
	}
	public, err := newMinio(cfg.PublicEndpoint, cfg)
	if err != nil {
		return nil, fmt.Errorf("S3 public endpoint: %w", err)
	}
	return &Client{
		internal: internal, public: public, bucket: cfg.Bucket,
		publicBase: strings.TrimRight(cfg.PublicEndpoint, "/") + "/" + cfg.Bucket + "/",
	}, nil
}

func newMinio(endpoint string, cfg Config) (*minio.Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("must be an http(s) URL, got %q", endpoint)
	}
	return minio.New(u.Host, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: u.Scheme == "https",
		// С известным регионом клиент не запрашивает его у сервера,
		// и подпись ссылок работает без обращения к сети.
		Region:       cfg.Region,
		BucketLookup: minio.BucketLookupPath,
	})
}

// PresignUpload выдаёт ссылку для загрузки файла методом PUT.
func (c *Client) PresignUpload(ctx context.Context, key string, ttl time.Duration) (string, error) {
	u, err := c.public.PresignedPutObject(ctx, c.bucket, key, ttl)
	if err != nil {
		return "", fmt.Errorf("presign put %s: %w", key, err)
	}
	return u.String(), nil
}

// Stat возвращает размер и тип файла.
func (c *Client) Stat(ctx context.Context, key string) (Info, error) {
	obj, err := c.internal.StatObject(ctx, c.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		if minio.ToErrorResponse(err).Code == minio.NoSuchKey {
			return Info{}, ErrNotFound
		}
		return Info{}, fmt.Errorf("stat %s: %w", key, err)
	}
	return Info{Size: obj.Size, ContentType: obj.ContentType}, nil
}

// URL — публичный адрес файла (бакет открыт на чтение, ADR 009).
func (c *Client) URL(key string) string {
	return c.publicBase + key
}

// Ping проверяет, что хранилище доступно и бакет есть.
func (c *Client) Ping(ctx context.Context) error {
	ok, err := c.internal.BucketExists(ctx, c.bucket)
	if err != nil {
		return fmt.Errorf("check bucket: %w", err)
	}
	if !ok {
		return fmt.Errorf("bucket %q does not exist", c.bucket)
	}
	return nil
}
