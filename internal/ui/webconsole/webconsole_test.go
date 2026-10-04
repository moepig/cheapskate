package webconsole

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"cheapskate/internal/app/port"
	"cheapskate/internal/app/port/porttest"
	"cheapskate/internal/core/model"
	"cheapskate/internal/state"
	statemocks "cheapskate/internal/state/mocks"
)

func webFixture(t *testing.T, allowedHosts ...string) (*statemocks.DynaStore, *porttest.Discoverer, *Server) {
	t.Helper()
	if allowedHosts == nil {
		allowedHosts = []string{"example.com", "console.example"}
	}
	api, db := statemocks.NewDynaStore(gomock.NewController(t))
	discoverer := porttest.NewDiscoverer()
	server := New(state.New(api, "table"), discoverer, nil, "", allowedHosts, time.UTC, nil)
	server.now = func() time.Time { return time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC) }
	return db, discoverer, server
}

func TestIndexContainsOnlySlimForms(t *testing.T) {
	_, _, server := webFixture(t)
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)

	require.Equal(t, http.StatusOK, response.Code)
	body := response.Body.String()
	assert.Contains(t, body, "Create or replace schedule")
	assert.Contains(t, body, "Create indefinite override")
	assert.Contains(t, body, "All schedules and dates use UTC")
	assert.NotEmpty(t, response.Header().Get("Content-Security-Policy"))
}

func TestScheduleAndOverrideFormsUseApplicationService(t *testing.T) {
	db, _, server := webFixture(t)
	postForm(t, server, url.Values{
		"action": {"schedule"}, "group": {"dev"}, "start": {"0 9 * * *"}, "stop": {"0 20 * * *"},
	}, http.StatusSeeOther)
	postForm(t, server, url.Values{
		"action": {"override"}, "group": {"dev"}, "override": {"disabled"}, "until": {"2026-09-03T14:00"},
	}, http.StatusSeeOther)

	item := db.Item("CONFIG", "GROUP#dev")
	assert.Equal(t, "disabled", item["override"].(*types.AttributeValueMemberS).Value)
	assert.Equal(t, "0 9 * * *", item["start_cron"].(*types.AttributeValueMemberS).Value)
	assert.NotNil(t, item["override_expires_at"])
}

func TestGroupDetailRendersScheduleResourcesStateAndBasePath(t *testing.T) {
	api, db := statemocks.NewDynaStore(gomock.NewController(t))
	discoverer := porttest.NewDiscoverer()
	discoverer.Resources = map[string]model.Resource{
		"arn:aws:ecs:ap-northeast-1:123456789012:service/dev/api": {
			Type: model.TypeEcsService, Ref: "dev/api", ARN: "arn:aws:ecs:ap-northeast-1:123456789012:service/dev/api",
			Tags: map[string]string{model.GroupTagKey: "dev", model.EcsDesiredCountTagKey: "2", model.EcsScheduledScalingPausedTagKey: "true"},
		},
	}
	describers := map[model.ResourceType]port.Describer{
		model.TypeEcsService: porttest.Describer{Obs: model.Observation{State: model.StateRunning, Detail: "desiredCount=2 minCapacity=1 maxCapacity=3 ScheduledScalingSuspended=true"}},
	}
	location := time.FixedZone("JST", 9*60*60)
	server := New(state.New(api, "table"), discoverer, describers, "/stage/", []string{"example.com"}, location, nil)
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, location)
	server.now = func() time.Time { return now }
	_, err := server.service.Schedule(context.Background(), "dev", model.ScheduleSpec{StartCron: "0 9 * * *", StopCron: "0 20 * * *"}, now)
	require.NoError(t, err)

	request := httptest.NewRequest(http.MethodGet, "/group?name=dev", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)

	require.Equal(t, http.StatusOK, response.Code)
	body := response.Body.String()
	assert.Contains(t, body, "<h2>dev</h2>")
	assert.Contains(t, body, "cheapskate:group=dev")
	assert.Contains(t, body, "start: 0 9 * * *")
	assert.Contains(t, body, "stop: 0 20 * * *")
	assert.Contains(t, body, "dev/api")
	assert.Contains(t, body, "desired: 2")
	assert.Contains(t, body, "scheduled scaling paused: true")
	assert.Contains(t, body, "running (desiredCount=2 minCapacity=1 maxCapacity=3 ScheduledScalingSuspended=true)")
	assert.Contains(t, body, `action="/stage/op"`)
	assert.Contains(t, body, "All schedules and dates use JST")
	assert.Equal(t, 2, db.Calls("get"))
}

