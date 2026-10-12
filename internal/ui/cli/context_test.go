package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 公開エントリポイントを繰り返し呼び、context の作成・更新・選択が起動間で保持されることを確認する。
func TestExecuteContextPersistence(t *testing.T) {
	serveExecuteDynamo(t)
	path := filepath.Join(t.TempDir(), "cheapskate", "config.json")
	t.Setenv("CHEAPSKATE_CONFIG", path)
	t.Setenv("AWS_PROFILE", "missing-profile")
	t.Setenv("DEFAULT_TIMEZONE", "invalid-timezone")
	assert.Contains(t, executeOK(t, "config", "get-contexts"), "CURRENT")
	assert.NoFileExists(t, path)
	executeOK(t, "config", "set-context", "prod", "--table", "prod-table")
	executeOK(t, "config", "set-context", "dev", "--table", "dev-table", "--profile", "dev-profile", "--region", "ap-northeast-1", "--timezone", "Asia/Tokyo")
	executeOK(t, "config", "use-context", "dev")
	executeOK(t, "config", "set-context", "dev", "--region", "us-west-2")
	assert.Equal(t, "dev\n", executeOK(t, "config", "current-context"))

	var saved struct {
		CurrentContext string                       `json:"current_context"`
		Contexts       map[string]map[string]string `json:"contexts"`
	}
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &saved))
	assert.Equal(t, "dev", saved.CurrentContext)
	assert.Equal(t, map[string]string{"table": "dev-table", "profile": "dev-profile", "region": "us-west-2", "timezone": "Asia/Tokyo"}, saved.Contexts["dev"])
	assert.Equal(t, map[string]string{"table": "prod-table"}, saved.Contexts["prod"])
	assert.JSONEq(t, string(data), executeOK(t, "-output", "json", "config", "get-contexts"))
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
	}
	rows := strings.Split(strings.TrimSpace(executeOK(t, "config", "get-contexts")), "\n")
	require.Len(t, rows, 3)
	assert.Equal(t, []string{"*", "dev", "dev-table", "dev-profile", "us-west-2", "Asia/Tokyo"}, strings.Fields(rows[1]))
	assert.Equal(t, []string{"prod", "prod-table"}, strings.Fields(rows[2]))

	executeOK(t, "config", "set-context", "dev", "--profile=", "--region=", "--timezone=")
	assert.JSONEq(t, `{"name":"dev","table":"dev-table"}`, executeOK(t, "-output", "json", "config", "set-context", "dev"))
	assert.JSONEq(t, `{"current_context":"prod"}`, executeOK(t, "-output", "json", "config", "use-context", "prod"))
	assert.JSONEq(t, `{"current_context":"prod"}`, executeOK(t, "-output", "json", "config", "current-context"))
}

// -config で別ファイルへ保存し、環境変数で指定した設定と選択済み context が変わらないことを確認する。
func TestExecuteConfigFileFlag(t *testing.T) {
	serveExecuteDynamo(t)
	executeOK(t, "config", "set-context", "dev", "--table", "dev-table")
	executeOK(t, "config", "use-context", "dev")
	other := filepath.Join(t.TempDir(), "other.json")
	executeOK(t, "-config", other, "config", "set-context", "prod", "--table", "prod-table")
	executeOK(t, "-config", other, "config", "use-context", "prod")
	assert.Equal(t, "prod\n", executeOK(t, "-config", other, "config", "current-context"))
	assert.Equal(t, "dev\n", executeOK(t, "config", "current-context"))
}

// context と従来の環境変数を設定し、HTTP リクエストのテーブル・署名から接続設定の優先順位を確認する。
func TestExecuteUsesContext(t *testing.T) {
	for _, test := range []struct {
		name, current, tableEnv, stateEnv, wantTable, wantKey, wantRegion string
		args                                                              []string
	}{
		{"saved selection", "dev", "env-table", "state-table", "dev-table", "DEV_KEY", "us-west-2", nil},
		{"temporary context", "dev", "env-table", "state-table", "prod-table", "PROD_KEY", "eu-central-1", []string{"-context", "prod"}},
		{"table flag", "dev", "env-table", "state-table", "flag-table", "DEV_KEY", "us-west-2", []string{"-table", "flag-table"}},
		{"switched selection", "prod", "env-table", "state-table", "prod-table", "PROD_KEY", "eu-central-1", nil},
		{"context with SDK defaults", "defaults", "env-table", "state-table", "defaults-table", "test", "ap-northeast-1", nil},
		{"legacy table environment", "", "env-table", "state-table", "env-table", "test", "ap-northeast-1", nil},
		{"legacy state environment", "", "", "state-table", "state-table", "test", "ap-northeast-1", nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			type request struct{ table, authorization string }
			requests := make(chan request, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct{ TableName string }
				assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				requests <- request{body.TableName, r.Header.Get("Authorization")}
				w.Header().Set("Content-Type", "application/x-amz-json-1.0")
				_, _ = io.WriteString(w, `{"Items":[]}`)
			}))
			t.Cleanup(server.Close)
			setExecuteEnvironment(t, server.URL)
			t.Setenv("CHEAPSKATE_TABLE", test.tableEnv)
			t.Setenv("STATE_TABLE_NAME", test.stateEnv)
			credentials := "[dev-profile]\naws_access_key_id=DEV_KEY\naws_secret_access_key=dev-secret\n[prod-profile]\naws_access_key_id=PROD_KEY\naws_secret_access_key=prod-secret\n"
			require.NoError(t, os.WriteFile(os.Getenv("AWS_SHARED_CREDENTIALS_FILE"), []byte(credentials), 0600))
			executeOK(t, "config", "set-context", "dev", "--table", "dev-table", "--profile", "dev-profile", "--region", "us-west-2", "--timezone", "Asia/Tokyo")
			executeOK(t, "config", "set-context", "prod", "--table", "prod-table", "--profile", "prod-profile", "--region", "eu-central-1", "--timezone", "UTC")
			executeOK(t, "config", "set-context", "defaults", "--table", "defaults-table")
			if test.current != "" {
				executeOK(t, "config", "use-context", test.current)
			}
			if test.current == "dev" || test.current == "prod" {
				t.Setenv("DEFAULT_TIMEZONE", "invalid-timezone")
			}
			before, err := os.ReadFile(os.Getenv("CHEAPSKATE_CONFIG"))
			require.NoError(t, err)
			executeOK(t, append(test.args, "list")...)
			select {
			case got := <-requests:
				assert.Equal(t, test.wantTable, got.table)
				assert.Contains(t, got.authorization, "Credential="+test.wantKey+"/")
				assert.Contains(t, got.authorization, "/"+test.wantRegion+"/dynamodb/")
			default:
				t.Fatal("no DynamoDB request received")
			}
			after, err := os.ReadFile(os.Getenv("CHEAPSKATE_CONFIG"))
			require.NoError(t, err)
			assert.Equal(t, before, after)
		})
	}
}

