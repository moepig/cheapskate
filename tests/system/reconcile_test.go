//go:build integration

// reconcile ループを実アダプタへ結線した状態で、エミュレータに対して実行する
//
// 対象は reconcile パッケージ単体ではなく、組み上がったシステムである
// internal/wire が本番で行う結線をテスト内で構築し、実 DynamoDB、実 RDS、実 SNS に対して 1 サイクル実行し、状態の遷移と通知を検証する
// 本パッケージの位置づけは doc.go を参照
package system

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cheapskate/internal/app/port"
	"cheapskate/internal/app/reconcile"
	"cheapskate/internal/aws/compute"
	awssns "cheapskate/internal/aws/sns"
	"cheapskate/internal/core/model"
	"cheapskate/internal/devtools/emutest"
	"cheapskate/internal/state"
)

// Describe はエミュレータへの実際の呼び出しとし、Stop/Start は呼び出しの記録のみとする
// Floci が StopDBInstance/StartDBInstance を実装しないためである
// これらの API 呼び出しは、実 AWS 上の受け入れテストが検証する
type spyRdsInstanceTarget struct {
	*compute.RdsInstanceTarget
	stopped []string
	started []string
}

func (s *spyRdsInstanceTarget) Stop(_ context.Context, ref string) error {
	s.stopped = append(s.stopped, ref)
	return nil
}

func (s *spyRdsInstanceTarget) Start(_ context.Context, res model.Resource) error {
	s.started = append(s.started, res.Ref)
	return nil
}

// port.Discoverer を満たす手書きのスタブであり、セレクタのタグ値をキーとする
// 本ファイルのテストは、いずれのグループもタグ値をグループ名と一致させる
// Floci の Resource Groups Tagging API の対応範囲は限定的であるため、結合テストはグループ所属の探索でこれに依存しない
// エミュレータを経由するのは、DynamoDB の読み書きに限る
type staticDiscoverer struct {
	byGroup map[string][]model.Resource
}

var _ port.Discoverer = (*staticDiscoverer)(nil)

func (d *staticDiscoverer) Discover(_ context.Context, sel model.Selector) ([]model.Resource, error) {
	return d.byGroup[sel.TagValue], nil
}

// SNS トピックへ SQS キューを購読させ、通知の到達を検証可能とする
type notificationProbe struct {
	sqs      *sqs.Client
	queueURL string
	topicArn string
}

func newNotificationProbe(t *testing.T, cfg aws.Config) *notificationProbe {
	t.Helper()
	ctx := context.Background()
	snsClient := sns.NewFromConfig(cfg)
	sqsClient := sqs.NewFromConfig(cfg)

	topicName := emutest.RandomName("cheapskate-itest")
	topic, err := snsClient.CreateTopic(ctx, &sns.CreateTopicInput{Name: &topicName})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = snsClient.DeleteTopic(ctx, &sns.DeleteTopicInput{TopicArn: topic.TopicArn}) })

	queueName := topicName + "-probe"
	queue, err := sqsClient.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: &queueName})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = sqsClient.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: queue.QueueUrl}) })

	attrs, err := sqsClient.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       queue.QueueUrl,
		AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameQueueArn},
	})
	require.NoError(t, err)
	queueArn := attrs.Attributes["QueueArn"]

	_, err = snsClient.Subscribe(ctx, &sns.SubscribeInput{
		TopicArn: topic.TopicArn,
		Protocol: aws.String("sqs"),
		Endpoint: &queueArn,
	})
	require.NoError(t, err)
	return &notificationProbe{sqs: sqsClient, queueURL: *queue.QueueUrl, topicArn: *topic.TopicArn}
}

