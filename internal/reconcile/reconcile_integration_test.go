//go:build integration

package reconcile_test

import (
	"context"
	"encoding/json"
	"fmt"
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

	"cheapskate/internal/emutest"
	"cheapskate/internal/model"
	"cheapskate/internal/reconcile"
	"cheapskate/internal/store"
	"cheapskate/internal/target"
)

// spyRdsInstanceTarget keeps the real Describe (served by the emulator) but records Stop/Start instead of calling them: Floci does not implement StopDBInstance/StartDBInstance, so those API calls are covered by the acceptance tests on real AWS (see DESIGN.md).
type spyRdsInstanceTarget struct {
	*target.RdsInstanceTarget
	stopped []string
	started []string
}

func (s *spyRdsInstanceTarget) PrepareStop(_ context.Context, _ string, _ model.Config, _ model.Status) (*model.SavedState, error) {
	return nil, nil
}

func (s *spyRdsInstanceTarget) Stop(_ context.Context, ref string, _ model.Config, _ model.Status) error {
	s.stopped = append(s.stopped, ref)
	return nil
}

func (s *spyRdsInstanceTarget) Start(_ context.Context, ref string, _ model.Config, _ model.Status) (*model.SavedState, error) {
	s.started = append(s.started, ref)
	return nil, nil
}