func TestInvalidGroupDoesNotDiscoverResources(t *testing.T) {
	db, discoverer, server := webFixture(t)
	db.Seed(map[string]types.AttributeValue{
		"pk": &types.AttributeValueMemberS{Value: "CONFIG"}, "sk": &types.AttributeValueMemberS{Value: "GROUP#broken"},
		"overide": &types.AttributeValueMemberS{Value: "running"},
	})
	request := httptest.NewRequest(http.MethodGet, "/group?name=broken", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)

	assert.Equal(t, http.StatusBadRequest, response.Code)
	assert.Zero(t, discoverer.Calls())
}

func TestConditionalWriteConflictReturnsHTTP409(t *testing.T) {
	db, _, server := webFixture(t)
	_, err := server.service.Schedule(context.Background(), "dev", model.ScheduleSpec{StartCron: "0 9 * * *", StopCron: "0 20 * * *"}, server.now())
	require.NoError(t, err)
	db.FailOn("update", "group#dev", &types.ConditionalCheckFailedException{})

	postForm(t, server, url.Values{
		"action": {"schedule"}, "group": {"dev"}, "start": {"0 8 * * *"}, "stop": {"0 19 * * *"},
	}, http.StatusConflict)
}

func TestCrossOriginMutationIsRejected(t *testing.T) {
	_, _, server := webFixture(t)
	request := httptest.NewRequest(http.MethodPost, "/op", strings.NewReader("action=remove&group=dev"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", "https://attacker.example")
	request.Host = "console.example"
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	assert.Equal(t, http.StatusForbidden, response.Code)
	assert.Contains(t, response.Body.String(), "cross-origin form submission rejected")
}

func TestSecFetchSiteMutationIsRejected(t *testing.T) {
	_, _, server := webFixture(t)
	request := httptest.NewRequest(http.MethodPost, "/op", strings.NewReader("action=remove&group=dev"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Sec-Fetch-Site", "cross-site")
	request.Header.Set("Origin", "http://example.com")
	request.Host = "example.com"
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	assert.Equal(t, http.StatusForbidden, response.Code)
	assert.Contains(t, response.Body.String(), "cross-origin form submission rejected")
}

// 未許可の Host を各ルートと未登録ルートへ送信し、Origin の一致や転送ヘッダーによらず HTTP 403 となり、読み取りと変更が発生しないことを確認する。
func TestUntrustedHostIsRejectedBeforeRouting(t *testing.T) {
	for _, route := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/"},
		{http.MethodHead, "/"},
		{http.MethodGet, "/group?name=dev"},
		{http.MethodPost, "/op"},
		{http.MethodGet, "/missing"},
		{http.MethodGet, "/group/"},
		{http.MethodOptions, "/op"},
	} {
		for _, host := range []string{"attacker.example", "attacker.example:8080", "example.com.attacker.example", "sub.example.com", "example.com:8080", ""} {
			for _, origin := range []string{"", "http://" + host} {
				t.Run(route.method+" "+route.path+" host="+host+" origin="+origin, func(t *testing.T) {
					db, discoverer, server := webFixture(t)
					request := httptest.NewRequest(route.method, route.path, strings.NewReader("action=override&group=dev&override=running"))
					request.Host = host
					request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
					request.Header.Set("Origin", origin)
					request.Header.Set("Sec-Fetch-Site", "same-origin")
					request.Header.Set("X-Forwarded-Host", "example.com")
					request.Header.Set("Forwarded", "host=example.com")
					response := httptest.NewRecorder()
					server.Handler().ServeHTTP(response, request)

					assert.Equal(t, http.StatusForbidden, response.Code)
					assert.Contains(t, response.Body.String(), "request host rejected")
					assert.NotEmpty(t, response.Header().Get("Content-Security-Policy"))
					for _, operation := range []string{"query", "get", "put", "update", "delete"} {
						assert.Zero(t, db.Calls(operation), operation)
					}
					assert.Zero(t, discoverer.Calls())
				})
			}
		}
	}
}

// 許可したホスト名、IPv4、IPv6、ポート付き Host で一覧を取得し、同じ Origin から running override を登録できることを確認する。
func TestConfiguredHostsAllowReadsAndMutations(t *testing.T) {
	for _, host := range []string{"localhost:8080", "127.0.0.1:8080", "[::1]:8080", "console.example", "CONSOLE.EXAMPLE", "other.example:443"} {
		t.Run(host, func(t *testing.T) {
			db, _, server := webFixture(t, "localhost:8080", "127.0.0.1:8080", "[::1]:8080", " console.EXAMPLE ", "other.example:443")
			request := httptest.NewRequest(http.MethodGet, "http://"+host+"/", nil)
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)
			require.Equal(t, http.StatusOK, response.Code)

			request = httptest.NewRequest(http.MethodPost, "http://"+host+"/op", strings.NewReader("action=override&group=dev&override=running"))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			request.Header.Set("Origin", "http://"+host)
			response = httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)
			require.Equal(t, http.StatusSeeOther, response.Code)
			assert.Equal(t, "running", db.Item("CONFIG", "GROUP#dev")["override"].(*types.AttributeValueMemberS).Value)
		})
	}
}

// 許可ホストが nil、空、空白だけの場合、Origin が Host と一致しても全要求を拒否することを確認する。
func TestEmptyAllowedHostsRejectAllRequests(t *testing.T) {
	for _, hosts := range [][]string{nil, {}, {"", " "}} {
		server := New(nil, nil, nil, "", hosts, time.UTC, nil)
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			request := httptest.NewRequest(method, "/", nil)
			request.Header.Set("Origin", "http://example.com")
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)
			assert.Equal(t, http.StatusForbidden, response.Code)
		}
	}
}

