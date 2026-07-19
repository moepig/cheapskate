package ops

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"cheapskate/internal/dynafake"
	"cheapskate/internal/model"
	"cheapskate/internal/store"
)

func newFixture() (*dynafake.Fake, *store.Store) {
	f := dynafake.New()
	return f, store.New(f, "t")
}

func i32(v int32) *int32 { return &v }

var now = time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)

// A-4: pin -> schedule -> pin must round-trip the cron fields and restore_count instead of losing them, since Pin/Schedule each read-modify-write the shared config item.
func TestPinScheduleRoundTripPreservesAttributes(t *testing.T) {
	_, s := newFixture()
	ctx := context.Background()
	resourceID := "ecs#dev/api"

	if _, err := Schedule(ctx, s, resourceID, ScheduleSpec{StartCron: "0 9 * * *", StopCron: "0 18 * * *", RestoreCount: 3}); err != nil {
		t.Fatal(err)
	}
	if err := Pin(ctx, s, resourceID, model.DesiredStopped); err != nil {
		t.Fatal(err)
	}
	cfg, err := s.GetConfig(ctx, resourceID)
	if err != nil || cfg == nil {
		t.Fatalf("get after pin: %v %v", cfg, err)
	}
	if cfg.StartCron != "0 9 * * *" || cfg.StopCron != "0 18 * * *" {
		t.Fatalf("pin must preserve cron fields: %+v", cfg)
	}
	if cfg.RestoreCount == nil || *cfg.RestoreCount != 3 {
		t.Fatalf("pin must preserve restore_count: %+v", cfg)
	}

	// B-9: schedule again without -restore-count must keep the existing restore_count, not silently drop it.
	if _, err := Schedule(ctx, s, resourceID, ScheduleSpec{StartCron: "0 8 * * *", StopCron: "0 20 * * *"}); err != nil {
		t.Fatal(err)
	}
	cfg, err = s.GetConfig(ctx, resourceID)
	if err != nil || cfg == nil {
		t.Fatalf("get after re-schedule: %v %v", cfg, err)
	}
	if cfg.RestoreCount == nil || *cfg.RestoreCount != 3 {
		t.Fatalf("schedule without -restore-count must preserve it: %+v", cfg)
	}
	if cfg.StartCron != "0 8 * * *" || cfg.StopCron != "0 20 * * *" {
		t.Fatalf("schedule must still update cron fields: %+v", cfg)
	}
}

func TestScheduleWithRestoreCountOverwrites(t *testing.T) {
	_, s := newFixture()
	ctx := context.Background()
	resourceID := "ecs#dev/api"

	if _, err := Schedule(ctx, s, resourceID, ScheduleSpec{StartCron: "0 9 * * *", RestoreCount: 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := Schedule(ctx, s, resourceID, ScheduleSpec{StartCron: "0 9 * * *", RestoreCount: 5}); err != nil {
		t.Fatal(err)
	}
	cfg, err := s.GetConfig(ctx, resourceID)
	if err != nil || cfg == nil {
		t.Fatalf("get: %v %v", cfg, err)
	}
	if cfg.RestoreCount == nil || *cfg.RestoreCount != 5 {
		t.Fatalf("explicit restore_count must overwrite: %+v", cfg)
	}
}

// B-6: an override on an unregistered resource has no effect, so registration is rejected.
func TestSetOverrideRejectsUnregistered(t *testing.T) {
	_, s := newFixture()
	if _, err := SetOverride(context.Background(), s, "rds-instance#ghost", model.DesiredRunning, time.Hour, now); err == nil {
		t.Fatal("want error for unregistered resource")
	}
}

// B-6: disabled is a stronger stop than override (ReconcileOne skips disabled before ever reading the override), so registering one would silently do nothing; SetOverride must reject it instead.
func TestSetOverrideRejectsDisabled(t *testing.T) {
	f, s := newFixture()
	ctx := context.Background()
	resourceID := "rds-instance#db"
	if err := Pin(ctx, s, resourceID, model.DesiredStopped); err != nil {
		t.Fatal(err)
	}
	if err := Disable(ctx, s, resourceID); err != nil {
		t.Fatal(err)
	}

	if _, err := SetOverride(ctx, s, resourceID, model.DesiredRunning, time.Hour, now); err == nil {
		t.Fatal("want error overriding a disabled resource")
	}
	if got := f.Item("override#" + resourceID); got != nil {
		t.Fatal("rejected override must not be written")
	}
}

func TestSetOverrideAllowedWhenScheduledOrPinned(t *testing.T) {
	_, s := newFixture()
	ctx := context.Background()
	resourceID := "rds-instance#db"
	if err := Pin(ctx, s, resourceID, model.DesiredStopped); err != nil {
		t.Fatal(err)
	}
	if _, err := SetOverride(ctx, s, resourceID, model.DesiredRunning, time.Hour, now); err != nil {
		t.Fatalf("override on a pinned resource must be allowed: %v", err)
	}
}

// Remove must delete all three item kinds for the resource_id.
func TestRemoveDeletesAllThreePrefixes(t *testing.T) {
	f, s := newFixture()
	ctx := context.Background()
	resourceID := "rds-instance#db"
	if err := Pin(ctx, s, resourceID, model.DesiredStopped); err != nil {
		t.Fatal(err)
	}
	if _, err := SetOverride(ctx, s, resourceID, model.DesiredRunning, time.Hour, now); err != nil {
		t.Fatal(err)
	}
	if err := s.PutStatus(ctx, resourceID, map[string]any{"last_action": "stop"}); err != nil {
		t.Fatal(err)
	}

	if err := Remove(ctx, s, resourceID); err != nil {
		t.Fatal(err)
	}
	for _, pk := range []string{"config#" + resourceID, "override#" + resourceID, "status#" + resourceID} {
		if f.Item(pk) != nil {
			t.Fatalf("%s not deleted", pk)
		}
	}
}

// B-5: List must keep going and report the bad row's error instead of aborting the whole list when one config's override is malformed.
func TestListSurfacesPerRowErrorWithoutAbortingOthers(t *testing.T) {
	f, s := newFixture()
	ctx := context.Background()
	if err := Pin(ctx, s, "rds-instance#broken", model.DesiredStopped); err != nil {
		t.Fatal(err)
	}
	f.Seed(map[string]types.AttributeValue{
		"pk":         &types.AttributeValueMemberS{Value: "override#rds-instance#broken"},
		"desired":    &types.AttributeValueMemberS{Value: "not-a-valid-state"},
		"expires_at": &types.AttributeValueMemberN{Value: "9999999999"},
	})
	if err := Pin(ctx, s, "rds-instance#fine", model.DesiredStopped); err != nil {
		t.Fatal(err)
	}

	rows, err := List(ctx, s, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows: %+v", rows)
	}
	byID := map[string]Row{}
	for _, r := range rows {
		byID[r.ResourceID] = r
	}
	if byID["rds-instance#broken"].Err == nil {
		t.Fatal("malformed row must carry its error")
	}
	if byID["rds-instance#fine"].Err != nil {
		t.Fatalf("unrelated row must be unaffected: %+v", byID["rds-instance#fine"])
	}
}
