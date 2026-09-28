package catalog

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/funster-a/dd/internal/catalog/catalogdb"
)

// ObjectStore — хранилище файлов (S3-совместимое, ADR 009). Интерфейс
// объявлен на стороне потребителя: каталог не знает, MinIO это или AWS.
type ObjectStore interface {
	// PresignUpload выдаёт одноразовую ссылку для загрузки файла напрямую
	// из браузера, минуя api.
	PresignUpload(ctx context.Context, key, contentType string, ttl time.Duration) (string, error)
	// Stat возвращает размер и тип загруженного файла; ErrObjectNotFound — файла нет.
	Stat(ctx context.Context, key string) (ObjectInfo, error)
}

// ObjectInfo — метаданные файла в хранилище.
type ObjectInfo struct {
	Size        int64
	ContentType string
}

// ErrObjectNotFound — файла с таким ключом нет в хранилище.
var ErrObjectNotFound = errors.New("object not found")

// Виды медиа события и их ограничения (spec.md).
const (
	MediaCoverImage = "cover_image"
	MediaCoverVideo = "cover_video"

	uploadURLTTL = 15 * time.Minute
)

type mediaRule struct {
	maxBytes int64
	types    map[string]string // content type → расширение
}

var mediaRules = map[string]mediaRule{
	MediaCoverImage: {maxBytes: 5 << 20, types: map[string]string{
		"image/jpeg": "jpg", "image/png": "png", "image/webp": "webp",
	}},
	MediaCoverVideo: {maxBytes: 15 << 20, types: map[string]string{
		"video/mp4": "mp4", "video/webm": "webm",
	}},
}

// UploadRequest — что организатор собирается загрузить.
type UploadRequest struct {
	Kind        string `json:"kind"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
}

// UploadTarget — куда и как загрузить файл.
type UploadTarget struct {
	Key       string            `json:"key"`
	URL       string            `json:"upload_url"`
	Method    string            `json:"method"`
	Headers   map[string]string `json:"headers"`
	ExpiresAt time.Time         `json:"expires_at"`
}

// MediaInput — какие загруженные файлы прикрепить к событию.
// Пустая видеообложка снимает её.
type MediaInput struct {
	CoverImageKey string  `json:"cover_image_key"`
	CoverVideoKey *string `json:"cover_video_key"`
}

// CreateUpload выдаёт ссылку для загрузки обложки события.
func (s *Service) CreateUpload(ctx context.Context, organizerID, eventID string, req UploadRequest) (UploadTarget, error) {
	if s.store == nil {
		return UploadTarget{}, errors.New("object store is not configured")
	}
	rule, ok := mediaRules[req.Kind]
	if !ok {
		return UploadTarget{}, &ValidationError{Field: "kind", Message: `must be "cover_image" or "cover_video"`}
	}
	ext, ok := rule.types[req.ContentType]
	if !ok {
		return UploadTarget{}, &ValidationError{Field: "content_type", Message: "unsupported type; allowed: " + strings.Join(sortedKeys(rule.types), ", ")}
	}
	if req.Size < 1 || req.Size > rule.maxBytes {
		return UploadTarget{}, &ValidationError{Field: "size", Message: fmt.Sprintf("must be 1-%d bytes", rule.maxBytes)}
	}
	ev, err := s.GetEvent(ctx, organizerID, eventID)
	if err != nil {
		return UploadTarget{}, err
	}
	if ev.Status == "cancelled" {
		return UploadTarget{}, requireDraft(ev.Status)
	}

	key := mediaPrefix(organizerID, eventID, req.Kind) + strings.ToLower(rand.Text()) + "." + ext
	url, err := s.store.PresignUpload(ctx, key, req.ContentType, uploadURLTTL)
	if err != nil {
		return UploadTarget{}, fmt.Errorf("presign upload: %w", err)
	}
	return UploadTarget{
		Key: key, URL: url, Method: http.MethodPut,
		Headers:   map[string]string{"Content-Type": req.ContentType},
		ExpiresAt: time.Now().UTC().Add(uploadURLTTL).Truncate(time.Second),
	}, nil
}

// SetMedia прикрепляет загруженные файлы к событию, проверив в хранилище,
// что файлы есть, их тип и размер в пределах правил.
func (s *Service) SetMedia(ctx context.Context, organizerID, eventID string, in MediaInput) (Event, error) {
	if s.store == nil {
		return Event{}, errors.New("object store is not configured")
	}
	if err := s.checkObject(ctx, organizerID, eventID, MediaCoverImage, in.CoverImageKey, "cover_image_key"); err != nil {
		return Event{}, err
	}
	var video *string
	if in.CoverVideoKey != nil && *in.CoverVideoKey != "" {
		if err := s.checkObject(ctx, organizerID, eventID, MediaCoverVideo, *in.CoverVideoKey, "cover_video_key"); err != nil {
			return Event{}, err
		}
		video = in.CoverVideoKey
	}
	e, err := s.q.SetEventMedia(ctx, catalogdb.SetEventMediaParams{
		OrganizerID: organizerID, ID: eventID, CoverImageKey: &in.CoverImageKey, CoverVideoKey: video,
	})
	if err != nil {
		return Event{}, notFound(err, "set event media")
	}
	return eventFrom(e), nil
}

func (s *Service) checkObject(ctx context.Context, organizerID, eventID, kind, key, field string) error {
	// Ключ должен быть выдан для этого события и этого вида медиа:
	// иначе можно прикрепить чужой файл.
	if !strings.HasPrefix(key, mediaPrefix(organizerID, eventID, kind)) {
		return &ValidationError{Field: field, Message: "must be a key issued by an upload request for this event"}
	}
	info, err := s.store.Stat(ctx, key)
	if errors.Is(err, ErrObjectNotFound) {
		return &ValidationError{Field: field, Message: "file is not uploaded yet"}
	}
	if err != nil {
		return fmt.Errorf("stat %s: %w", field, err)
	}
	rule := mediaRules[kind]
	if _, ok := rule.types[info.ContentType]; !ok || info.Size > rule.maxBytes {
		return &ValidationError{Field: field, Message: "uploaded file has a wrong type or is too large"}
	}
	return nil
}

func mediaPrefix(organizerID, eventID, kind string) string {
	return "organizers/" + organizerID + "/events/" + eventID + "/" + kind + "/"
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
