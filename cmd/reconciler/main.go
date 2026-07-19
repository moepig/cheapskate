// Lambda entrypoint for the reconciler (container image deployment).
package main

import (
	"context"
	"encoding/json"
	"log"
	"log/slog"
	"os"
	"time"
	_ "time/tzdata" // cron timezones without OS tzdata in the image

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/config"
	aas "github.com/aws/aws-sdk-go-v2/service/applicationautoscaling"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/sns"

	"cheapskate/internal/reconcile"
	"cheapskate/internal/store"
	"cheapskate/internal/target"
)

func main() {
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		log.Fatalf("load AWS config: %v", err)
	}

	table := os.Getenv("STATE_TABLE_NAME")
	if table == "" {
		log.Fatal("STATE_TABLE_NAME is required")
	}
	defaultTimezone := os.Getenv("DEFAULT_TIMEZONE")
	if defaultTimezone == "" {
		defaultTimezone = "UTC"
	}

	rdsClient := rds.NewFromConfig(cfg)
	deps := &reconcile.Deps{
		Store: store.New(dynamodb.NewFromConfig(cfg), table),
		Targets: targetsByType(
			&target.RdsInstanceTarget{Client: rdsClient},
			&target.RdsClusterTarget{Client: rdsClient},
			&target.EcsServiceTarget{Ecs: ecs.NewFromConfig(cfg), AutoScaling: aas.NewFromConfig(cfg)},
		),
		Notifier: &reconcile.SnsNotifier{
			Client:   sns.NewFromConfig(cfg),
			TopicArn: os.Getenv("NOTIFICATION_TOPIC_ARN"),
		},
		DefaultTimezone: defaultTimezone,
		Log:             slog.New(slog.NewJSONHandler(os.Stderr, nil)),
	}

	lambda.Start(func(ctx context.Context, raw json.RawMessage) (reconcile.Summary, error) {
		return reconcile.Run(ctx, raw, deps, time.Now().UTC())
	})
}

func targetsByType(targets ...target.Target) map[string]target.Target {
	m := map[string]target.Target{}
	for _, t := range targets {
		m[t.Type()] = t
	}
	return m
}
