package reconcile

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/sns"
)

// snsSubjectMaxLen is SNS's documented Subject limit: ASCII, printable, <= 100 characters.
const snsSubjectMaxLen = 100

// SnsAPI is the subset of the SNS client the notifier uses.
type SnsAPI interface {
	Publish(ctx context.Context, in *sns.PublishInput, opts ...func(*sns.Options)) (*sns.PublishOutput, error)
}

// SnsNotifier publishes to an SNS topic. With an empty topic ARN it is a no-op (notifications disabled).
type SnsNotifier struct {
	Client   SnsAPI
	TopicArn string
}

func (n *SnsNotifier) Publish(ctx context.Context, subject string, payload map[string]any) error {
	if n.TopicArn == "" {
		return nil
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	subject = sanitizeSnsSubject(subject)
	message := string(body)
	_, err = n.Client.Publish(ctx, &sns.PublishInput{
		TopicArn: &n.TopicArn,
		Subject:  &subject,
		Message:  &message,
	})
	return err
}

// sanitizeSnsSubject makes a string safe as an SNS Subject: printable ASCII only (non-ASCII and control characters become '?', so a truncation never splits a multi-byte rune), at most 100 characters. The full resource_id survives regardless, since it is already in the JSON payload (Message), not just the subject.
func sanitizeSnsSubject(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || r > 0x7e {
			b.WriteByte('?')
			continue
		}
		b.WriteRune(r)
	}
	subject := b.String()
	if len(subject) > snsSubjectMaxLen {
		subject = subject[:snsSubjectMaxLen]
	}
	return subject
}
