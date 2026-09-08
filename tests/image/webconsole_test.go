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
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
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

func TestWebconsoleImageLifecycleThroughTheLambdaWebAdapter(t *testing.T) {
	cfg := emutest.Config(t)
	table := emutest.CreateStateTable(t, cfg)
	env := emulatorEnv(t, table)
	env["BASE_PATH"] = "/stage"
	env["DEFAULT_TIMEZONE"] = "Asia/Tokyo"
	console := startUnderRIE(t, buildImage(t, "webconsole"), env,
		proxyEvent(t, warmupSourceIP, nil))

	db := dynamodb.NewFromConfig(cfg)
	post := func(values url.Values, origin string) events.APIGatewayProxyResponse {
		headers := map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Origin": origin}
		return invokeProxyResponse(t, console, proxyRequest(t, realSourceIP, http.MethodPost, "/op", values.Encode(), headers, nil))
	}

	schedule := post(url.Values{
		"action": {"schedule"}, "group": {"dev"}, "start": {"0 9 * * *"}, "stop": {"0 20 * * *"},
	}, "http://example.com")
	assert.Equal(t, http.StatusSeeOther, schedule.StatusCode)
	assert.Equal(t, "/stage/group?name=dev&msg=schedule+saved", responseHeader(schedule, "Location"))
	item, err := db.GetItem(context.Background(), &dynamodb.GetItemInput{TableName: aws.String(table), Key: map[string]types.AttributeValue{
		"pk": &types.AttributeValueMemberS{Value: "CONFIG"}, "sk": &types.AttributeValueMemberS{Value: "GROUP#dev"},
	}})
	require.NoError(t, err)
	assert.Equal(t, "0 9 * * *", item.Item["start_cron"].(*types.AttributeValueMemberS).Value)
	assert.Equal(t, "0 20 * * *", item.Item["stop_cron"].(*types.AttributeValueMemberS).Value)

	detail := invokeProxyResponse(t, console, proxyRequest(t, realSourceIP, http.MethodGet, "/group", "", nil, map[string]string{"name": "dev"}))
	assert.Equal(t, http.StatusOK, detail.StatusCode)
	detailBody := responseBody(t, detail)
	assert.Contains(t, detailBody, "<h2>dev</h2>")
	assert.Contains(t, detailBody, "All schedules and dates use Asia/Tokyo")
	assert.Contains(t, detailBody, `action="/stage/op"`)

	crossOrigin := post(url.Values{"action": {"override"}, "group": {"dev"}, "override": {"stopped"}}, "https://attacker.example")
	assert.Equal(t, http.StatusForbidden, crossOrigin.StatusCode)
	item, err = db.GetItem(context.Background(), &dynamodb.GetItemInput{TableName: aws.String(table), Key: map[string]types.AttributeValue{
		"pk": &types.AttributeValueMemberS{Value: "CONFIG"}, "sk": &types.AttributeValueMemberS{Value: "GROUP#dev"},
	}})
	require.NoError(t, err)
	assert.Nil(t, item.Item["override"], "cross-origin POST must not change DynamoDB")

	expectedUntil := time.Now().In(time.FixedZone("JST", 9*60*60)).Add(2 * time.Hour).Truncate(time.Minute)
	until := expectedUntil.Format("2006-01-02T15:04")
	override := post(url.Values{
		"action": {"override"}, "group": {"dev"}, "override": {"stopped"}, "until": {until},
	}, "http://example.com")
	assert.Equal(t, http.StatusSeeOther, override.StatusCode)
	assert.Equal(t, "/stage/group?name=dev&msg=override+saved", responseHeader(override, "Location"))
	item, err = db.GetItem(context.Background(), &dynamodb.GetItemInput{TableName: aws.String(table), Key: map[string]types.AttributeValue{
		"pk": &types.AttributeValueMemberS{Value: "CONFIG"}, "sk": &types.AttributeValueMemberS{Value: "GROUP#dev"},
	}})
	require.NoError(t, err)
	assert.Equal(t, "stopped", item.Item["override"].(*types.AttributeValueMemberS).Value)
	expiresAt, ok := item.Item["override_expires_at"].(*types.AttributeValueMemberN)
	require.True(t, ok, "override_expires_at must be a DynamoDB Number")
	assert.Equal(t, strconv.FormatInt(expectedUntil.Unix(), 10), expiresAt.Value)

	detail = invokeProxyResponse(t, console, proxyRequest(t, realSourceIP, http.MethodGet, "/group", "", nil, map[string]string{"name": "dev"}))
	require.Equal(t, http.StatusOK, detail.StatusCode)
	assert.Contains(t, responseBody(t, detail), "stopped until "+expectedUntil.Format("2006-01-02 15:04 MST"))

	cleared := post(url.Values{"action": {"clear-override"}, "group": {"dev"}}, "http://example.com")
	assert.Equal(t, http.StatusSeeOther, cleared.StatusCode)
	assert.Equal(t, "/stage/group?name=dev&msg=override+cleared", responseHeader(cleared, "Location"))
	item, err = db.GetItem(context.Background(), &dynamodb.GetItemInput{TableName: aws.String(table), Key: map[string]types.AttributeValue{
		"pk": &types.AttributeValueMemberS{Value: "CONFIG"}, "sk": &types.AttributeValueMemberS{Value: "GROUP#dev"},
	}})
	require.NoError(t, err)
	assert.Nil(t, item.Item["override"])
	assert.Nil(t, item.Item["override_expires_at"])

	removed := post(url.Values{"action": {"remove"}, "group": {"dev"}}, "http://example.com")
	assert.Equal(t, http.StatusSeeOther, removed.StatusCode)
	assert.Equal(t, "/stage/?msg=group+removed", responseHeader(removed, "Location"))
	item, err = db.GetItem(context.Background(), &dynamodb.GetItemInput{TableName: aws.String(table), Key: map[string]types.AttributeValue{
		"pk": &types.AttributeValueMemberS{Value: "CONFIG"}, "sk": &types.AttributeValueMemberS{Value: "GROUP#dev"},
	}})
	require.NoError(t, err)
	assert.Empty(t, item.Item)
}

