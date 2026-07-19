//go:build integration

package main

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cheapskate/internal/emutest"
	"cheapskate/internal/model"
	"cheapskate/internal/store"
)

func setup(t *testing.T) (*store.Store, string) {
	cfg := emutest.Config(t)
	table := emutest.CreateStateTable(t, cfg)
	return store.New(dynamodb.NewFromConfig(cfg), table), table
}

func TestAddPinScheduleDisableRemoveLifecycle(t *testing.T) {
	s, table := setup(t)
	ctx := context.Background()
	args := func(a ...string) []string { return append([]string{"-table", table}, a...) }

	require.NoError(t, run(args("add", "--tag", "dev", "--type", "rds-cluster", "--name", "dev-aurora")))
	require.NoError(t, run(args("pin", "--tag", "dev", "stopped")))
	tag, err := s.GetTag(ctx, "dev")
	require.NoError(t, err)
	require.NotNil(t, tag)
	assert.Equal(t, model.ModePinned, tag.Mode)
	assert.Equal(t, model.DesiredStopped, tag.Desired)
	member, err := s.GetMember(ctx, "rds-cluster#dev-aurora")
	require.NoError(t, err)
	require.NotNil(t, member)
	assert.Equal(t, model.TypeRdsCluster, member.Type)

	require.NoError(t, run(args("add", "--tag", "dev", "--type", "ecs", "--cluster", "dev", "--service", "api", "-restore-count", "2")))
	member, err = s.GetMember(ctx, "ecs#dev/api")
	require.NoError(t, err)
	require.NotNil(t, member)
	require.NotNil(t, member.RestoreCount)
	assert.Equal(t, int32(2), *member.RestoreCount)

	require.NoError(t, run(args("schedule", "--tag", "dev", "-start", "0 9 * * MON-FRI", "-stop", "0 20 * * MON-FRI", "-timezone", "Asia/Tokyo")))
	tag, err = s.GetTag(ctx, "dev")
	require.NoError(t, err)
	require.NotNil(t, tag)
	assert.Equal(t, model.ModeSchedule, tag.Mode)
	assert.Equal(t, "0 9 * * MON-FRI", tag.StartCron)

	require.NoError(t, run(args("disable", "--tag", "dev")))
	tag, err = s.GetTag(ctx, "dev")
	require.NoError(t, err)
	require.NotNil(t, tag)
	assert.Equal(t, model.ModeDisabled, tag.Mode)
	assert.Equal(t, "0 9 * * MON-FRI", tag.StartCron, "disable must keep other fields")

	require.NoError(t, run(args("remove", "--tag", "dev", "--type", "ecs", "--cluster", "dev", "--service", "api")))
	member, err = s.GetMember(ctx, "ecs#dev/api")
	require.NoError(t, err)
	assert.Nil(t, member, "removed member must be gone")
	tag, err = s.GetTag(ctx, "dev")
	require.NoError(t, err)
	assert.NotNil(t, tag, "removing one member must not remove the tag")

	require.NoError(t, run(args("remove", "--tag", "dev")))
	tag, err = s.GetTag(ctx, "dev")
	require.NoError(t, err)
	assert.Nil(t, tag, "remove without resource flags must remove the whole tag")
}

func TestOverrideLifecycle(t *testing.T) {
	s, table := setup(t)
	ctx := context.Background()
	args := func(a ...string) []string { return append([]string{"-table", table}, a...) }

	// An override on an unregistered tag must be rejected.
	err := run(args("override", "--tag", "ghost", "running", "-for", "2h"))
	require.Error(t, err, "want error for override without a tag")

	require.NoError(t, run(args("add", "--tag", "dev", "--type", "rds-instance", "--name", "dev-db")))
	require.NoError(t, run(args("pin", "--tag", "dev", "stopped")))
	require.NoError(t, run(args("override", "--tag", "dev", "running", "-for", "2h")))

	o, err := s.GetOverride(ctx, "dev", time.Now())
	require.NoError(t, err)
	require.NotNil(t, o)
	assert.Equal(t, model.DesiredRunning, o.Desired)
	remaining := time.Until(time.Unix(o.ExpiresAt, 0))
	assert.InDelta(t, 2*time.Hour, remaining, float64(10*time.Minute), "expires_at not ~2h out")

	require.NoError(t, run(args("override", "--tag", "dev", "-clear")))
	o, err = s.GetOverride(ctx, "dev", time.Now())
	require.NoError(t, err)
	assert.Nil(t, o, "override after clear")
}

func TestValidationErrors(t *testing.T) {
	_, table := setup(t)
	args := func(a ...string) []string { return append([]string{"-table", table}, a...) }

	cases := map[string][]string{
		"unknown resource type":         args("add", "--tag", "dev", "--type", "sqs", "--name", "queue"),
		"bad desired":                   args("pin", "--tag", "dev", "on"),
		"no crons":                      args("schedule", "--tag", "dev"),
		"invalid cron":                  args("schedule", "--tag", "dev", "-start", "not a cron"),
		"invalid timezone":              args("schedule", "--tag", "dev", "-start", "0 9 * * *", "-timezone", "Not/AZone"),
		"restore-count on non-ecs":      args("add", "--tag", "dev", "--type", "rds-instance", "--name", "db", "-restore-count", "2"),
		"disable unregistered tag":      args("disable", "--tag", "unregistered"),
		"cluster without service":       args("add", "--tag", "dev", "--type", "ecs", "--cluster", "c"),
		"name with ecs type":            args("add", "--tag", "dev", "--type", "ecs", "--name", "n", "--cluster", "c", "--service", "s"),
		"cluster/service with rds type": args("add", "--tag", "dev", "--type", "rds-instance", "--name", "db", "--cluster", "c"),
	}
	for desc, c := range cases {
		assert.Errorf(t, run(c), "want error for: %s", desc)
	}
}
