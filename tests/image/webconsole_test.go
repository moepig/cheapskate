//go:build image

// ビルド済みの webconsole イメージへ、本番で API Gateway が送信するものと同じプロキシイベントを投入する
//
// コンソール本体は Lambda を参照しない HTTP サーバであり、イベントとの変換はイメージへ同梱した
// Lambda Web Adapter 拡張が行う (docs/ja/architecture/web_console.md)
// この拡張は Lambda 側にのみ存在するため、本パッケージのみがこの経路を検証できる
// 単体テストと統合テストはサーバのハンドラを直接呼ぶため、アダプタを経由しない
//
// パッケージの位置づけとハーネスの前提は、doc.go と harness_test.go を参照
//
//	make image-test   # = go test -tags image -count=1 ./tests/image/
package image

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/aws/aws-lambda-go/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cheapskate/internal/devtools/emutest"
)

// API Gateway が観測した接続元
// リソースポリシーの aws:SourceIp が許可の判定に用いる値であり、コンソールが記録してよい唯一の IP である
const realSourceIP = "203.0.113.77"

// クライアントが x-amzn-request-context ヘッダへ設定した IP
// このヘッダはクライアントが任意に設定できるため、アダプタがイベント由来の値で上書きしない場合、ログへ記録される
const forgedSourceIP = "198.51.100.66"

// warm up で用いる IP
// 上記の 2 つと区別するため、別の網 (TEST-NET-1) から選ぶ
const warmupSourceIP = "192.0.2.1"

func TestWebconsoleImageServesThroughTheLambdaWebAdapter(t *testing.T) {
	cfg := emutest.Config(t)
	// 使い捨ての空テーブルであり、グループが 0 件の場合も一覧ページを描画できる
	table := emutest.CreateStateTable(t, cfg)

	console := startUnderRIE(t, buildImage(t, "webconsole"), emulatorEnv(t, table),
		proxyEvent(t, warmupSourceIP, nil))

	// アダプタが拡張として起動し、イベントをループバック経由の HTTP へ変換し、応答をプロキシレスポンスへ戻すことを確かめる
	// 拡張の欠落とポートの不一致は、いずれも本テストで検出する
	t.Run("proxy event reaches the server", func(t *testing.T) {
		var resp events.APIGatewayProxyResponse
		require.NoError(t, json.Unmarshal(
			console.invoke(t, proxyEvent(t, realSourceIP, forgedRequestContext(forgedSourceIP))), &resp))
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	// 応答からは、参照した IP を判定できない
	// アダプタがクライアント由来のヘッダを破棄し、イベントの requestContext へ差し替えたことは、コンソールが出力するログでのみ確認できる
	t.Run("logs the request context source IP, not the forged header", func(t *testing.T) {
		logs := console.logs(t)
		assert.Contains(t, logs, `"client":"`+realSourceIP+`"`)
		assert.NotContains(t, logs, forgedSourceIP)
	})
}

// API Gateway REST API (本番構成) が送信するプロキシイベントを組み立てる
// sourceIP は requestContext.identity に入り、クライアントは設定できない
func proxyEvent(t *testing.T, sourceIP string, headers map[string]string) []byte {
	t.Helper()

	h := map[string]string{"Host": "example.com"}
	for k, v := range headers {
		h[k] = v
	}

	payload, err := json.Marshal(events.APIGatewayProxyRequest{
		Resource:   "/",
		Path:       "/",
		HTTPMethod: http.MethodGet,
		Headers:    h,
		RequestContext: events.APIGatewayProxyRequestContext{
			AccountID:    "123456789012",
			ResourcePath: "/",
			Path:         "/",
			HTTPMethod:   http.MethodGet,
			RequestID:    "imagetest",
			Stage:        "imagetest",
			Identity:     events.APIGatewayRequestIdentity{SourceIP: sourceIP},
		},
	})
	require.NoError(t, err)
	return payload
}

// アダプタが設定するものと同じ名前のヘッダを、クライアントが送信した状態を構成する
func forgedRequestContext(sourceIP string) map[string]string {
	return map[string]string{
		"x-amzn-request-context": `{"identity":{"sourceIp":"` + sourceIP + `"}}`,
	}
}
