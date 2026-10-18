package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cheapskate/internal/app/groups"
	"cheapskate/internal/core/model"
)

const executeValidItem = `{"pk":{"S":"CONFIG"},"sk":{"S":"GROUP#dev"},"start_cron":{"S":"0 9 * * *"},"stop_cron":{"S":"0 20 * * *"}}`
const executeInvalidItem = `{"pk":{"S":"CONFIG"},"sk":{"S":"GROUP#broken"},"unknown":{"S":"value"}}`

// SDK の HTTP 境界で保存データを返し、公開エントリポイントの終了コードと出力先を確認する。
func TestExecuteListAndShowExitCodes(t *testing.T) {
	for _, test := range []struct {
		name, command, format string
		response              dynamoResponse
		code                  int
		stderr                string
		broken                bool
	}{
		{"list JSON success", "list", "json", dynamoResponse{"Query", `{"Items":[` + executeValidItem + `]}`, 200}, 0, "", false},
		{"list text success", "list", "text", dynamoResponse{"Query", `{"Items":[` + executeValidItem + `]}`, 200}, 0, "", false},
		{"list JSON config error", "list", "json", dynamoResponse{"Query", `{"Items":[` + executeValidItem + `,` + executeInvalidItem + `]}`, 200}, 2, "", true},
		{"list text config error", "list", "text", dynamoResponse{"Query", `{"Items":[` + executeValidItem + `,` + executeInvalidItem + `]}`, 200}, 2, "broken", true},
		{"show JSON config error", "show", "json", dynamoResponse{"GetItem", `{"Item":` + executeInvalidItem + `}`, 200}, 2, "", true},
		{"show text config error", "show", "text", dynamoResponse{"GetItem", `{"Item":` + executeInvalidItem + `}`, 200}, 2, "broken", true},
		{"AWS error", "list", "json", dynamoResponse{"Query", `{"__type":"AccessDeniedException","message":"denied"}`, 400}, 1, "denied", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			serveExecuteDynamo(t, test.response)
			args := []string{"-table", "table", "-o", test.format, test.command}
			if test.command == "show" {
				args = append(args, "--group", "broken")
			}
			var stdout, stderr bytes.Buffer
			assert.Equal(t, test.code, Execute(args, &stdout, &stderr))
			if test.stderr == "" {
				assert.Empty(t, stderr.String())
			} else {
				assert.Contains(t, stderr.String(), test.stderr)
				assert.Equal(t, 1, strings.Count(stderr.String(), "\n"), "errors must be reported once")
			}
			if test.code == 1 || test.command == "show" && test.format == "text" {
				assert.Empty(t, stdout.String())
			} else if test.format == "json" {
				decoder := json.NewDecoder(&stdout)
				var result map[string]json.RawMessage
				require.NoError(t, decoder.Decode(&result))
				var extra any
				assert.ErrorIs(t, decoder.Decode(&extra), io.EOF, "stdout must contain exactly one complete JSON object")
				if test.command == "show" {
					var configError map[string]string
					require.NoError(t, json.Unmarshal(result["error"], &configError))
					assert.Equal(t, "broken", configError["group"])
					assert.NotEmpty(t, configError["message"])
					assert.NotContains(t, result, "resources")
				} else {
					var groups []groupJSON
					var errors []configErrorJSON
					require.NoError(t, json.Unmarshal(result["groups"], &groups))
					require.NoError(t, json.Unmarshal(result["errors"], &errors))
					require.Len(t, groups, 1)
					assert.Equal(t, "dev", groups[0].Name)
					if test.broken {
						require.Len(t, errors, 1)
						assert.Equal(t, "broken", errors[0].Group)
					} else {
						assert.Empty(t, errors)
					}
				}
			} else {
				assert.Contains(t, stdout.String(), "dev")
				assert.NotContains(t, stdout.String(), "broken")
			}
		})
	}
}

// 読み取り後の条件付き書き込みを失敗させ、各変更コマンドが終了コード 2 を返すことを確認する。
func TestExecuteReportsWriteConflicts(t *testing.T) {
	for _, command := range []struct {
		args      []string
		operation string
	}{
		{[]string{"schedule", "--group", "dev", "-start", "0 8 * * *", "-stop", "0 19 * * *"}, "UpdateItem"},
		{[]string{"override", "--group", "dev", "running"}, "UpdateItem"},
		{[]string{"clear-override", "--group", "dev"}, "UpdateItem"},
		{[]string{"ecs-max-count", "--group", "dev", "3"}, "UpdateItem"},
		{[]string{"clear-ecs-max-count", "--group", "dev"}, "UpdateItem"},
		{[]string{"remove", "--group", "dev"}, "DeleteItem"},
	} {
		for _, format := range []string{"json", "text"} {
			t.Run(command.args[0]+"/"+format, func(t *testing.T) {
				serveExecuteDynamo(t,
					dynamoResponse{"GetItem", `{"Item":` + executeValidItem + `}`, 200},
					dynamoResponse{command.operation, `{"__type":"ConditionalCheckFailedException","message":"conflict"}`, 400},
				)
				var stdout, stderr bytes.Buffer
				args := append([]string{"-table", "table", "-output", format}, command.args...)
				assert.Equal(t, 2, Execute(args, &stdout, &stderr))
				assert.Empty(t, stdout.String())
				assert.Contains(t, stderr.String(), "configuration conflict")
				assert.Equal(t, 1, strings.Count(stderr.String(), "\n"))
			})
		}
	}
}

