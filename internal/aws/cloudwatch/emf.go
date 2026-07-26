// CloudWatch Embedded Metric Format（EMF）のログ行を出力する
// Lambda が stderr へ書いたものを CloudWatch Logs が取り込み、そこからメトリクスが自動生成されるため、PutMetricData の API 呼び出しも追加の IAM 権限も要らない
//
// reconciler がリソース単位の失敗をここへ出すのは、それが Lambda の Errors メトリクスには乗らないためである
// ハンドラは Summary.Errors が非空なら別途エラーも返すが、Errors メトリクスは「何件失敗したか」を区別できない
// 失敗件数の推移とアクション件数を見るにはこちらの方が要る
package cloudwatch

import (
	"log/slog"
	"time"
)

// EMF のメトリクス 1 件で、値は常に Count 単位の整数とする
// cheapskate が出すのは件数だけであり、レイテンシやバイト数を測る予定はない
type Metric struct {
	Name  string
	Value int
}

// EMF のログ行を出力する
// Enabled が false なら何もしない
// 有効・無効の判断を呼び出し側の if 文ではなくこの型が持つのは、sns.Notifier が TopicArn 空で no-op になるのと同じ約束にするためである
// 発行箇所は複数あり、そのすべてに同じ条件を書かせると、片方だけ書き忘れる余地が残る
type Emitter struct {
	Log       *slog.Logger
	Namespace string
	Enabled   bool
}

// metrics を EMF のログ行 1 本として出力する
// Dimensions を空にしているのは、cheapskate の reconciler が 1 アカウント 1 関数だからである
// グループ単位に次元を切るとカーディナリティが利用者のグループ数に比例して増えるうえ、どのグループが失敗したかは status# と SNS 通知の側がすでに持っている
//
// 無効なとき、名前空間が空のとき、metrics が空のときは何もしない
// 後ろ 2 つは CloudWatch 側で黙って捨てられるだけの行になるので、ログにゴミを残さないためここで止める
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
