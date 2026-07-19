package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResourceIDType(t *testing.T) {
	cases := map[string]string{
		"rds-instance#db":    TypeRdsInstance,
		"rds-cluster#aurora": TypeRdsCluster,
		"ecs#cluster/svc":    TypeEcsService,
	}
	for id, want := range cases {
		got, err := ResourceIDType(id)
		require.NoErrorf(t, err, "%s", id)
		assert.Equal(t, want, got, id)
	}
	for _, id := range []string{"nodelimiter", "sqs#queue", "#db"} {
		_, err := ResourceIDType(id)
		assert.Errorf(t, err, "want error for %q", id)
	}
}

func TestValidTagName(t *testing.T) {
	for _, name := range []string{"dev", "dev-1", "dev_1", "dev.1", "A", "a23456789012345678901234567890123456789012345678901234567890123"} {
		assert.NoErrorf(t, ValidTagName(name), "%q should be valid", name)
	}
	for _, name := range []string{"", "-dev", ".dev", "dev#1", "dev/1", "dev 1", "日本語"} {
		assert.Errorf(t, ValidTagName(name), "%q should be invalid", name)
	}
}

func TestParseTagValid(t *testing.T) {
	tag, err := ParseTag(TagItem{PK: "tag#dev", Mode: ModePinned, Desired: DesiredStopped})
	require.NoError(t, err)
	assert.Equal(t, "dev", tag.Name)
	assert.Equal(t, ModePinned, tag.Mode)
	assert.Equal(t, DesiredStopped, tag.Desired)
}

func TestParseTagDefaultsToDisabled(t *testing.T) {
	tag, err := ParseTag(TagItem{PK: "tag#dev"})
	require.NoError(t, err)
	assert.Equal(t, ModeDisabled, tag.Mode)
}

func TestParseTagScheduleFields(t *testing.T) {
	tag, err := ParseTag(TagItem{PK: "tag#dev", Mode: ModeSchedule, StartCron: "0 9 * * 1-5", StopCron: "0 21 * * 1-5", Timezone: "Asia/Tokyo"})
	require.NoError(t, err)
	assert.Equal(t, "0 9 * * 1-5", tag.StartCron)
	assert.Equal(t, "0 21 * * 1-5", tag.StopCron)
	assert.Equal(t, "Asia/Tokyo", tag.Timezone)
}

func TestParseTagRejects(t *testing.T) {
	cases := []TagItem{
		{PK: "config#dev", Mode: ModePinned, Desired: DesiredStopped}, // wrong prefix
		{PK: "tag#-bad", Mode: ModePinned, Desired: DesiredStopped},   // invalid tag name
		{PK: "tag#dev", Mode: "sometimes"},                            // unknown mode
		{PK: "tag#dev", Mode: ModePinned},                             // pinned without desired
		{PK: "tag#dev", Mode: ModePinned, Desired: "on"},              // bad desired
	}
	for _, item := range cases {
		_, err := ParseTag(item)
		assert.Errorf(t, err, "want error for %+v", item)
	}
}

func TestParseMemberValid(t *testing.T) {
	m, err := ParseMember(MemberItem{PK: "member#rds-cluster#dev-aurora", Tag: "dev", Type: TypeRdsCluster})
	require.NoError(t, err)
	assert.Equal(t, "rds-cluster#dev-aurora", m.ResourceID)
	assert.Equal(t, "dev-aurora", m.Ref())
	assert.Equal(t, "dev", m.Tag)
}

func TestParseMemberEcsRef(t *testing.T) {
	m, err := ParseMember(MemberItem{PK: "member#ecs#dev-cluster/api", Tag: "dev", Type: TypeEcsService})
	require.NoError(t, err)
	assert.Equal(t, "dev-cluster/api", m.Ref())
}

func TestParseMemberRejects(t *testing.T) {
	cases := []MemberItem{
		{PK: "tag#rds-instance#db", Tag: "dev", Type: TypeRdsInstance},     // wrong prefix
		{PK: "member#nodelimiter", Tag: "dev", Type: TypeRdsInstance},      // malformed resource_id
		{PK: "member#rds-instance#db", Tag: "dev", Type: TypeEcsService},   // type mismatch
		{PK: "member#rds-instance#db", Tag: "-bad", Type: TypeRdsInstance}, // invalid tag name
	}
	for _, item := range cases {
		_, err := ParseMember(item)
		assert.Errorf(t, err, "want error for %+v", item)
	}
}