// サービス生成を禁止し、引数エラーとヘルプの終了コードと stderr を確認する。
func TestExecuteArgumentErrorsAndHelp(t *testing.T) {
	for _, test := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{"-h"}, 0, Usage},
		{nil, 1, "missing command"},
		{[]string{"-output", "yaml", "list"}, 1, "invalid -output"},
		{[]string{"-o=yaml", "list"}, 1, "invalid -output"},
	} {
		t.Run(test.want, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			createService := func(context.Context, serviceOptions) (*groups.Service, error) {
				t.Fatal("unexpected service creation")
				return nil, nil
			}
			assert.Equal(t, test.code, execute(test.args, &stdout, &stderr, createService))
			assert.Empty(t, stdout.String())
			assert.Contains(t, stderr.String(), test.want)
		})
	}
}

// 選択済み context とテーブル指定を生成関数へ渡し、返されたサービスでコマンドを実行する。
func TestExecuteUsesServiceFactory(t *testing.T) {
	setCLIEnvironment(t)
	path, err := cliConfigPath("")
	require.NoError(t, err)
	require.NoError(t, saveCLIConfig(path, &cliConfig{
		CurrentContext: "dev",
		Contexts: map[string]cliContext{
			"dev": {Table: "context-table", Profile: "dev-profile", Region: "us-west-2", Timezone: "Asia/Tokyo"},
		},
	}))
	_, service := cliFixture(t)
	_, err = service.Override(context.Background(), "dev", model.OverrideRunning, 0, time.Now())
	require.NoError(t, err)
	var calls int
	createService := func(ctx context.Context, options serviceOptions) (*groups.Service, error) {
		calls++
		assert.NoError(t, ctx.Err())
		assert.Equal(t, "flag-table", options.table)
		assert.Equal(t, "dev-profile", options.profile)
		assert.Equal(t, "us-west-2", options.region)
		assert.Equal(t, "Asia/Tokyo", options.location.String())
		return service, nil
	}
	var stdout, stderr bytes.Buffer

	code := execute([]string{"-table", "flag-table", "-o", "json", "list"}, &stdout, &stderr, createService)

	assert.Equal(t, 0, code)
	assert.Equal(t, 1, calls)
	assert.Empty(t, stderr.String())
	assert.JSONEq(t, `{"groups":[{"name":"dev","override":"running"}],"errors":[]}`, stdout.String())
}

// サービス生成を失敗させ、コマンドを実行せず終了コード 1 とエラー 1 行を返すことを確認する。
func TestExecuteReportsServiceFactoryFailure(t *testing.T) {
	setCLIEnvironment(t)
	createService := func(context.Context, serviceOptions) (*groups.Service, error) {
		return nil, errors.New("service unavailable")
	}
	var stdout, stderr bytes.Buffer

	code := execute([]string{"-table", "table", "list"}, &stdout, &stderr, createService)

	assert.Equal(t, 1, code)
	assert.Empty(t, stdout.String())
	assert.Equal(t, "service unavailable\n", stderr.String())
}

// サービス生成を禁止し、context の保存が成功することを確認する。
func TestExecuteConfigSkipsServiceCreation(t *testing.T) {
	setCLIEnvironment(t)
	createService := func(context.Context, serviceOptions) (*groups.Service, error) {
		t.Fatal("unexpected service creation")
		return nil, nil
	}
	var stdout, stderr bytes.Buffer

	code := execute([]string{"config", "set-context", "dev", "--table", "table"}, &stdout, &stderr, createService)

	assert.Equal(t, 0, code)
	assert.Empty(t, stderr.String())
	path, err := cliConfigPath("")
	require.NoError(t, err)
	saved, err := loadCLIConfig(path)
	require.NoError(t, err)
	assert.Equal(t, cliContext{Table: "table"}, saved.Contexts["dev"])
}

type dynamoResponse struct {
	operation string
	body      string
	status    int
}

// AWS 設定をローカル HTTP サーバへ向け、指定した順序の応答だけを返す。
func serveExecuteDynamo(t *testing.T, responses ...dynamoResponse) {
	t.Helper()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		index := int(calls.Add(1)) - 1
		if index >= len(responses) {
			t.Errorf("unexpected AWS request: %s", r.Header.Get("X-Amz-Target"))
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		response := responses[index]
		assert.Equal(t, "DynamoDB_20120810."+response.operation, r.Header.Get("X-Amz-Target"))
		w.Header().Set("Content-Type", "application/x-amz-json-1.0")
		w.WriteHeader(response.status)
		_, _ = io.WriteString(w, response.body)
	}))
	t.Cleanup(func() {
		server.Close()
		assert.Equal(t, len(responses), int(calls.Load()))
	})
	setExecuteEnvironment(t, server.URL)
}

// AWS と CLI の設定をテストごとに分離し、AWS 接続先を指定した HTTP サーバに限定する。
func setExecuteEnvironment(t *testing.T, endpoint string) {
	t.Helper()
	setCLIEnvironment(t)
	t.Setenv("AWS_ENDPOINT_URL", endpoint)
	t.Setenv("AWS_ENDPOINT_URL_DYNAMODB", endpoint)
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_SESSION_TOKEN", "")
	t.Setenv("AWS_REGION", "ap-northeast-1")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(t.TempDir(), "config"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(t.TempDir(), "credentials"))
	t.Setenv("AWS_PROFILE", "")
}

// CLI の設定ファイルと環境変数をテストごとに分離する。
func setCLIEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("CHEAPSKATE_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("CHEAPSKATE_TABLE", "")
	t.Setenv("STATE_TABLE_NAME", "")
	t.Setenv("DEFAULT_TIMEZONE", "UTC")
}
