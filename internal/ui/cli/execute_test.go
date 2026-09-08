package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
			args := []string{"-table", "table", "-output", test.format, test.command}
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

// 引数エラーとヘルプは AWS を呼ばず、終了コードと stderr で結果を返す。
func TestExecuteArgumentErrorsAndHelp(t *testing.T) {
	for _, test := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{"-h"}, 0, Usage},
		{nil, 1, "missing command"},
		{[]string{"-output", "yaml", "list"}, 1, "invalid -output"},
	} {
		t.Run(test.want, func(t *testing.T) {
			serveExecuteDynamo(t)
			var stdout, stderr bytes.Buffer
			assert.Equal(t, test.code, Execute(test.args, &stdout, &stderr))
			assert.Empty(t, stdout.String())
			assert.Contains(t, stderr.String(), test.want)
		})
	}
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
	t.Setenv("AWS_ENDPOINT_URL", server.URL)
	t.Setenv("AWS_ENDPOINT_URL_DYNAMODB", server.URL)
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_SESSION_TOKEN", "")
	t.Setenv("AWS_REGION", "ap-northeast-1")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(t.TempDir(), "config"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(t.TempDir(), "credentials"))
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("DEFAULT_TIMEZONE", "UTC")
}
