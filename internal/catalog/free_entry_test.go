package catalog

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// Событие со свободным входом: без схемы зала, цен и билетов, публикуется
// одна страница.
func TestFreeEntryEvent(t *testing.T) {
	e, cache, orgSlug := newPublicEnv(t)
	ctx := t.Context()
	e.svc.cache = cache

	in := e.input("open-air-" + suffix())
	in.Admission = AdmissionFreeEntry
	if _, err := e.svc.CreateEvent(ctx, e.org, in); !invalidField(err, "seat_map_id") {
		t.Fatalf("free entry with a seat map: err = %v, want invalid seat_map_id", err)
	}
	in.SeatMapID = ""
	ev, err := e.svc.CreateEvent(ctx, e.org, in)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Admission != AdmissionFreeEntry || ev.SeatMapID != nil {
		t.Fatalf("event = %+v", ev)
	}
	if _, err := e.svc.SetPrices(ctx, e.org, ev.ID, clonePrices(fullPrices)); !isPrecondition(err, "free_entry") {
		t.Errorf("SetPrices on free entry: err = %v, want free_entry", err)
	}

	// Без обложки не публикуется, как и любое событие.
	if _, err := e.svc.Publish(ctx, e.org, ev.ID, time.Now()); !isPrecondition(err, "cover_required") {
		t.Fatalf("publish without cover: err = %v", err)
	}
	up, err := e.svc.CreateUpload(ctx, e.org, ev.ID, UploadRequest{Kind: MediaCoverImage, ContentType: "image/png", Size: 100})
	if err != nil {
		t.Fatal(err)
	}
	e.store.put(up.Key, ObjectInfo{Size: 100, ContentType: "image/png"})
	if _, err := e.svc.SetMedia(ctx, e.org, ev.ID, MediaInput{CoverImageKey: up.Key}); err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.Publish(ctx, e.org, ev.ID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.Seats != 0 || res.Event.Status != "published" {
		t.Errorf("publish = %+v, want published without seats", res)
	}

	b, err := e.svc.GetPublicEvent(ctx, orgSlug, ev.Slug)
	if err != nil {
		t.Fatal(err)
	}
	var pe PublicEvent
	if err := json.Unmarshal(b, &pe); err != nil {
		t.Fatal(err)
	}
	if pe.Admission != AdmissionFreeEntry || pe.Layout != nil || len(pe.Prices) != 0 {
		t.Errorf("public page = admission %s, layout %v, prices %v", pe.Admission, pe.Layout, pe.Prices)
	}
}

func TestAdmissionValidation(t *testing.T) {
	e := newEventEnv(t)
	in := e.input("bad-" + suffix())
	in.Admission = "vip_only"
	if _, err := e.svc.CreateEvent(t.Context(), e.org, in); !invalidField(err, "admission") {
		t.Errorf("unknown admission: err = %v", err)
	}
	in = e.input("no-map-" + suffix())
	in.SeatMapID = ""
	if _, err := e.svc.CreateEvent(t.Context(), e.org, in); !invalidField(err, "seat_map_id") {
		t.Errorf("ticketed without seat map: err = %v", err)
	}
}

func invalidField(err error, field string) bool {
	v, ok := errors.AsType[*ValidationError](err)
	return ok && v.Field == field
}
