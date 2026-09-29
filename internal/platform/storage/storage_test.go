package storage_test

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/funster-a/dd/internal/platform/storage"
)

// Интеграционный тест: нужен S3 (make infra-up поднимает SeaweedFS) и
// S3_TEST_ENDPOINT, например http://localhost:8333.
func newClient(t *testing.T) *storage.Client {
	t.Helper()
	endpoint := os.Getenv("S3_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("S3_TEST_ENDPOINT is not set")
	}
	c, err := storage.New(storage.Config{
		Endpoint: endpoint, PublicEndpoint: endpoint,
		AccessKey: envOr("S3_TEST_ACCESS_KEY", "dd"), SecretKey: envOr("S3_TEST_SECRET_KEY", "dd-secret-key"),
		Bucket: envOr("S3_TEST_BUCKET", "dd-media"), Region: "us-east-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Ping(t.Context()); err != nil {
		t.Fatalf("Ping() error = %v", err)
	}
	return c
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// TestUploadFlow проходит путь браузера: ссылка → PUT → проверка → публичное чтение.
func TestUploadFlow(t *testing.T) {
	c := newClient(t)
	ctx := t.Context()
	key := "test/" + strings.ToLower(rand.Text()) + ".png"
	payload := bytes.Repeat([]byte{0x89, 'P', 'N', 'G'}, 256)

	if _, err := c.Stat(ctx, key); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Stat() before upload: err = %v, want ErrNotFound", err)
	}

	uploadURL, err := c.PresignUpload(ctx, key, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "image/png")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload: status %d, body %s", resp.StatusCode, body)
	}

	info, err := c.Stat(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size != int64(len(payload)) || info.ContentType != "image/png" {
		t.Fatalf("Stat() = %+v, want %d bytes image/png", info, len(payload))
	}

	// Обложки публичные: читаются без подписи.
	get, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.URL(key), nil)
	resp, err = http.DefaultClient.Do(get)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !bytes.Equal(got, payload) {
		t.Fatalf("public read: status %d, %d bytes", resp.StatusCode, len(got))
	}

	// Подпись не даёт загрузить по чужому ключу.
	tampered := strings.Replace(uploadURL, key, key+"x", 1)
	req, _ = http.NewRequestWithContext(ctx, http.MethodPut, tampered, bytes.NewReader(payload))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("upload with tampered key succeeded")
	}
}