// プローブ用キューへ到達した通知の Subject と本文を返す
// SNS から SQS へのファンアウトは非同期であり、ReceiveMessage はキュー内の一部のみを返す場合がある
// したがって 1 回の受信で全件を取得できるとは仮定せず、`want` 件の到達または期限まで受信を繰り返す
// 件数の検証は呼び出し側が行う
func (p *notificationProbe) receive(t *testing.T, want int) []snsEnvelope {
	t.Helper()
	envelopes := []snsEnvelope{}
	deadline := time.Now().Add(30 * time.Second)
	for len(envelopes) < want && time.Now().Before(deadline) {
		out, err := p.sqs.ReceiveMessage(context.Background(), &sqs.ReceiveMessageInput{
			QueueUrl:            &p.queueURL,
			MaxNumberOfMessages: 10,
			WaitTimeSeconds:     5,
		})
		require.NoError(t, err)
		for _, m := range out.Messages {
			var e snsEnvelope
			require.NoErrorf(t, json.Unmarshal([]byte(*m.Body), &e), "non-SNS message in probe queue")
			envelopes = append(envelopes, e)
		}
	}
	return envelopes
}

type snsEnvelope struct {
	Subject string `json:"Subject"`
	Message string `json:"Message"`
}

func createDBInstance(t *testing.T, cfg aws.Config, identifier string) {
	t.Helper()
	ctx := context.Background()
	client := rds.NewFromConfig(cfg)
	_, err := client.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier: &identifier,
		Engine:               aws.String("postgres"),
		DBInstanceClass:      aws.String("db.t3.micro"),
		MasterUsername:       aws.String("master"),
		MasterUserPassword:   aws.String("secret99"),
		AllocatedStorage:     aws.Int32(5),
	})
	require.NoErrorf(t, err, "create db instance")
	t.Cleanup(func() {
		_, _ = client.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{
			DBInstanceIdentifier: &identifier,
			SkipFinalSnapshot:    aws.Bool(true),
		})
	})

	tgt := &compute.RdsInstanceTarget{Client: client}
	deadline := time.Now().Add(120 * time.Second)
	for {
		obs, err := tgt.Describe(ctx, identifier)
		require.NoError(t, err)
		if obs.State == model.StateRunning {
			return
		}
		require.Falsef(t, time.Now().After(deadline), "instance %s not available in time (last: %s %s)", identifier, obs.State, obs.Detail)
		time.Sleep(2 * time.Second)
	}
}

type harness struct {
	cfg        aws.Config
	store      *state.Store
	spy        *spyRdsInstanceTarget
	discoverer *staticDiscoverer
	probe      *notificationProbe
	deps       *reconcile.Deps
}

func newHarness(t *testing.T) *harness {
	cfg := emutest.Config(t)
	table := emutest.CreateStateTable(t, cfg)
	s := state.New(dynamodb.NewFromConfig(cfg), table)
	spy := &spyRdsInstanceTarget{RdsInstanceTarget: &compute.RdsInstanceTarget{Client: rds.NewFromConfig(cfg)}}
	discoverer := &staticDiscoverer{byGroup: map[string][]model.Resource{}}
	probe := newNotificationProbe(t, cfg)
	return &harness{
		cfg: cfg, store: s, spy: spy, discoverer: discoverer, probe: probe,
		deps: &reconcile.Deps{
			Store:           s,
			Discoverer:      discoverer,
			Targets:         map[model.ResourceType]port.Target{model.TypeRdsInstance: spy},
			Notifier:        &awssns.Notifier{Client: sns.NewFromConfig(cfg), TopicArn: probe.topicArn},
			DefaultTimezone: "UTC",
			Log:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
	}
}

// セレクタのタグ値をグループ名と一致させた pinned なグループを書き込み、対応するリソースをスタブの discoverer へ結線する
// 実際の Tagging API を呼ばずに、`cheapskate-cli set-selector` と `pin` の組み合わせを再現する
func (h *harness) pin(t *testing.T, group string, res model.Resource, desired model.DesiredState) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, h.store.PutGroup(ctx, model.GroupSpec{
		Name: group, Mode: model.ModePinned, Desired: desired,
		TagKey: "env", TagValue: group, Types: []model.ResourceType{res.Type},
	}))
	h.discoverer.byGroup[group] = []model.Resource{res}
}

