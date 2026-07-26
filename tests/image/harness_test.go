//go:build image

// イメージを Lambda として動かすための足場
//
// ここにあるものはすべて手段であって検証の対象ではない
// 何を確かめるかは reconciler_test.go と webconsole_test.go にある
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

// リポジトリルート（docker build のコンテキスト）
const repoRoot = "../.."

// docker CLI で Dockerfile の target をビルドし、そのタグを返す
//
// testcontainers の FromDockerfile を使わないのは、あちらが古い /build API を叩くためである
// この Dockerfile は BuildKit 前提（`# syntax` ディレクティブ、`FROM --platform=$BUILDPLATFORM`、`TARGETOS`/`TARGETARCH`）で、旧ビルダに通すと $BUILDPLATFORM が未定義のまま解釈されて失敗する
// BuildKit を選ぶ version=2 を指定しても、セッション（コンテキストの転送とレジストリ認証）を張るのは docker CLI の仕事なので、クライアントライブラリ単体では "no active sessions" で止まる
// CLI 越しなら `make image-reconciler` / `make image-webconsole` と同じものがビルドされるという利点もある
//
// --platform は付けない
// コンテナはホストの docker で動くので、既定のホストアーキテクチャ向けビルドがそのまま正しい（Makefile の本番既定である arm64 とは異なる）
func buildImage(t *testing.T, target string) string {
	t.Helper()
	image := "cheapskate-" + target + ":imagetest"
	t.Logf("building %s (about 90s from cold; docker's layer cache covers the rest)", image)
	out, err := exec.Command("docker", "build", "--target", target, "-t", image, repoRoot).CombinedOutput()
	require.NoError(t, err, "docker build failed:\n%s", out)
	return image
}

// エミュレータと繋がった状態のイメージに渡す環境変数を、コンテナから見た形で返す
//
// エミュレータはホストにポートを公開している（emutest が AWS_ENDPOINT_URL に入れる）
// コンテナからは host.docker.internal 経由で同じポートに届く（startUnderRIE を参照）
// 本番との差は AWS_ENDPOINT_URL ただ 1 つで、これは AWS SDK が標準で解釈する変数である
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

// RIE 上で動いているイメージと、その呼び出し先
type imageUnderRIE struct {
	container testcontainers.Container
	url       string
}

// RIE が公開する Lambda 呼び出しエンドポイント（関数名は "function" 固定）
const invokePath = "/2015-03-31/functions/function/invocations"

// 本番の Lambda タイムアウト（120 秒, setup.md §5）にコールドスタート分の余裕を足したもの
var invokeClient = &http.Client{Timeout: 150 * time.Second}

// イメージを RIE 越しに起動し、呼び出せる状態になったものを返す
//
// warmup は最初の 1 回を通すために投げるペイロードである
// 何が無害かはイメージごとに違うので呼び出し側が決める（イメージ間で共通の「空の呼び出し」は無い）
func startUnderRIE(t *testing.T, image string, env map[string]string, warmup []byte) imageUnderRIE {
	t.Helper()
	ctx := context.Background()

	req := testcontainers.ContainerRequest{
		Image:        image,
		ExposedPorts: []string{"8080/tcp"},
		// 本番のイメージはエントリポイントを上書きしない（/var/runtime/bootstrap がそのまま動く）
		// ローカルで RIE を挟むときだけ、この 2 行が Lambda ランタイムの代役になる
		Entrypoint: []string{"/usr/local/bin/aws-lambda-rie"},
		Cmd:        []string{"/var/runtime/bootstrap"},
		Env:        env,
		// ホストに公開されたエミュレータへの経路
		// compose と testcontainers のどちらが起動したエミュレータでも同じように届く
		HostConfigModifier: func(hc *container.HostConfig) {
			hc.ExtraHosts = append(hc.ExtraHosts, "host.docker.internal:host-gateway")
		},
		// RIE はランタイムの起動を待たずにポートを開けるので、ここで分かるのは「起動した」ことだけである
		// 呼び出せるようになったかどうかは warm up で確かめる
		WaitingFor: wait.ForListeningPort("8080/tcp").WithStartupTimeout(2 * time.Minute),
	}

	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	require.NoError(t, err, "start %s under the RIE", image)
	// emutest が Ryuk を無効化しているので、後片付けはこちらの責任になる
	t.Cleanup(func() { _ = c.Terminate(context.Background()) })

	host, err := c.Host(ctx)
	require.NoError(t, err)
	port, err := c.MappedPort(ctx, "8080/tcp")
	require.NoError(t, err)

	running := imageUnderRIE{container: c, url: "http://" + host + ":" + port.Port() + invokePath}
	running.warmup(t, warmup)
	return running
}

// ペイロードを 1 つ送り、ハンドラの応答本文を返す
// RIE はハンドラが error を返しても 200 を返す（失敗は本文の errorMessage 側に出る）ので、ステータスコードの確認は「呼び出せたか」の確認にとどまる
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

// コンテナの stderr（ハンドラの JSON ログ）を読む
func (r imageUnderRIE) logs(t *testing.T) string {
	t.Helper()
	stream, err := r.container.Logs(context.Background())
	require.NoError(t, err)
	defer stream.Close()
	logs, err := io.ReadAll(stream)
	require.NoError(t, err)
	return string(logs)
}

// 最初の呼び出しが通るまで待つ
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
