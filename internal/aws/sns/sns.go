// cheapskate の通知を SNS トピックへ publish する
// port.Notifier インターフェースを実装する
package sns

import (
	"context"
	"encoding/json"
	"strings"

	awssns "github.com/aws/aws-sdk-go-v2/service/sns"
)

//go:generate go tool mockgen -typed -destination mocks/mocks.go -package mocks cheapskate/internal/aws/sns API

// SNS が定める Subject の上限であり、印字可能な ASCII かつ 100 文字以内である
const subjectMaxLen = 100

// 利用する SNS クライアントの部分集合
type API interface {
	Publish(ctx context.Context, in *awssns.PublishInput, opts ...func(*awssns.Options)) (*awssns.PublishOutput, error)
}

// SNS トピックへ publish する
// トピック ARN が空の場合は何も行わない。通知が無効な状態を表す
type Notifier struct {
	Client   API
	TopicArn string
}

func (n *Notifier) Publish(ctx context.Context, subject string, payload map[string]any) error {
	if n.TopicArn == "" {
		return nil
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	subject = sanitizeSubject(subject)
	message := string(body)
	_, err = n.Client.Publish(ctx, &awssns.PublishInput{
		TopicArn: &n.TopicArn,
		Subject:  &subject,
		Message:  &message,
	})
	return err
}

// 文字列を SNS の Subject が受け付ける形式へ変換する
// 印字可能な ASCII のみを残し、非 ASCII と制御文字は '?' へ置き換えるため、切り詰めがマルチバイト文字を分断することはない
// 長さは 100 文字以内とする
// resource_id の全体は JSON ペイロード (Message) にも含まれるため、Subject の切り詰めにより失われることはない
func sanitizeSubject(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || r > 0x7e {
			b.WriteByte('?')
			continue
		}
		b.WriteRune(r)
	}
	subject := b.String()
	if len(subject) > subjectMaxLen {
		subject = subject[:subjectMaxLen]
	}
	return subject
}
