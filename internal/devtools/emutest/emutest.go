// 結合テストをローカルの AWS エミュレータ（Floci）へ接続する
// エミュレータは testcontainers-go 経由で必要に応じて起動する
// 接続先の指定には標準の AWS_ENDPOINT_URL 環境変数だけを使い、本番コードはエミュレータの存在を知らない
package emutest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/moby/moby/api/types/container"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"cheapskate/internal/state"
)

var (
	startOnce sync.Once
	endpoint  string
	startErr  error
)

// Ryuk の掃除の単位は testcontainers セッション全体であり、`go test ./...` の全テストバイナリにまたがる
// そのため、それらのバイナリ間で共有するコンテナを Ryuk に任せることはできない（ContainerName を参照）
// testcontainers が設定を読む前に無効化する
// 設定の読み込みは最初のコンテナ呼び出し時に遅延して行われるので、必ずパッケージの init より後になる
func init() {
	if _, set := os.LookupEnv("TESTCONTAINERS_RYUK_DISABLED"); !set {
		_ = os.Setenv("TESTCONTAINERS_RYUK_DISABLED", "true")
	}
}

// 共有するエミュレータコンテナの固定名
// `go test ./...` はパッケージごとに 1 バイナリを走らせるので、バイナリ単位のコンテナでは 3 つのエミュレータが同じマシンを取り合うことになる
// さらに各エミュレータが実際に起動する RDS/ECS のコンテナも加わる
// 悪いことに、それらのバイナリは単一の testcontainers セッション、ひいては単一の Ryuk reaper を共有する
// Ryuk はクライアント数が 0 になった時点でセッション全体を刈り取る
// つまり最初に終わったバイナリが、まだ走っている他のバイナリのエミュレータまで持っていき、テスト中の "connection refused" として表面化した
// コンテナを 1 つ使い回すことで、この両方の問題を回避できる
const ContainerName = "cheapskate-itest-floci"

// 共有の Floci コンテナを起動する、あるいは並行するテストバイナリや過去の実行がすでに起動したものへ接続する
// init() で Ryuk を無効化しているので刈り取られることはない
// テストセッションより長く生き残り、次のセッションがそれを拾う
// 削除するには `make floci-down` を使う
func startFloci(ctx context.Context) (string, error) {
	req := testcontainers.ContainerRequest{
		Name:         ContainerName,
		Image:        "floci/floci:latest",
		ExposedPorts: []string{"4566/tcp"},
		WaitingFor:   wait.ForHTTP("/_localstack/health").WithPort("4566/tcp").WithStartupTimeout(2 * time.Minute),
		ConfigModifier: func(c *container.Config) {
			c.User = "root"
		},
		// RDS/ECS のエミュレーションは実際のコンテナを動かすので docker ソケットが必要になる（compose.yaml と同じ）
		HostConfigModifier: func(hc *container.HostConfig) {
			hc.Binds = append(hc.Binds, "/var/run/docker.sock:/var/run/docker.sock")
		},
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
		Reuse:            true,
	})
	if err != nil {
		return "", fmt.Errorf("start floci container: %w", err)
	}

	host, err := c.Host(ctx)
	if err != nil {
		return "", err
	}
	port, err := c.MappedPort(ctx, "4566/tcp")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("http://%s:%s", host, port.Port()), nil
}

// 共有の Floci コンテナを起動、または既存のものへ接続し、そこを指す AWS の設定を返す
// テストは作成するリソースすべてに名前空間を与える（RandomName）
// そのため 1 つのエミュレータをバイナリや実行の間で共有しても、互いに独立していられる
func Config(t *testing.T) aws.Config {
	t.Helper()
	startOnce.Do(func() {
		endpoint, startErr = startFloci(context.Background())
	})
	if startErr != nil {
		t.Fatalf("start floci: %v", startErr)
	}

	t.Setenv("AWS_ENDPOINT_URL", endpoint)
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_REGION", "ap-northeast-1")

	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		t.Fatalf("load AWS config: %v", err)
	}
	return cfg
}

// 与えられた接頭辞を持つ一意な名前を返す
func RandomName(prefix string) string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return prefix + "-" + hex.EncodeToString(b)
}

// 使い捨ての state テーブル（スキーマは internal/state）を作成し、その削除を後片付けとして登録する
func CreateStateTable(t *testing.T, cfg aws.Config) string {
	t.Helper()
	ctx := context.Background()
	db := dynamodb.NewFromConfig(cfg)
	name := RandomName("cheapskate-itest")

	if err := state.CreateTable(ctx, db, name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.DeleteTable(ctx, &dynamodb.DeleteTableInput{TableName: &name})
	})
	return name
}
