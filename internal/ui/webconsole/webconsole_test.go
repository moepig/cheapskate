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

	"cheapskate/internal/app/port/porttest"
	"cheapskate/internal/core/model"
	"cheapskate/internal/state"
	statemocks "cheapskate/internal/state/mocks"
)

func webFixture(t *testing.T) (*statemocks.DynaStore, *porttest.Discoverer, *Server) {
	t.Helper()
	api, db := statemocks.NewDynaStore(gomock.NewController(t))
	discoverer := porttest.NewDiscoverer()
	server := New(state.New(api, "table"), discoverer, nil, "", time.UTC, nil)
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
