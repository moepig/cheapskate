package model

import "testing"

func TestParseConfigValid(t *testing.T) {
	cfg, err := ParseConfig(ConfigItem{
		PK:      "config#rds-cluster#dev-aurora",
		Type:    TypeRdsCluster,
		Mode:    ModePinned,
		Desired: DesiredStopped,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ResourceID != "rds-cluster#dev-aurora" {
		t.Fatalf("resource_id: %q", cfg.ResourceID)
	}
	if cfg.Ref() != "dev-aurora" {
		t.Fatalf("ref: %q", cfg.Ref())
	}
}

func TestParseConfigEcsRef(t *testing.T) {
	cfg, err := ParseConfig(ConfigItem{
		PK:   "config#ecs#dev-cluster/api",
		Type: TypeEcsService,
		Mode: ModeSchedule,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Ref() != "dev-cluster/api" {
		t.Fatalf("ref: %q", cfg.Ref())
	}
}

func TestParseConfigDefaultsToDisabled(t *testing.T) {
	cfg, err := ParseConfig(ConfigItem{PK: "config#rds-instance#db", Type: TypeRdsInstance})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != ModeDisabled {
		t.Fatalf("mode: %q", cfg.Mode)
	}
}

func TestParseConfigRejects(t *testing.T) {
	cases := []ConfigItem{
		{PK: "status#rds-instance#db", Type: TypeRdsInstance, Mode: ModePinned, Desired: DesiredStopped}, // wrong prefix
		{PK: "config#rds-instance", Type: TypeRdsInstance, Mode: ModePinned, Desired: DesiredStopped},    // no identifier
		{PK: "config#rds-instance#db", Type: "sqs-queue", Mode: ModePinned, Desired: DesiredStopped},     // unknown type
		{PK: "config#rds-instance#db", Type: TypeRdsInstance, Mode: "sometimes"},                         // unknown mode
		{PK: "config#rds-instance#db", Type: TypeRdsInstance, Mode: ModePinned},                          // pinned without desired
		{PK: "config#rds-instance#db", Type: TypeRdsInstance, Mode: ModePinned, Desired: "on"},           // bad desired
	}
	for _, item := range cases {
		if _, err := ParseConfig(item); err == nil {
			t.Errorf("want error for %+v", item)
		}
	}
}

func TestResourceIDType(t *testing.T) {
	cases := map[string]string{
		"rds-instance#db":    TypeRdsInstance,
		"rds-cluster#aurora": TypeRdsCluster,
		"ecs#cluster/svc":    TypeEcsService,
	}
	for id, want := range cases {
		got, err := ResourceIDType(id)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if got != want {
			t.Errorf("%s: want %s, got %s", id, want, got)
		}
	}
	for _, id := range []string{"nodelimiter", "sqs#queue", "#db"} {
		if _, err := ResourceIDType(id); err == nil {
			t.Errorf("want error for %q", id)
		}
	}
}
