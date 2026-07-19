//go:build integration

package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"

	"cheapskate/internal/emutest"
	"cheapskate/internal/model"
	"cheapskate/internal/store"
)

func newStore(t *testing.T) *store.Store {
	cfg := emutest.Config(t)
	table := emutest.CreateStateTable(t, cfg)
	return store.New(dynamodb.NewFromConfig(cfg), table)
}

func TestListConfigsFiltersAndSorts(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	for _, item := range []model.ConfigItem{
		{PK: "config#rds-instance#b-db", Type: model.TypeRdsInstance, Mode: model.ModePinned, Desired: model.DesiredStopped},
		{PK: "config#rds-instance#a-db", Type: model.TypeRdsInstance, Mode: model.ModeDisabled},
	} {
		if err := s.PutConfig(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	// Non-config items must be excluded from the listing.
	if err := s.PutStatus(ctx, "rds-instance#a-db", map[string]any{"last_action": "stop"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutOverride(ctx, "rds-instance#a-db", model.Override{Desired: model.DesiredRunning, ExpiresAt: time.Now().Add(time.Hour).Unix()}); err != nil {
		t.Fatal(err)
	}

	items, err := s.ListConfigs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("want 2 configs, got %d", len(items))
	}
	if items[0].PK != "config#rds-instance#a-db" || items[1].PK != "config#rds-instance#b-db" {
		t.Fatalf("order: %s, %s", items[0].PK, items[1].PK)
	}
}

func TestOverrideExpiryEnforcedInCode(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	now := time.Now()

	if err := s.PutOverride(ctx, "rds-instance#db", model.Override{Desired: model.DesiredRunning, ExpiresAt: now.Add(time.Hour).Unix()}); err != nil {
		t.Fatal(err)
	}
	o, err := s.GetOverride(ctx, "rds-instance#db", now)
	if err != nil {
		t.Fatal(err)
	}
	if o == nil || o.Desired != model.DesiredRunning {
		t.Fatalf("unexpired override: %+v", o)
	}
	// TTL deletion is lazy; the store must treat a past expires_at as absent.
	o, err = s.GetOverride(ctx, "rds-instance#db", now.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if o != nil {
		t.Fatalf("expired override must be nil, got %+v", o)
	}
}

func TestStatusRoundtrip(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	err := s.PutStatus(ctx, "ecs#dev/api", map[string]any{
		"last_action":         "stop",
		"saved_desired_count": int32(3),
		"saved_scaling_min":   int32(2),
		"ignored":             nil, // nil attrs must be skipped, not written
	})
	if err != nil {
		t.Fatal(err)
	}
	// A second partial update must merge, not replace.
	if err := s.PutStatus(ctx, "ecs#dev/api", map[string]any{"last_action": "start"}); err != nil {
		t.Fatal(err)
	}

	status, err := s.GetStatus(ctx, "ecs#dev/api")
	if err != nil {
		t.Fatal(err)
	}
	if status.LastAction != "start" {
		t.Fatalf("last_action: %q", status.LastAction)
	}
	if status.SavedDesiredCount == nil || *status.SavedDesiredCount != 3 {
		t.Fatalf("saved_desired_count: %v", status.SavedDesiredCount)
	}
	if status.SavedScalingMin == nil || *status.SavedScalingMin != 2 {
		t.Fatalf("saved_scaling_min: %v", status.SavedScalingMin)
	}
}
