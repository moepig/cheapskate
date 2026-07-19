package reconcile

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/service/sns"
)

type fakeSns struct {
	published []sns.PublishInput
}

func (f *fakeSns) Publish(_ context.Context, in *sns.PublishInput, _ ...func(*sns.Options)) (*sns.PublishOutput, error) {
	f.published = append(f.published, *in)
	return &sns.PublishOutput{}, nil
}

func TestSnsNotifierNoopWithoutTopicArn(t *testing.T) {
	client := &fakeSns{}
	n := &SnsNotifier{Client: client, TopicArn: ""}

	if err := n.Publish(context.Background(), "subject", map[string]any{"a": 1}); err != nil {
		t.Fatal(err)
	}
	if len(client.published) != 0 {
		t.Fatalf("empty TopicArn must be a no-op, got %d publishes", len(client.published))
	}
}

func TestSnsNotifierPublishesJsonPayload(t *testing.T) {
	client := &fakeSns{}
	n := &SnsNotifier{Client: client, TopicArn: "arn:aws:sns:us-east-1:123:topic"}

	if err := n.Publish(context.Background(), "subject", map[string]any{"resource_id": "ecs#a/b"}); err != nil {
		t.Fatal(err)
	}
	if len(client.published) != 1 {
		t.Fatalf("publishes: %d", len(client.published))
	}
	got := *client.published[0].Message
	if !strings.Contains(got, `"resource_id":"ecs#a/b"`) {
		t.Fatalf("message not JSON payload: %q", got)
	}
	if *client.published[0].TopicArn != n.TopicArn {
		t.Fatalf("topic arn: %q", *client.published[0].TopicArn)
	}
}

// A-3/B-7: SNS Subject must be printable ASCII and <=100 chars, or Publish itself fails with InvalidParameter. 99-byte truncation of a UTF-8 string can split a multi-byte rune; sanitizeSnsSubject must not do that.
func TestSanitizeSnsSubjectTruncatesAtLimit(t *testing.T) {
	long := strings.Repeat("a", 150)
	got := sanitizeSnsSubject(long)
	if len(got) != snsSubjectMaxLen {
		t.Fatalf("length: %d, want %d", len(got), snsSubjectMaxLen)
	}
}

func TestSanitizeSnsSubjectReplacesNonAscii(t *testing.T) {
	got := sanitizeSnsSubject("[cheapskate] stop: ecs#日本語-service")
	for i, r := range got {
		if r > 0x7e || r < 0x20 {
			t.Fatalf("non-ASCII/control byte survived at %d in %q", i, got)
		}
	}
	if !strings.Contains(got, "ecs#") {
		t.Fatalf("ASCII portion must survive: %q", got)
	}
}

func TestSanitizeSnsSubjectTruncationDoesNotSplitMultibyteRune(t *testing.T) {
	// Every multi-byte rune becomes a single '?' byte before truncation, so truncating at a byte boundary is always safe — there is nothing multi-byte left to split.
	subject := strings.Repeat("a", 95) + "日本語超過分"
	got := sanitizeSnsSubject(subject)
	if !utf8.ValidString(got) {
		t.Fatalf("truncated subject is not valid UTF-8: %q", got)
	}
	if len(got) > snsSubjectMaxLen {
		t.Fatalf("length: %d", len(got))
	}
}

func TestSubjectTruncatedBeforePublish(t *testing.T) {
	client := &fakeSns{}
	n := &SnsNotifier{Client: client, TopicArn: "arn:aws:sns:us-east-1:123:topic"}
	long := "[cheapskate] error: " + strings.Repeat("x", 200)

	if err := n.Publish(context.Background(), long, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if got := *client.published[0].Subject; len(got) > snsSubjectMaxLen {
		t.Fatalf("published subject length: %d", len(got))
	}
}