// notificationProbe subscribes an SQS queue to the SNS topic so tests can assert that notifications are actually delivered.
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
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = snsClient.DeleteTopic(ctx, &sns.DeleteTopicInput{TopicArn: topic.TopicArn}) })

	queueName := topicName + "-probe"
	queue, err := sqsClient.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: &queueName})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = sqsClient.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: queue.QueueUrl}) })

	attrs, err := sqsClient.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       queue.QueueUrl,
		AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameQueueArn},
	})
	if err != nil {
		t.Fatal(err)
	}
	queueArn := attrs.Attributes["QueueArn"]

	_, err = snsClient.Subscribe(ctx, &sns.SubscribeInput{
		TopicArn: topic.TopicArn,
		Protocol: aws.String("sqs"),
		Endpoint: &queueArn,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &notificationProbe{sqs: sqsClient, queueURL: *queue.QueueUrl, topicArn: *topic.TopicArn}
}

// receive returns the subjects and messages of all notifications delivered within the wait window.
func (p *notificationProbe) receive(t *testing.T) []snsEnvelope {
	t.Helper()
	out, err := p.sqs.ReceiveMessage(context.Background(), &sqs.ReceiveMessageInput{
		QueueUrl:            &p.queueURL,
		MaxNumberOfMessages: 10,
		WaitTimeSeconds:     5,
	})
	if err != nil {
		t.Fatal(err)
	}
	var envelopes []snsEnvelope
	for _, m := range out.Messages {
		var e snsEnvelope
		if err := json.Unmarshal([]byte(*m.Body), &e); err != nil {
			t.Fatalf("non-SNS message in probe queue: %v", err)
		}
		envelopes = append(envelopes, e)
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
	if err != nil {
		t.Fatalf("create db instance: %v", err)
	}
	t.Cleanup(func() {
		_, _ = client.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{
			DBInstanceIdentifier: &identifier,
			SkipFinalSnapshot:    aws.Bool(true),
		})
	})

	tgt := &target.RdsInstanceTarget{Client: client}
	deadline := time.Now().Add(120 * time.Second)
	for {
		obs, err := tgt.Describe(ctx, identifier)
		if err != nil {
			t.Fatal(err)
		}
		if obs.State == model.StateRunning {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("instance %s not available in time (last: %s %s)", identifier, obs.State, obs.Detail)
		}
		time.Sleep(2 * time.Second)
	}
}

type harness struct {
	cfg   aws.Config
	store *store.Store
	spy   *spyRdsInstanceTarget
	probe *notificationProbe
	deps  *reconcile.Deps
}

func newHarness(t *testing.T) *harness {
	cfg := emutest.Config(t)
	table := emutest.CreateStateTable(t, cfg)
	s := store.New(dynamodb.NewFromConfig(cfg), table)
	spy := &spyRdsInstanceTarget{RdsInstanceTarget: &target.RdsInstanceTarget{Client: rds.NewFromConfig(cfg)}}
	probe := newNotificationProbe(t, cfg)
	return &harness{
		cfg: cfg, store: s, spy: spy, probe: probe,
		deps: &reconcile.Deps{
			Store:           s,
			Targets:         map[string]target.Target{model.TypeRdsInstance: spy},
			Notifier:        &reconcile.SnsNotifier{Client: sns.NewFromConfig(cfg), TopicArn: probe.topicArn},
			DefaultTimezone: "UTC",
			Log:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
	}
}

func TestPinnedStopFlow(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	identifier := emutest.RandomName("itest-db")
	createDBInstance(t, h.cfg, identifier)
	resourceID := "rds-instance#" + identifier

	err := h.store.PutConfig(ctx, model.ConfigItem{
		PK: model.ConfigPrefix + resourceID, Type: model.TypeRdsInstance,
		Mode: model.ModePinned, Desired: model.DesiredStopped,
	})
	if err != nil {
		t.Fatal(err)
	}

	summary, err := reconcile.Run(ctx, json.RawMessage(`{}`), h.deps, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Errors) != 0 {
		t.Fatalf("errors: %+v", summary.Errors)
	}
	if len(h.spy.stopped) != 1 || h.spy.stopped[0] != identifier {
		t.Fatalf("stop calls: %v", h.spy.stopped)
	}

	status, err := h.store.GetStatus(ctx, resourceID)
	if err != nil {
		t.Fatal(err)
	}
	if status.LastAction != "stop" {
		t.Fatalf("last_action: %q", status.LastAction)
	}

	envelopes := h.probe.receive(t)
	if len(envelopes) != 1 {
		t.Fatalf("notifications delivered: %d", len(envelopes))
	}
	if envelopes[0].Subject != "[cheapskate] stop: "+resourceID {
		t.Fatalf("subject: %q", envelopes[0].Subject)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(envelopes[0].Message), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["action"] != "stop" || payload["resource_id"] != resourceID {
		t.Fatalf("payload: %v", payload)
	}
}

func TestRdsEventReconcilesOnlyNamedResource(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	eventTarget := emutest.RandomName("itest-db-a")
	other := emutest.RandomName("itest-db-b")
	createDBInstance(t, h.cfg, eventTarget)
	createDBInstance(t, h.cfg, other)

	for _, id := range []string{eventTarget, other} {
		err := h.store.PutConfig(ctx, model.ConfigItem{
			PK: model.ConfigPrefix + "rds-instance#" + id, Type: model.TypeRdsInstance,
			Mode: model.ModePinned, Desired: model.DesiredStopped,
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	event := fmt.Sprintf(`{
	  "source": "aws.rds",
	  "detail-type": "RDS DB Instance Event",
	  "detail": {"SourceType": "DB_INSTANCE", "SourceIdentifier": %q, "EventID": "RDS-EVENT-0088"}
	}`, eventTarget)
	summary, err := reconcile.Run(ctx, json.RawMessage(event), h.deps, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if summary.Reconciled != 1 {
		t.Fatalf("reconciled: %d", summary.Reconciled)
	}
	if len(h.spy.stopped) != 1 || h.spy.stopped[0] != eventTarget {
		t.Fatalf("stop calls: %v (must not include %s)", h.spy.stopped, other)
	}
}

func TestScheduleModeAgainstEmulator(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	identifier := emutest.RandomName("itest-db")
	createDBInstance(t, h.cfg, identifier)
	resourceID := "rds-instance#" + identifier

	err := h.store.PutConfig(ctx, model.ConfigItem{
		PK: model.ConfigPrefix + resourceID, Type: model.TypeRdsInstance,
		Mode: model.ModeSchedule, StartCron: "0 9 * * MON-FRI", StopCron: "0 20 * * MON-FRI",
		Timezone: "Asia/Tokyo",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Wednesday 23:00 JST = 14:00 UTC → desired stopped, observed running → stop.
	night := time.Date(2026, 7, 15, 14, 0, 0, 0, time.UTC)
	if _, err := reconcile.Run(ctx, json.RawMessage(`{}`), h.deps, night); err != nil {
		t.Fatal(err)
	}
	if len(h.spy.stopped) != 1 {
		t.Fatalf("night run must stop: %v", h.spy.stopped)
	}

	// Wednesday 12:00 JST = 03:00 UTC → desired running, observed running (the emulator instance is still available) → converged, no action.
	h.spy.stopped = nil
	noon := time.Date(2026, 7, 15, 3, 0, 0, 0, time.UTC)
	summary, err := reconcile.Run(ctx, json.RawMessage(`{}`), h.deps, noon)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.spy.stopped)+len(h.spy.started) != 0 || len(summary.Actions) != 0 {
		t.Fatalf("business-hours run must converge without action: %+v", summary)
	}
}