func TestPinnedStopFlow(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	identifier := emutest.RandomName("itest-db")
	createDBInstance(t, h.cfg, identifier)
	res := model.Resource{Type: model.TypeRdsInstance, Ref: identifier}
	resourceID := res.ID()
	group := "dev"
	h.pin(t, group, res, model.DesiredStopped)

	summary, err := reconcile.Run(ctx, json.RawMessage(`{}`), h.deps, time.Now().UTC())
	require.NoError(t, err)
	assert.Empty(t, summary.Errors)
	assert.Equal(t, []string{identifier}, h.spy.stopped)

	status, err := h.store.GetStatus(ctx, resourceID)
	require.NoError(t, err)
	assert.Equal(t, model.ActionStop, status.LastAction)

	envelopes := h.probe.receive(t, 1)
	require.Len(t, envelopes, 1)
	assert.Equal(t, "[cheapskate] stop: "+group+"/"+resourceID, envelopes[0].Subject)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(envelopes[0].Message), &payload))
	assert.Equal(t, "stop", payload["action"])
	assert.Equal(t, resourceID, payload["resource_id"])
	assert.Equal(t, group, payload["group"])
}

// 単一リソースへ絞る reconcile は、メンバー登録とともに廃止した
// RDS イベントは、他の呼び出しと同じく全グループを reconcile する
func TestRdsEventTriggersFullReconcile(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	a := emutest.RandomName("itest-db-a")
	b := emutest.RandomName("itest-db-b")
	createDBInstance(t, h.cfg, a)
	createDBInstance(t, h.cfg, b)

	h.pin(t, "dev-a", model.Resource{Type: model.TypeRdsInstance, Ref: a}, model.DesiredStopped)
	h.pin(t, "dev-b", model.Resource{Type: model.TypeRdsInstance, Ref: b}, model.DesiredStopped)

	event := `{
	  "source": "aws.rds",
	  "detail-type": "RDS DB Instance Event",
	  "detail": {"SourceType": "DB_INSTANCE", "SourceIdentifier": "irrelevant-now", "EventID": "RDS-EVENT-0088"}
	}`
	summary, err := reconcile.Run(ctx, json.RawMessage(event), h.deps, time.Now().UTC())
	require.NoError(t, err)
	assert.Equal(t, 2, summary.Reconciled)
	assert.ElementsMatch(t, []string{a, b}, h.spy.stopped, "an RDS event must reconcile every group")
}

func TestScheduleModeAgainstEmulator(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	identifier := emutest.RandomName("itest-db")
	createDBInstance(t, h.cfg, identifier)
	group := "dev"

	require.NoError(t, h.store.PutGroup(ctx, model.GroupSpec{
		Name: group, Mode: model.ModeSchedule,
		StartCron: "0 9 * * MON-FRI", StopCron: "0 20 * * MON-FRI", Timezone: "Asia/Tokyo",
		TagKey: "env", TagValue: group, Types: []model.ResourceType{model.TypeRdsInstance},
	}))
	h.discoverer.byGroup[group] = []model.Resource{{Type: model.TypeRdsInstance, Ref: identifier}}

	// 水曜 23:00 JST = 14:00 UTC であり、desired は stopped、observed は running となるため stop する
	night := time.Date(2026, 7, 15, 14, 0, 0, 0, time.UTC)
	_, err := reconcile.Run(ctx, json.RawMessage(`{}`), h.deps, night)
	require.NoError(t, err)
	assert.Len(t, h.spy.stopped, 1, "night run must stop")

	// 水曜 12:00 JST = 03:00 UTC であり、desired は running、observed も running となる
	// 収束済みであるためアクションは発生しない
	h.spy.stopped = nil
	noon := time.Date(2026, 7, 15, 3, 0, 0, 0, time.UTC)
	summary, err := reconcile.Run(ctx, json.RawMessage(`{}`), h.deps, noon)
	require.NoError(t, err)
	assert.Empty(t, h.spy.stopped)
	assert.Empty(t, h.spy.started)
	assert.Empty(t, summary.Actions, "business-hours run must converge without action")
}
