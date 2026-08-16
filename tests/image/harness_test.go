//go:build image

// イメージを Lambda として実行するための基盤
//
// 本ファイルの内容はすべて実行の手段であり、検証の対象ではない
// 検証の内容は reconciler_test.go と webconsole_test.go にある
package image

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// リポジトリルート (docker build のコンテキスト)
const repoRoot = "../.."

// docker CLI で Dockerfile の target をビルドし、そのタグを返す
//
// testcontainers の FromDockerfile を用いないのは、それが旧来の /build API を呼ぶためである
// この Dockerfile は BuildKit を前提とし、`# syntax` ディレクティブ、`FROM --platform=$BUILDPLATFORM`、`TARGETOS`/`TARGETARCH` を用いる
// 旧ビルダで解釈した場合、$BUILDPLATFORM が未定義のまま処理され、失敗する
// BuildKit を選択する version=2 を指定した場合も、コンテキストの転送とレジストリ認証を行うセッションの確立は docker CLI のみが行うため、クライアントライブラリ単体では "no active sessions" となる
// CLI を経由することにより、`make image-reconciler` と `make image-webconsole` と同一のイメージをビルドする
//
// --platform は指定しない
// コンテナはホストの docker 上で動作するため、既定のホストアーキテクチャ向けのビルドを用いる (Makefile の本番既定である arm64 とは異なる)
func buildImage(t *testing.T, target string) string {
	t.Helper()
	image := "cheapskate-" + target + ":imagetest"
	t.Logf("building %s (about 90s from cold; docker's layer cache covers the rest)", image)
	out, err := exec.Command("docker", "build", "--target", target, "-t", image, repoRoot).CombinedOutput()
	require.NoError(t, err, "docker build failed:\n%s", out)
	return image
}

// エミュレータへ接続した状態のイメージへ渡す環境変数を、コンテナから見た形式で返す
//
// エミュレータはホストへポートを公開する (emutest が AWS_ENDPOINT_URL へ設定する)
// コンテナからは host.docker.internal を経由して同じポートへ到達する (startUnderRIE を参照)
// 本番との差異は AWS_ENDPOINT_URL の 1 つであり、これは AWS SDK が標準で解釈する変数である
func emulatorEnv(t *testing.T, table string) map[string]string {
	t.Helper()
	endpoint, err := url.Parse(os.Getenv("AWS_ENDPOINT_URL"))
	require.NoError(t, err)
	return map[string]string{
		"STATE_TABLE_NAME":      table,
		"AWS_ENDPOINT_URL":      "http://host.docker.internal:" + endpoint.Port(),
		"AWS_ACCESS_KEY_ID":     os.Getenv("AWS_ACCESS_KEY_ID"),
		"AWS_SECRET_ACCESS_KEY": os.Getenv("AWS_SECRET_ACCESS_KEY"),
		"AWS_REGION":            os.Getenv("AWS_REGION"),
		"DEFAULT_TIMEZONE":      "Asia/Tokyo",
	}
}

// RIE 上で動作するイメージと、その呼び出し先
type imageUnderRIE struct {
	container testcontainers.Container
	url       string
}

// RIE が公開する Lambda 呼び出しエンドポイント (関数名は "function" で固定である)
const invokePath = "/2015-03-31/functions/function/invocations"

// 本番の Lambda タイムアウト (120 秒、setup.md §5) に、コールドスタートの所要時間を加えたもの
var invokeClient = &http.Client{Timeout: 150 * time.Second}

// イメージを RIE 経由で起動し、呼び出し可能となった状態で返す
//
// warmup は、最初の 1 回を通すために投入するペイロードである
// 状態を変更しないペイロードはイメージごとに異なるため、呼び出し側が指定する
func startUnderRIE(t *testing.T, image string, env map[string]string, warmup []byte) imageUnderRIE {
	t.Helper()
	ctx := context.Background()

	req := testcontainers.ContainerRequest{
		Image:        image,
		ExposedPorts: []string{"8080/tcp"},
		// 本番のイメージはエントリポイントを上書きしない (/var/runtime/bootstrap をそのまま実行する)
		// ローカルで RIE を経由する場合に限り、この 2 行が Lambda ランタイムを代替する
		Entrypoint: []string{"/usr/local/bin/aws-lambda-rie"},
		Cmd:        []string{"/var/runtime/bootstrap"},
		Env:        env,
		// ホストへ公開したエミュレータへの経路
		// compose と testcontainers のいずれが起動したエミュレータに対しても同じく到達する
		HostConfigModifier: func(hc *container.HostConfig) {
			hc.ExtraHosts = append(hc.ExtraHosts, "host.docker.internal:host-gateway")
		},
		// RIE はランタイムの起動を待たずにポートを開くため、ここで判定できるのは起動の完了までである
		// 呼び出しが可能となったかどうかは warm up で確かめる
		WaitingFor: wait.ForListeningPort("8080/tcp").WithStartupTimeout(2 * time.Minute),
	}

	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	require.NoError(t, err, "start %s under the RIE", image)
	// emutest が Ryuk を無効化するため、コンテナの削除は本ファイルが行う
	t.Cleanup(func() { _ = c.Terminate(context.Background()) })

	host, err := c.Host(ctx)
	require.NoError(t, err)
	port, err := c.MappedPort(ctx, "8080/tcp")
	require.NoError(t, err)

	running := imageUnderRIE{container: c, url: "http://" + host + ":" + port.Port() + invokePath}
	running.warmup(t, warmup)
	return running
}

// ペイロードを 1 つ送信し、ハンドラの応答本文を返す
// RIE はハンドラが error を返した場合も 200 を返し、失敗は本文の errorMessage に現れる
// したがってステータスコードの検査は、呼び出しの成否の確認にとどまる
func (r imageUnderRIE) invoke(t *testing.T, payload []byte) []byte {
	t.Helper()
	resp, err := invokeClient.Post(r.url, "application/json", bytes.NewReader(payload))
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "invoke failed: %s", body)
	return body
}

// コンテナの stderr (ハンドラの JSON ログ) を読む
func (r imageUnderRIE) logs(t *testing.T) string {
	t.Helper()
	stream, err := r.container.Logs(context.Background())
	require.NoError(t, err)
	defer stream.Close()
	logs, err := io.ReadAll(stream)
	require.NoError(t, err)
	return string(logs)
}

// 最初の呼び出しが成功するまで待機する
func (r imageUnderRIE) warmup(t *testing.T, payload []byte) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for {
		resp, err := invokeClient.Post(r.url, "application/json", bytes.NewReader(payload))
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no answer from %s within the deadline (last error: %v)", r.url, err)
		}
		time.Sleep(time.Second)
	}
}
