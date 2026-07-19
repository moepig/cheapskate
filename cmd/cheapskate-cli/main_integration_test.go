//go:build integration

package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"

	"cheapskate/internal/emutest"
	"cheapskate/internal/model"
	"cheapskate/internal/store"
)

func setup(t *testing.T) (*store.Store, string) {
	cfg := emutest.Config(t)
	table := emutest.CreateStateTable(t, cfg)
	return store.New(dynamodb.NewFromConfig(cfg), table), table
}

func TestPinScheduleDisableRemoveLifecycle(t *testing.T) {
	s, table := setup(t)
	ctx := context.Background()
	args := func(a ...string) []string { return append([]string{"-table", table}, a...) }

	if err := run(args("pin", "rds-cluster#dev-aurora", "stopped")); err != nil {
		t.Fatal(err)
	}
	cfg, err := s.GetConfig(ctx, "rds-cluster#dev-aurora")
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil || cfg.Mode != model.ModePinned || cfg.Desired != model.DesiredStopped || cfg.Type != model.TypeRdsCluster {
		t.Fatalf("config after pin: %+v", cfg)
	}

	err = run(args("schedule", "ecs#dev/api", "-start", "0 9 * * MON-FRI", "-stop", "0 20 * * MON-FRI",
		"-timezone", "Asia/Tokyo", "-restore-count", "2"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err = s.GetConfig(ctx, "ecs#dev/api")
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil || cfg.Mode != model.ModeSchedule || cfg.StartCron != "0 9 * * MON-FRI" ||
		cfg.RestoreCount == nil || *cfg.RestoreCount != 2 {
		t.Fatalf("config after schedule: %+v", cfg)
	}

	if err := run(args("disable", "ecs#dev/api")); err != nil {
		t.Fatal(err)
	}
	cfg, err = s.GetConfig(ctx, "ecs#dev/api")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != model.ModeDisabled || cfg.StartCron != "0 9 * * MON-FRI" {
		t.Fatalf("disable must keep other fields: %+v", cfg)
	}

	if err := run(args("remove", "ecs#dev/api")); err != nil {
		t.Fatal(err)
	}
	cfg, err = s.GetConfig(ctx, "ecs#dev/api")
	if err != nil {
		t.Fatal(err)
	}
	if cfg != nil {
		t.Fatalf("config after remove: %+v", cfg)
	}
}

func TestOverrideLifecycle(t *testing.T) {
	s, table := setup(t)
	ctx := context.Background()
	args := func(a ...string) []string { return append([]string{"-table", table}, a...) }

	// An override on an unregistered resource must be rejected.
	if err := run(args("override", "rds-instance#nope", "running", "-for", "2h")); err == nil {
		t.Fatal("want error for override without config")
	}

	if err := run(args("pin", "rds-instance#dev-db", "stopped")); err != nil {
		t.Fatal(err)
	}
	if err := run(args("override", "rds-instance#dev-db", "running", "-for", "2h")); err != nil {
		t.Fatal(err)
	}
	o, err := s.GetOverride(ctx, "rds-instance#dev-db", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if o == nil || o.Desired != model.DesiredRunning {
		t.Fatalf("override: %+v", o)
	}
	remaining := time.Until(time.Unix(o.ExpiresAt, 0))
	if remaining < 110*time.Minute || remaining > 130*time.Minute {
		t.Fatalf("expires_at not ~2h out: %v", remaining)
	}

	if err := run(args("override", "rds-instance#dev-db", "-clear")); err != nil {
		t.Fatal(err)
	}
	o, err = s.GetOverride(ctx, "rds-instance#dev-db", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if o != nil {
		t.Fatalf("override after clear: %+v", o)
	}
}

func TestValidationErrors(t *testing.T) {
	_, table := setup(t)
	args := func(a ...string) []string { return append([]string{"-table", table}, a...) }

	cases := [][]string{
		args("pin", "sqs#queue", "stopped"),                         // unknown resource type
		args("pin", "rds-instance#db", "on"),                        // bad desired
		args("schedule", "rds-instance#db"),                         // no crons
		args("schedule", "rds-instance#db", "-start", "not a cron"), // invalid cron
		args("schedule", "rds-instance#db", "-start", "0 9 * * *", "-timezone", "Not/AZone"),
		args("schedule", "rds-instance#db", "-start", "0 9 * * *", "-restore-count", "2"), // non-ECS
		args("disable", "rds-instance#unregistered"),
	}
	for _, c := range cases {
		if err := run(c); err == nil {
			t.Errorf("want error for: %s", strings.Join(c, " "))
		}
	}
}