// 不正な引数では終了コード 1 を返し、保存済み設定を変更せず AWS も呼ばないことを確認する。
func TestExecuteRejectsInvalidContexts(t *testing.T) {
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"config"}, "missing config command"},
		{[]string{"config", "unknown"}, "unknown config command"},
		{[]string{"config", "set-context"}, "requires one context name"},
		{[]string{"config", "set-context", "new"}, "--table is required"},
		{[]string{"config", "set-context", "two words", "--table", "table"}, "without whitespace"},
		{[]string{"config", "set-context", "dev\u3000name", "--table", "table"}, "without whitespace"},
		{[]string{"config", "set-context", "dev", "extra"}, "requires one context name"},
		{[]string{"config", "set-context", "dev", "--table="}, "cannot be empty"},
		{[]string{"config", "set-context", "dev", "--timezone", "invalid-timezone"}, "invalid timezone"},
		{[]string{"config", "use-context"}, "requires one context name"},
		{[]string{"config", "use-context", "missing"}, `context "missing" not found`},
		{[]string{"config", "use-context", "dev", "extra"}, "requires one context name"},
		{[]string{"config", "get-contexts", "extra"}, "takes no arguments"},
		{[]string{"config", "current-context", "extra"}, "takes no arguments"},
		{[]string{"config", "current-context"}, "current context not set"},
		{[]string{"-context", "missing", "-table", "table", "list"}, `context "missing" not found`},
		{[]string{"list"}, "state table not set"},
	} {
		t.Run(strings.Join(test.args, " "), func(t *testing.T) {
			serveExecuteDynamo(t)
			executeOK(t, "config", "set-context", "dev", "--table", "dev-table")
			before, err := os.ReadFile(os.Getenv("CHEAPSKATE_CONFIG"))
			require.NoError(t, err)
			var stdout, stderr bytes.Buffer
			assert.Equal(t, 1, Execute(test.args, &stdout, &stderr))
			assert.Empty(t, stdout.String())
			assert.Contains(t, stderr.String(), test.want)
			after, err := os.ReadFile(os.Getenv("CHEAPSKATE_CONFIG"))
			require.NoError(t, err)
			assert.Equal(t, before, after)
		})
	}
}

// 不正な JSON と未定義フィールドを含む設定を読み込み、エラー時にファイルを上書きしないことを確認する。
func TestExecuteRejectsInvalidConfigFile(t *testing.T) {
	for _, content := range []string{
		"", "{", "null", `{} {}`, `{"typo":true}`, `{"contexts":{"dev":{"table":"table","typo":true}}}`,
		`{"current_context": "missing", "contexts": {}}`,
		`{"current_context": "dev", "contexts": {"dev": {}}}`,
	} {
		t.Run(content, func(t *testing.T) {
			serveExecuteDynamo(t)
			path := os.Getenv("CHEAPSKATE_CONFIG")
			require.NoError(t, os.WriteFile(path, []byte(content), 0600))
			var stdout, stderr bytes.Buffer
			assert.Equal(t, 1, Execute([]string{"-table", "table", "list"}, &stdout, &stderr))
			assert.Empty(t, stdout.String())
			assert.NotEmpty(t, stderr.String())
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, content, string(after))
		})
	}
}

// 保存先の置換を失敗させ、既存ファイルを保持し一時ファイルを残さないことを確認する。
func TestSaveCLIConfigFailurePreservesDestination(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	require.NoError(t, os.Mkdir(path, 0700))
	retained := filepath.Join(path, "retained")
	require.NoError(t, os.WriteFile(retained, []byte("keep"), 0600))
	err := saveCLIConfig(path, &cliConfig{Contexts: map[string]cliContext{}})
	require.ErrorContains(t, err, "save CLI config")
	data, err := os.ReadFile(retained)
	require.NoError(t, err)
	assert.Equal(t, "keep", string(data))
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "config.json", entries[0].Name())
}

// コマンドを公開エントリポイントで実行し、成功時の stdout を返す。
func executeOK(t *testing.T, args ...string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	require.Equal(t, 0, Execute(args, &stdout, &stderr), "args: %v; stderr: %s", args, stderr.String())
	require.Empty(t, stderr.String())
	return stdout.String()
}
