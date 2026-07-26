//go:build image

// ビルド済みの webconsole イメージに、本番で API Gateway が届けるのと同じプロキシイベントを投げる
//
// コンソール本体は Lambda を知らない素の HTTP サーバで、イベントとの変換はイメージに同梱した
// Lambda Web Adapter 拡張が行う（docs/ja/architecture/web_console.md）
// その拡張は Lambda 側にしか無いので、ここが経路を通せる唯一の場所である
// 単体テストと統合テストはサーバのハンドラを直接叩くため、アダプタを通らない
//
// パッケージの位置づけとハーネスの前提は doc.go と harness_test.go を参照
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

// API Gateway が実際に観測した接続元
// リソースポリシーの aws:SourceIp が許可判定に使う値そのものであり、コンソールが残してよい唯一の IP である
const realSourceIP = "203.0.113.77"

// クライアントが x-amzn-request-context ヘッダに自分で詰めてきた IP
// ヘッダはクライアントが自由に書けるので、アダプタがイベント由来のもので上書きしなければログに漏れる
const forgedSourceIP = "198.51.100.66"

// warm up で使う IP
// 上の 2 つと混ざらないよう別の網（TEST-NET-1）から取る
const warmupSourceIP = "192.0.2.1"

func TestWebconsoleImageServesThroughTheLambdaWebAdapter(t *testing.T) {
	cfg := emutest.Config(t)
	// 使い捨ての空テーブルであり、グループが 0 件でも一覧ページは描ける
	table := emutest.CreateStateTable(t, cfg)

	console := startUnderRIE(t, buildImage(t, "webconsole"), emulatorEnv(t, table),
		proxyEvent(t, warmupSourceIP, nil))

	// アダプタが拡張として起動し、イベントをループバック越しの HTTP に変換し、応答をプロキシレスポンスに戻せていること
	// 拡張の入れ忘れやポートの食い違いは、すべてここで落ちる
	t.Run("proxy event reaches the server", func(t *testing.T) {
		var resp events.APIGatewayProxyResponse
		require.NoError(t, json.Unmarshal(
			console.invoke(t, proxyEvent(t, realSourceIP, forgedRequestContext(forgedSourceIP))), &resp))
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	// 応答だけでは、どちらの IP を見たのかは分からない
	// アダプタがクライアント由来のヘッダを捨ててイベントの requestContext に差し替えたことは、コンソールが残すログでしか確認できない
	t.Run("logs the request context source IP, not the forged header", func(t *testing.T) {
		logs := console.logs(t)
		assert.Contains(t, logs, `"client":"`+realSourceIP+`"`)
		assert.NotContains(t, logs, forgedSourceIP)
	})
}

// API Gateway REST API（本番構成）が届けるプロキシイベントを組み立てる
// sourceIP は requestContext.identity に入る、つまりクライアントが触れない側である
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

// アダプタが立てるのと同じ名前のヘッダを、クライアントが偽装して送ってきた形にする
func forgedRequestContext(sourceIP string) map[string]string {
	return map[string]string{
		"x-amzn-request-context": `{"identity":{"sourceIp":"` + sourceIP + `"}}`,
	}
}
