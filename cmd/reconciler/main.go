// reconciler の Lambda エントリポイント（コンテナイメージでのデプロイ）
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
// 有効・無効（METRICS_ENABLED）と名前空間（METRICS_NAMESPACE）を別の変数にしているのは、1 つにまとめると「未設定なら既定の名前空間で有効、空文字列なら無効」という、未設定と空文字列で意味が変わる約束が必要になるためである
// 他の環境変数はどれも両者を区別しないので、そこだけ規約が違うのは事故のもとになる
//
// METRICS_ENABLED の既定は true である
// EMF から生成されるメトリクスはカスタムメトリクスとして課金される（4 本で月 1 ドル強）ので、本体を月 1 ドル未満に収めたい利用者は明示的に切れる
// 無効にしても失われるのは件数と推移だけで、失敗の検知そのものは Lambda 組み込みの Errors メトリクス（ハンドラが失敗件数に応じてエラーを返す）と SNS 通知が引き続き担う
//
// 解釈できない METRICS_ENABLED は既定へ倒さず起動を失敗させる
// "fasle" のような打ち間違いを黙って「有効」と読むと、切ったつもりの課金が続くことになる
// 切れていないことに気づく手段が請求書しかないより、起動時に落ちるほうがよい
func metricsEmitter(logger *slog.Logger) cloudwatch.Emitter {
	namespace := os.Getenv("METRICS_NAMESPACE")
	if namespace == "" {
		namespace = defaultMetricsNamespace
	}
	enabled := true
	if raw := os.Getenv("METRICS_ENABLED"); raw != "" {
		var err error
		if enabled, err = strconv.ParseBool(raw); err != nil {
			log.Fatalf("invalid METRICS_ENABLED %q: want a boolean (true/false, 1/0)", raw)
		}
	}
	return cloudwatch.Emitter{Log: logger, Namespace: namespace, Enabled: enabled}
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

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	metrics := metricsEmitter(logger)
	if !metrics.Enabled {
		// 「メトリクスが出ていない」の原因を探させないため、コールドスタートごとに 1 行だけ残す
		logger.Info("metrics-disabled", "reason", "METRICS_ENABLED is false")
	}
	deps := &reconcile.Deps{
		Store:           state.New(dynamodb.NewFromConfig(cfg), table),
		Discoverer:      wire.Discoverer(cfg),
		Targets:         wire.Targets(cfg),
		Notifier:        wire.Notifier(cfg, os.Getenv("NOTIFICATION_TOPIC_ARN")),
		DefaultTimezone: defaultTimezone,
		Log:             logger,
	}

	lambda.Start(func(ctx context.Context, raw json.RawMessage) (reconcile.Summary, error) {
		now := time.Now().UTC()
		summary, err := reconcile.Run(ctx, raw, deps, now)
		if err != nil {
			// サイクル全体が立ち上がらなかった場合（payload 不正、Scan 失敗）
			// 個々のリソースまで到達していないので、件数ではなく 1 本のフラグとして出す
			metrics.Emit(now, []cloudwatch.Metric{{Name: "ReconcileAborted", Value: 1}})
			return summary, err
		}
		metrics.Emit(now, []cloudwatch.Metric{
			{Name: "ReconcileAborted", Value: 0},
			{Name: "ReconciledResources", Value: summary.Reconciled},
			{Name: "ReconcileActions", Value: len(summary.Actions)},
			{Name: "ReconcileErrors", Value: len(summary.Errors)},
		})
		// リソース単位・グループ単位の失敗も Lambda の Errors メトリクスへ乗せる
		// Run 自身がここで中断しないのは意図どおりで、1 件の失敗が残りの収束を止めてはならない
		// 失敗を握りつぶすかどうかと、握りつぶした結果を呼び出し側へ報告するかどうかは別の話である
		//
		// EventBridge の非同期リトライで同じフル reconcile が最大 2 回追加で走るが、収束済みのリソースにはアクションが起きず、継続中のエラーは通知の重複排除に当たるので、通知が増えることもない
		if len(summary.Errors) > 0 {
			return summary, fmt.Errorf("reconcile completed with %d resource-level error(s); see status# last_error and the SNS notifications", len(summary.Errors))
		}
		return summary, nil
	})
}
