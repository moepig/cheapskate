package reconcile

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"cheapskate/internal/mocks"
)

func TestSnsNotifierNoopWithoutTopicArn(t *testing.T) {
	ctrl := gomock.NewController(t)
	client := mocks.NewMockSnsAPI(ctrl) // no EXPECT: empty TopicArn must be a no-op
	n := &SnsNotifier{Client: client, TopicArn: ""}

	require.NoError(t, n.Publish(context.Background(), "subject", map[string]any{"a": 1}))
}

func TestSnsNotifierPublishesJsonPayload(t *testing.T) {
	ctrl := gomock.NewController(t)
	client := mocks.NewMockSnsAPI(ctrl)
	n := &SnsNotifier{Client: client, TopicArn: "arn:aws:sns:us-east-1:123:topic"}
	var published *sns.PublishInput
	client.EXPECT().Publish(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, in *sns.PublishInput, _ ...func(*sns.Options)) (*sns.PublishOutput, error) {
			published = in
			return &sns.PublishOutput{}, nil
		})

	require.NoError(t, n.Publish(context.Background(), "subject", map[string]any{"resource_id": "ecs#a/b"}))
	require.NotNil(t, published)
	assert.Contains(t, *published.Message, `"resource_id":"ecs#a/b"`)
	assert.Equal(t, n.TopicArn, *published.TopicArn)
}

// A-3/B-7: SNS Subject must be printable ASCII and <=100 chars, or Publish itself fails with InvalidParameter. 99-byte truncation of a UTF-8 string can split a multi-byte rune; sanitizeSnsSubject must not do that.
func TestSanitizeSnsSubjectTruncatesAtLimit(t *testing.T) {
	long := strings.Repeat("a", 150)
	got := sanitizeSnsSubject(long)
	assert.Len(t, got, snsSubjectMaxLen)
}

func TestSanitizeSnsSubjectReplacesNonAscii(t *testing.T) {
	got := sanitizeSnsSubject("[cheapskate] stop: ecs#日本語-service")
	for i, r := range got {
		require.Falsef(t, r > 0x7e || r < 0x20, "non-ASCII/control byte survived at %d in %q", i, got)
	}
	assert.Contains(t, got, "ecs#", "ASCII portion must survive")
}

func TestSanitizeSnsSubjectTruncationDoesNotSplitMultibyteRune(t *testing.T) {
	// Every multi-byte rune becomes a single '?' byte before truncation, so truncating at a byte boundary is always safe — there is nothing multi-byte left to split.
	subject := strings.Repeat("a", 95) + "日本語超過分"
	got := sanitizeSnsSubject(subject)
	require.True(t, utf8.ValidString(got), "truncated subject is not valid UTF-8: %q", got)
	assert.LessOrEqual(t, len(got), snsSubjectMaxLen)
}

func TestSubjectTruncatedBeforePublish(t *testing.T) {
	ctrl := gomock.NewController(t)
	client := mocks.NewMockSnsAPI(ctrl)
	n := &SnsNotifier{Client: client, TopicArn: "arn:aws:sns:us-east-1:123:topic"}
	long := "[cheapskate] error: " + strings.Repeat("x", 200)
	var published *sns.PublishInput
	client.EXPECT().Publish(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, in *sns.PublishInput, _ ...func(*sns.Options)) (*sns.PublishOutput, error) {
			published = in
			return &sns.PublishOutput{}, nil
		})

	require.NoError(t, n.Publish(context.Background(), long, map[string]any{}))
	require.NotNil(t, published)
	assert.LessOrEqual(t, len(*published.Subject), snsSubjectMaxLen)
}
