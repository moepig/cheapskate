// 結合テストをローカルの AWS エミュレータ (Floci) へ接続する
// エミュレータは testcontainers-go を通じて必要に応じて起動する
// 接続先の指定には AWS_ENDPOINT_URL 環境変数のみを用い、本番コードはエミュレータを参照しない
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

// Ryuk による回収の単位は testcontainers セッション全体であり、`go test ./...` の全テストバイナリにまたがる
// したがって、バイナリ間で共有するコンテナの回収を Ryuk に委ねられない (ContainerName を参照)
// testcontainers が設定を読み込む前に無効化する
// 設定の読み込みは最初のコンテナ呼び出し時に遅延して行われるため、パッケージの init より後となる
func init() {
	if _, set := os.LookupEnv("TESTCONTAINERS_RYUK_DISABLED"); !set {
		_ = os.Setenv("TESTCONTAINERS_RYUK_DISABLED", "true")
	}
}

// 共有するエミュレータコンテナの固定名
// `go test ./...` はパッケージごとに 1 バイナリを実行するため、バイナリ単位のコンテナでは 3 つのエミュレータが同一のマシン上で並行して動作する
// これに加え、各エミュレータが起動する RDS/ECS のコンテナも動作する
// さらに、これらのバイナリは単一の testcontainers セッション、すなわち単一の Ryuk reaper を共有する
// Ryuk はクライアント数が 0 となった時点でセッション全体を回収する
// したがって、最初に終了したバイナリが他のバイナリのエミュレータも回収し、テスト中の "connection refused" として現れる
// コンテナを 1 つ共有することにより、この両方を回避する
const ContainerName = "cheapskate-itest-floci"

// 共有の Floci コンテナを起動する。並行するテストバイナリまたは過去の実行が起動済みの場合は、それへ接続する
// init() で Ryuk を無効化するため、回収は発生しない
// コンテナはテストセッションより長く存続し、次のセッションが再利用する
// 削除には `make floci-down` を用いる
func startFloci(ctx context.Context) (string, error) {
	req := testcontainers.ContainerRequest{
		Name:         ContainerName,
		Image:        "floci/floci:latest",
		ExposedPorts: []string{"4566/tcp"},
		WaitingFor:   wait.ForHTTP("/_localstack/health").WithPort("4566/tcp").WithStartupTimeout(2 * time.Minute),
		ConfigModifier: func(c *container.Config) {
			c.User = "root"
		},
		// RDS/ECS のエミュレーションはコンテナを起動するため、docker ソケットを要する (compose.yaml と同じ)
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

// 共有の Floci コンテナを起動または既存のものへ接続し、それを指す AWS の設定を返す
// テストは作成するすべてのリソースへ名前空間を与える (RandomName)
// したがって、1 つのエミュレータをバイナリと実行の間で共有しても、互いに干渉しない
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

// 指定した接頭辞を持つ一意な名前を返す
func RandomName(prefix string) string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return prefix + "-" + hex.EncodeToString(b)
}

// 使い捨ての state テーブルを作成し、その削除を後処理として登録する。スキーマは internal/state が定める
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