func TestWebconsoleImageRejectsInvalidTimezone(t *testing.T) {
	cfg := emutest.Config(t)
	table := emutest.CreateStateTable(t, cfg)
	assertRejectsInvalidTimezone(t, buildImage(t, "webconsole"), emulatorEnv(t, table))
}

// API Gateway REST API (本番構成) が送信するプロキシイベントを組み立てる
// sourceIP は requestContext.identity に入り、クライアントは設定できない
func proxyEvent(t *testing.T, sourceIP string, headers map[string]string) []byte {
	t.Helper()
	return proxyRequest(t, sourceIP, http.MethodGet, "/", "", headers, nil)
}

func proxyRequest(t *testing.T, sourceIP, method, path, body string, headers, query map[string]string) []byte {
	t.Helper()

	h := map[string]string{"Host": "example.com"}
	for k, v := range headers {
		h[k] = v
	}

	payload, err := json.Marshal(events.APIGatewayProxyRequest{
		Resource:              path,
		Path:                  path,
		HTTPMethod:            method,
		Headers:               h,
		Body:                  body,
		QueryStringParameters: query,
		RequestContext: events.APIGatewayProxyRequestContext{
			AccountID:    "123456789012",
			ResourcePath: path,
			Path:         path,
			HTTPMethod:   method,
			RequestID:    "imagetest",
			Stage:        "imagetest",
			Identity:     events.APIGatewayRequestIdentity{SourceIP: sourceIP},
		},
	})
	require.NoError(t, err)
	return payload
}

func invokeProxyResponse(t *testing.T, console imageUnderRIE, payload []byte) events.APIGatewayProxyResponse {
	t.Helper()
	var response events.APIGatewayProxyResponse
	require.NoError(t, json.Unmarshal(console.invoke(t, payload), &response))
	return response
}

func responseBody(t *testing.T, response events.APIGatewayProxyResponse) string {
	t.Helper()
	if !response.IsBase64Encoded {
		return response.Body
	}
	decoded, err := base64.StdEncoding.DecodeString(response.Body)
	require.NoError(t, err)
	return string(decoded)
}

func responseHeader(response events.APIGatewayProxyResponse, key string) string {
	for name, value := range response.Headers {
		if strings.EqualFold(name, key) {
			return value
		}
	}
	for name, values := range response.MultiValueHeaders {
		if strings.EqualFold(name, key) && len(values) > 0 {
			return values[0]
		}
	}
	return ""
}

// アダプタが設定するものと同じ名前のヘッダを、クライアントが送信した状態を構成する
func forgedRequestContext(sourceIP string) map[string]string {
	return map[string]string{
		"x-amzn-request-context": `{"identity":{"sourceIp":"` + sourceIP + `"}}`,
	}
}
