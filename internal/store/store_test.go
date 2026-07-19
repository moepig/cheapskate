package store

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"cheapskate/internal/dynafake"
	"cheapskate/internal/model"
)

func s(v string) types.AttributeValue { return &types.AttributeValueMemberS{Value: v} }

func seedConfig(f *dynafake.Fake, resourceID, typ, mode, desired string) {
	f.Seed(map[string]types.AttributeValue{
		"pk": s(model.ConfigPrefix + resourceID), "type": s(typ), "mode": s(mode), "desired": s(desired),
	})
}

// A-6: ListConfigs must page through Scan via LastEvaluatedKey rather than assuming one page, since a real table's Scan is capped at 1MB per call.
func TestListConfigsPagesThroughScan(t *testing.T) {
	f := dynafake.New()
	f.SetScanPageSize(1)
	seedConfig(f, "rds-instance#a", model.TypeRdsInstance, model.ModePinned, model.DesiredStopped)
	seedConfig(f, "rds-instance#b", model.TypeRdsInstance, model.ModePinned, model.DesiredStopped)
	seedConfig(f, "rds-instance#c", model.TypeRdsInstance, model.ModePinned, model.DesiredStopped)

	items, err := New(f, "t").ListConfigs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("got %d items across pages, want 3: %+v", len(items), items)
	}
}

func TestScanAllJoinsConfigOverrideStatus(t *testing.T) {
	f := dynafake.New()
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	seedConfig(f, "rds-instance#a", model.TypeRdsInstance, model.ModePinned, model.DesiredStopped)
	f.Seed(map[string]types.AttributeValue{
		"pk": s(model.OverridePrefix + "rds-instance#a"), "desired": s(model.DesiredRunning),
		"expires_at": &types.AttributeValueMemberN{Value: "9999999999"},
	})
	f.Seed(map[string]types.AttributeValue{
		"pk": s(model.StatusPrefix + "rds-instance#a"), "last_action": s("stop"),
	})

	rows, err := New(f, "t").ScanAll(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows: %+v", rows)
	}
	r := rows[0]
	if !r.HasConfig || r.Override == nil || r.Override.Desired != model.DesiredRunning || r.Status.LastAction != "stop" {
		t.Fatalf("row not joined correctly: %+v", r)
	}
	if r.Err != nil {
		t.Fatalf("unexpected error: %v", r.Err)
	}
}

// A-6/C-1: ScanAll must page through Scan just like ListConfigs.
func TestScanAllPagesThroughScan(t *testing.T) {
	f := dynafake.New()
	f.SetScanPageSize(1)
	seedConfig(f, "rds-instance#a", model.TypeRdsInstance, model.ModePinned, model.DesiredStopped)
	seedConfig(f, "rds-instance#b", model.TypeRdsInstance, model.ModePinned, model.DesiredStopped)

	rows, err := New(f, "t").ScanAll(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows across pages, want 2: %+v", len(rows), rows)
	}
}

// B-5: a malformed override item for one resource_id must not prevent other resources from being listed, and must surface as that row's Err instead of aborting the scan.
func TestScanAllRecordsPerRowErrorForMalformedOverride(t *testing.T) {
	f := dynafake.New()
	now := time.Now()
	seedConfig(f, "rds-instance#broken", model.TypeRdsInstance, model.ModePinned, model.DesiredStopped)
	f.Seed(map[string]types.AttributeValue{
		"pk": s(model.OverridePrefix + "rds-instance#broken"), "desired": s("not-a-valid-state"),
		"expires_at": &types.AttributeValueMemberN{Value: "9999999999"},
	})
	seedConfig(f, "rds-instance#fine", model.TypeRdsInstance, model.ModePinned, model.DesiredStopped)

	rows, err := New(f, "t").ScanAll(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows: %+v", rows)
	}
	byID := map[string]ScanRow{}
	for _, r := range rows {
		byID[r.ResourceID] = r
	}
	if byID["rds-instance#broken"].Err == nil {
		t.Fatal("malformed override must set Err on its row")
	}
	if !byID["rds-instance#broken"].HasConfig {
		t.Fatal("config must still be joined despite the bad override")
	}
	if byID["rds-instance#fine"].Err != nil {
		t.Fatalf("unrelated row must be unaffected: %+v", byID["rds-instance#fine"])
	}
}

func TestScanAllExpiredOverrideIsIgnored(t *testing.T) {
	f := dynafake.New()
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	seedConfig(f, "rds-instance#a", model.TypeRdsInstance, model.ModePinned, model.DesiredStopped)
	f.Seed(map[string]types.AttributeValue{
		"pk": s(model.OverridePrefix + "rds-instance#a"), "desired": s(model.DesiredRunning),
		"expires_at": &types.AttributeValueMemberN{Value: "1"}, // long past
	})

	rows, err := New(f, "t").ScanAll(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Override != nil {
		t.Fatalf("expired override must be ignored: %+v", rows[0])
	}
}

func TestScanAllSkipsOrphanedOverrideWithoutConfig(t *testing.T) {
	f := dynafake.New()
	now := time.Now()
	f.Seed(map[string]types.AttributeValue{
		"pk": s(model.OverridePrefix + "rds-instance#ghost"), "desired": s(model.DesiredRunning),
		"expires_at": &types.AttributeValueMemberN{Value: "9999999999"},
	})

	rows, err := New(f, "t").ScanAll(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].HasConfig {
		t.Fatalf("orphaned override must be reported without HasConfig: %+v", rows)
	}
}
