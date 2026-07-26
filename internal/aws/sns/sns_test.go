package sns

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	awssns "github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"cheapskate/internal/aws/sns/mocks"
)

func TestSnsNotifierNoopWithoutTopicArn(t *testing.T) {
	ctrl := gomock.NewController(t)
	client := mocks.NewMockAPI(ctrl) // TopicArn が空なら何もしないため EXPECT はない
	n := &Notifier{Client: client, TopicArn: ""}

	require.NoError(t, n.Publish(context.Background(), "subject", map[string]any{"a": 1}))
}

func TestSnsNotifierPublishesJsonPayload(t *testing.T) {
	ctrl := gomock.NewController(t)
	client := mocks.NewMockAPI(ctrl)
	n := &Notifier{Client: client, TopicArn: "arn:aws:sns:us-east-1:123:topic"}
	var published *awssns.PublishInput
	client.EXPECT().Publish(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, in *awssns.PublishInput, _ ...func(*awssns.Options)) (*awssns.PublishOutput, error) {
			published = in
			return &awssns.PublishOutput{}, nil
		})

	require.NoError(t, n.Publish(context.Background(), "subject", map[string]any{"resource_id": "ecs#a/b"}))
	require.NotNil(t, published)
	assert.Contains(t, *published.Message, `"resource_id":"ecs#a/b"`)
	assert.Equal(t, n.TopicArn, *published.TopicArn)
}

// SNS の Subject は印字可能な ASCII かつ 100 文字以内でなければ、Publish 自体が InvalidParameter で失敗する
// UTF-8 文字列を 99 バイトで切るとマルチバイト文字を分断しうるので、sanitizeSubject はそうしてはならない
func TestSanitizeSnsSubjectTruncatesAtLimit(t *testing.T) {
	long := strings.Repeat("a", 150)
	got := sanitizeSubject(long)
	assert.Len(t, got, subjectMaxLen)
}

func TestSanitizeSnsSubjectReplacesNonAscii(t *testing.T) {
	got := sanitizeSubject("[cheapskate] stop: ecs#日本語-service")
	for i, r := range got {
		require.Falsef(t, r > 0x7e || r < 0x20, "non-ASCII/control byte survived at %d in %q", i, got)
	}
	assert.Contains(t, got, "ecs#", "ASCII portion must survive")
}

func TestSanitizeSnsSubjectTruncationDoesNotSplitMultibyteRune(t *testing.T) {
	// 切り詰めの前にマルチバイト文字はすべて 1 バイトの '?' になる
	// 分断すべきマルチバイト文字が残らないため、バイト境界での切り詰めは常に安全である
	subject := strings.Repeat("a", 95) + "日本語超過分"
	got := sanitizeSubject(subject)
	require.True(t, utf8.ValidString(got), "truncated subject is not valid UTF-8: %q", got)
	assert.LessOrEqual(t, len(got), subjectMaxLen)
}

func TestSubjectTruncatedBeforePublish(t *testing.T) {
	ctrl := gomock.NewController(t)
	client := mocks.NewMockAPI(ctrl)
	n := &Notifier{Client: client, TopicArn: "arn:aws:sns:us-east-1:123:topic"}
	long := "[cheapskate] error: " + strings.Repeat("x", 200)
	var published *awssns.PublishInput
	client.EXPECT().Publish(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, in *awssns.PublishInput, _ ...func(*awssns.Options)) (*awssns.PublishOutput, error) {
			published = in
			return &awssns.PublishOutput{}, nil
		})

	require.NoError(t, n.Publish(context.Background(), long, map[string]any{}))
	require.NotNil(t, published)
	assert.LessOrEqual(t, len(*published.Subject), subjectMaxLen)
}
