// reconciler の Lambda エントリポイント (コンテナイメージでのデプロイ)
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"os"
	"strconv"
	"time"
	_ "time/tzdata" // イメージ内に OS の tzdata がなくても cron のタイムゾーンを解決する

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"

	"cheapskate/internal/app/reconcile"
	"cheapskate/internal/aws/cloudwatch"
	"cheapskate/internal/state"
	"cheapskate/internal/wire"
)

// METRICS_NAMESPACE が未設定のときに使う EMF メトリクスの CloudWatch 名前空間
const defaultMetricsNamespace = "cheapskate"

// 環境変数から EMF メトリクスの発行設定を組み立てる
//
// 有効・無効 (METRICS_ENABLED) と名前空間 (METRICS_NAMESPACE) を別の変数に分ける
// 1 つにまとめると、未設定と空文字列で意味が変わる規約が必要になる
// 他の環境変数は両者を区別しないため、この変数だけが異なる規約を持つことになる
//
// METRICS_ENABLED の既定は false である
// EMF から生成されるメトリクスはカスタムメトリクスとして課金されるため、明示的に有効化した環境だけで発行する
// 無効化で失われるのは件数と推移であり、失敗の検知はログと SNS 通知が担う
//
// 解釈できない METRICS_ENABLED は既定へ倒さず起動を失敗させる
// 既定へ倒すと、無効化したつもりの設定が有効なまま課金され、それを検知する手段が請求書だけになる
func metricsEmitter(logger *slog.Logger) (cloudwatch.Emitter, error) {
	namespace := os.Getenv("METRICS_NAMESPACE")
	if namespace == "" {
		namespace = defaultMetricsNamespace
	}
	enabled := false
	if raw := os.Getenv("METRICS_ENABLED"); raw != "" {
		var err error
		if enabled, err = strconv.ParseBool(raw); err != nil {
			return cloudwatch.Emitter{}, fmt.Errorf("invalid METRICS_ENABLED %q: want a boolean (true/false, 1/0)", raw)
		}
	}
	return cloudwatch.Emitter{Log: logger, Namespace: namespace, Enabled: enabled}, nil
}

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
	location, err := time.LoadLocation(defaultTimezone)
	if err != nil {
		log.Fatalf("invalid DEFAULT_TIMEZONE %q: %v", defaultTimezone, err)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	metrics, err := metricsEmitter(logger)
	if err != nil {
		log.Fatal(err)
	}
	if !metrics.Enabled {
		// メトリクスが出力されない原因を特定するため、コールドスタートごとに 1 行記録する
		logger.Info("metrics-disabled", "reason", "METRICS_ENABLED is not true")
	}
	deps := &reconcile.Deps{
		Store:      state.New(dynamodb.NewFromConfig(cfg), table),
		Discoverer: wire.Discoverer(cfg),
		Targets:    wire.Targets(cfg),
		Notifier:   wire.Notifier(cfg, os.Getenv("NOTIFICATION_TOPIC_ARN")),
		Location:   location,
		Log:        logger,
	}

	lambda.Start(func(ctx context.Context, raw json.RawMessage) (reconcile.Summary, error) {
		now := time.Now().UTC()
		summary, err := reconcile.Run(ctx, raw, deps, now)
		if err != nil {
			// サイクル全体が開始できなかった場合である
			// 個々のリソースへ到達していないため、件数ではなく 1 本のフラグとして出力する
			metrics.Emit(now, []cloudwatch.Metric{{Name: "ReconcileAborted", Value: 1}})
			return summary, err
		}
		metrics.Emit(now, []cloudwatch.Metric{
			{Name: "ReconcileAborted", Value: 0},
			{Name: "ReconciledResources", Value: summary.Reconciled},
			{Name: "ReconcileActions", Value: len(summary.Actions)},
			{Name: "ReconcileErrors", Value: len(summary.Errors)},
		})
		// リソース単位・グループ単位の失敗は Summary、ログ、通知で報告し、Lambda 呼び出し自体は成功とする。
		// ここでエラーを返すと EventBridge が全体 reconcile を再送し、正常に処理できたリソースまで再探索するためである。
		return summary, nil
	})
}
