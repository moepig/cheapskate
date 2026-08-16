// オプトインのブラウザ向けフロントエンドを提供する
//
// 実行形態によらず localhost にバインドした単一の HTTP サーバであり、Lambda 固有のコードを持たない
// Lambda 上 (IP 制限付き API Gateway REST API の背後) では、Lambda Web Adapter 拡張がランタイム API を処理し、各呼び出しをこのサーバへの HTTP リクエストへ変換する
// ローカル実行と Lambda 実行は同一の経路を通る (Dockerfile の webconsole ステージ)
package main

import (
	"context"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"
	_ "time/tzdata"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"

	"cheapskate/internal/state"
	"cheapskate/internal/ui/webconsole"
	"cheapskate/internal/wire"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "listen address")
	flag.Parse()

	// reconciler と同じ JSON 形式で stderr へ出力する
	// Lambda 上ではそのまま CloudWatch Logs へ取り込まれ、フィールドによる絞り込みが可能となる
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	table := os.Getenv("STATE_TABLE_NAME")
	if table == "" {
		table = os.Getenv("CHEAPSKATE_TABLE")
	}
	if table == "" {
		fatal(logger, "STATE_TABLE_NAME (or CHEAPSKATE_TABLE) is required")
	}
	loc := time.Local
	if tz := os.Getenv("DEFAULT_TIMEZONE"); tz != "" {
		var err error
		if loc, err = time.LoadLocation(tz); err != nil {
			fatal(logger, "invalid DEFAULT_TIMEZONE", "timezone", tz, "error", err.Error())
		}
	}

	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		fatal(logger, "load AWS config", "error", err.Error())
	}
	s := state.New(dynamodb.NewFromConfig(cfg), table)
	// BASE_PATH はブラウザから見えるパスの接頭辞であり、API Gateway のステージに対応する
	// Lambda のプロキシイベントのパスには、この接頭辞が含まれない
	base := os.Getenv("BASE_PATH")
	handler := webconsole.New(s, wire.Discoverer(cfg), wire.Describers(cfg), base, loc, logger).Handler()

	// コンテナでは待ち受けポートをイメージ側が決定する (Dockerfile で PORT と AWS_LWA_PORT を一致させる)
	// アダプタはループバック経由で接続するため、バインド先は常に 127.0.0.1 とする
	listen := *addr
	if port := os.Getenv("PORT"); port != "" {
		listen = net.JoinHostPort("127.0.0.1", port)
	}

	logger.Info("startup", "table", table, "base_path", base, "timezone", loc.String(), "addr", listen)
	fatal(logger, "http server stopped", "error", http.ListenAndServe(listen, handler).Error())
}

// 起動を継続できない失敗を 1 行記録して終了する
// slog に log.Fatal 相当が存在しないため、この 1 か所へ集約する
func fatal(logger *slog.Logger, msg string, args ...any) {
	logger.Error(msg, args...)
	os.Exit(1)
}
