package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/funster-a/dd/internal/catalog/catalogdb"
)

// Cache — кэш публичных страниц (Redis). Объявлен на стороне потребителя.
type Cache interface {
	// Get возвращает значение; ok = false — ключа нет.
	Get(ctx context.Context, key string) (value []byte, ok bool, err error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	Delete(ctx context.Context, keys ...string) error
}

// publicTTL — срок кэша страницы. Страница опубликованного события почти
// не меняется (схема и цены зафиксированы), а изменения медиа сбрасывают
// кэш сразу; срок лишь ограничивает жизнь случайно устаревшей записи.
const publicTTL = 10 * time.Minute

// PublicEvent — публичная страница события: карточка, площадка, цены и
// схема зала. Наличие свободных мест сюда не входит: оно меняется каждую
// секунду и отдаётся отдельно (модуль booking).
type PublicEvent struct {
	// ID — идентификатор для бронирования и занятости мест (модуль booking).
	ID                  string          `json:"id"`
	OrganizerSlug       string          `json:"organizer_slug"`
	OrganizerName       string          `json:"organizer_name"`
	Slug                string          `json:"slug"`
	Title               string          `json:"title"`
	Description         string          `json:"description"`
	AgeRating           string          `json:"age_rating"`
	StartsAt            time.Time       `json:"starts_at"`
	EndsAt              time.Time       `json:"ends_at"`
	SalesStartAt        *time.Time      `json:"sales_start_at"`
	SalesEndAt          *time.Time      `json:"sales_end_at"`
	MaxTicketsPerBuyer  int32           `json:"max_tickets_per_buyer"`
	RefundDeadlineHours int32           `json:"refund_deadline_hours"`
	CoverImageURL       string          `json:"cover_image_url"`
	CoverVideoURL       *string         `json:"cover_video_url"`
	Venue               PublicVenue     `json:"venue"`
	Prices              []PriceCategory `json:"prices"`
	Layout              Layout          `json:"layout"`
}

// PublicVenue — площадка на публичной странице.
type PublicVenue struct {
	Name      string   `json:"name"`
	Address   string   `json:"address"`
	Timezone  string   `json:"timezone"`
	Latitude  *float64 `json:"latitude"`
	Longitude *float64 `json:"longitude"`
}

// GetPublicEvent отдаёт публичную страницу опубликованного события из кэша,
// а при промахе строит её из базы. Одновременные промахи по одному событию
// (старт продаж, тысячи открытий сразу) идут в базу одним запросом:
// остальные ждут его результат (singleflight).
func (s *Service) GetPublicEvent(ctx context.Context, organizerSlug, eventSlug string) ([]byte, error) {
	key := publicKey(organizerSlug, eventSlug)
	if s.cache != nil {
		if b, ok, err := s.cache.Get(ctx, key); err == nil && ok {
			return b, nil
		}
		// Ошибка кэша не мешает отдать страницу из базы.
	}

	v, err, _ := s.group.Do(key, func() (any, error) {
		// Контекст первого запроса мог отмениться; остальные ждут этот же результат.
		ctx := context.WithoutCancel(ctx)
		s.publicLoads.Add(1)
		b, err := s.buildPublicEvent(ctx, organizerSlug, eventSlug)
		if err != nil {
			return nil, err
		}
		if s.cache != nil {
			_ = s.cache.Set(ctx, key, b, publicTTL)
		}
		return b, nil
	})
	if err != nil {
		return nil, err
	}
	return v.([]byte), nil
}

func (s *Service) buildPublicEvent(ctx context.Context, organizerSlug, eventSlug string) ([]byte, error) {
	e, err := s.q.GetPublishedEvent(ctx, catalogdb.GetPublishedEventParams{OrganizerSlug: organizerSlug, EventSlug: eventSlug})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load public event: %w", err)
	}
	var layout Layout
	if err := json.Unmarshal(e.SeatMapLayout, &layout); err != nil {
		return nil, fmt.Errorf("decode layout: %w", err)
	}
	prices, err := s.GetPrices(ctx, e.OrganizerID, e.ID)
	if err != nil {
		return nil, err
	}
	pe := PublicEvent{
		ID:            e.ID,
		OrganizerSlug: e.OrganizerSlug, OrganizerName: e.OrganizerName,
		Slug: e.Slug, Title: e.Title, Description: e.Description, AgeRating: e.AgeRating,
		StartsAt: e.StartsAt, EndsAt: e.EndsAt, SalesStartAt: e.SalesStartAt, SalesEndAt: e.SalesEndAt,
		MaxTicketsPerBuyer: e.MaxTicketsPerBuyer, RefundDeadlineHours: e.RefundDeadlineHours,
		Venue: PublicVenue{
			Name: e.VenueName, Address: e.VenueAddress, Timezone: e.VenueTimezone,
			Latitude: e.VenueLatitude, Longitude: e.VenueLongitude,
		},
		Prices: prices, Layout: layout,
	}
	if s.store != nil && e.CoverImageKey != nil {
		pe.CoverImageURL = s.store.URL(*e.CoverImageKey)
		if e.CoverVideoKey != nil {
			u := s.store.URL(*e.CoverVideoKey)
			pe.CoverVideoURL = &u
		}
	}
	return json.Marshal(pe)
}

// invalidatePublic сбрасывает кэш страницы события после изменения.
func (s *Service) invalidatePublic(ctx context.Context, organizerID, eventSlug string) {
	if s.cache == nil {
		return
	}
	orgSlug, err := s.q.GetOrganizerSlug(ctx, organizerID)
	if err != nil {
		return
	}
	_ = s.cache.Delete(ctx, publicKey(orgSlug, eventSlug))
}

func publicKey(organizerSlug, eventSlug string) string {
	return "catalog:public-event:" + organizerSlug + "/" + eventSlug
}
