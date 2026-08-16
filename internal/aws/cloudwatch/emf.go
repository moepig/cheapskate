// CloudWatch Embedded Metric Format (EMF) のログ行を出力する
// Lambda が stderr へ書いた内容を CloudWatch Logs が取り込み、そこからメトリクスを生成するため、PutMetricData の呼び出しと追加の IAM 権限を必要としない
//
// reconciler はリソース単位の失敗をここへ出力する
// この失敗は Lambda の Errors メトリクスに現れないためである
// ハンドラは Summary.Errors が非空の場合にエラーを返すが、Errors メトリクスは失敗件数を区別しない
// 失敗件数の推移とアクション件数の観測には、EMF が必要となる
package cloudwatch

import (
	"log/slog"
	"time"
)

// EMF のメトリクス 1 件であり、値は常に Count 単位の整数とする
// 出力する対象は件数に限る
type Metric struct {
	Name  string
	Value int
}

// EMF のログ行を出力する
// Enabled が false の場合は何も行わない
// 有効・無効の判定を呼び出し側ではなくこの型が持つのは、TopicArn が空のとき no-op となる sns.Notifier と規約を揃えるためである
// 発行箇所は複数あり、そのすべてへ同じ条件を記述した場合、一部の記述漏れが生じうる
type Emitter struct {
	Log       *slog.Logger
	Namespace string
	Enabled   bool
}

// metrics を EMF のログ行 1 本として出力する
// Dimensions は空とする。reconciler は 1 アカウントにつき 1 関数であるためである
// グループ単位の次元を設けた場合、カーディナリティがグループ数に比例して増加する
// 失敗したグループの特定は、status# と SNS 通知が担う
//
// 無効な場合、名前空間が空の場合、metrics が空の場合は何も行わない
// 後の 2 つは CloudWatch 側で破棄される行となるため、出力しない
func (e Emitter) Emit(now time.Time, metrics []Metric) {
	if !e.Enabled || e.Namespace == "" || len(metrics) == 0 {
		return
	}
	defs := make([]map[string]string, 0, len(metrics))
	attrs := make([]any, 0, len(metrics)+1)
	for _, m := range metrics {
		defs = append(defs, map[string]string{"Name": m.Name, "Unit": "Count"})
		attrs = append(attrs, slog.Int(m.Name, m.Value))
	}
	attrs = append(attrs, slog.Any("_aws", map[string]any{
		"Timestamp": now.UnixMilli(),
		"CloudWatchMetrics": []map[string]any{{
			"Namespace":  e.Namespace,
			"Dimensions": [][]string{{}},
			"Metrics":    defs,
		}},
	}))
	e.Log.Info("metrics", attrs...)
}