// 生成後に渡したスライスを書き換えても、許可ホストが変更されないことを確認する。
func TestAllowedHostsAreCopiedAtConstruction(t *testing.T) {
	hosts := []string{"example.com"}
	_, _, server := webFixture(t, hosts...)
	hosts[0] = "attacker.example"
	for _, test := range []struct {
		host   string
		status int
	}{
		{"example.com", http.StatusOK},
		{"attacker.example", http.StatusForbidden},
	} {
		request := httptest.NewRequest(http.MethodGet, "http://"+test.host+"/", nil)
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		assert.Equal(t, test.status, response.Code)
	}
}

func TestClearOverrideAndRemoveForms(t *testing.T) {
	db, _, server := webFixture(t)
	postForm(t, server, url.Values{"action": {"schedule"}, "group": {"dev"}, "start": {"0 9 * * *"}, "stop": {"0 20 * * *"}}, http.StatusSeeOther)
	postForm(t, server, url.Values{"action": {"override"}, "group": {"dev"}, "override": {"stopped"}}, http.StatusSeeOther)
	postForm(t, server, url.Values{"action": {"clear-override"}, "group": {"dev"}}, http.StatusSeeOther)
	assert.Nil(t, db.Item("CONFIG", "GROUP#dev")["override"])
	postForm(t, server, url.Values{"action": {"remove"}, "group": {"dev"}}, http.StatusSeeOther)
	assert.Nil(t, db.Item("CONFIG", "GROUP#dev"))
}

func TestRenderedValuesAreEscaped(t *testing.T) {
	assert.NotContains(t, string(describeGroup(model.GroupSpec{StartCron: `<script>`, StopCron: `</script>`})), "<script>")
	assert.NotContains(t, string(describeResourceConfig(model.Resource{Type: model.TypeEcsService, Tags: map[string]string{model.EcsDesiredCountTagKey: `<img>`}})), "<img>")
}

func TestSourceIP(t *testing.T) {
	assert.Equal(t, "192.0.2.10", sourceIP(`{"identity":{"sourceIp":"192.0.2.10"}}`))
	assert.Equal(t, "192.0.2.20", sourceIP(`{"http":{"sourceIp":"192.0.2.20"}}`))
	assert.Empty(t, sourceIP("invalid"))
}

func postForm(t *testing.T, server *Server, values url.Values, wantStatus int) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/op", strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", "http://example.com")
	request.Host = "example.com"
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	assert.Equal(t, wantStatus, response.Code, response.Body.String())
}
