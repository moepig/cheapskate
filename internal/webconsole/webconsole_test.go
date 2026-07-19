package webconsole

import (
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

	"cheapskate/internal/mocks"
	"cheapskate/internal/model"
	"cheapskate/internal/store"
)

func newTestServer(t *testing.T) (*mocks.DynaStore, http.Handler) {
	t.Helper()
	ctrl := gomock.NewController(t)
	api, db := mocks.NewDynaStore(ctrl)
	seedDevTag(db)
	return db, New(store.New(api, "t"), "", time.UTC).Handler()
}

func seedDevTag(db *mocks.DynaStore) {
	db.Seed(map[string]types.AttributeValue{
		"pk": &types.AttributeValueMemberS{Value: "tag#dev"}, "mode": &types.AttributeValueMemberS{Value: model.ModePinned},
		"desired": &types.AttributeValueMemberS{Value: model.DesiredStopped},
	})
	db.Seed(map[string]types.AttributeValue{
		"pk": &types.AttributeValueMemberS{Value: "member#rds-instance#db1"}, "tag": &types.AttributeValueMemberS{Value: "dev"},
		"type": &types.AttributeValueMemberS{Value: model.TypeRdsInstance},
	})
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return rec
}

func post(t *testing.T, h http.Handler, form url.Values, header map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/op", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestIndexListsTagsAndMembers(t *testing.T) {
	_, h := newTestServer(t)
	rec := get(t, h, "/")
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "dev", "index does not list the seeded tag")
	assert.Contains(t, body, "rds-instance#db1", "index does not list the seeded member")
}

func TestTagPage(t *testing.T) {
	_, h := newTestServer(t)
	rec := get(t, h, "/tag?name=dev")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rec = get(t, h, "/tag?name=ghost")
	assert.Equal(t, http.StatusNotFound, rec.Code, "unregistered tag")

	rec = get(t, h, "/tag?name="+url.QueryEscape("bad name"))
	assert.Equal(t, http.StatusBadRequest, rec.Code, "malformed tag name")
}

func TestOpPin(t *testing.T) {
	db, h := newTestServer(t)
	rec := post(t, h, url.Values{"action": {"pin"}, "tag": {"dev"}, "desired": {"running"}}, nil)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())

	item := db.Item("tag#dev")
	require.NotNil(t, item)
	assert.Equal(t, "running", item["desired"].(*types.AttributeValueMemberS).Value)
	assert.Contains(t, rec.Header().Get("Location"), "msg=")
}

func TestOpOverrideAndClear(t *testing.T) {
	db, h := newTestServer(t)
	rec := post(t, h, url.Values{"action": {"override"}, "tag": {"dev"}, "desired": {"running"}, "for": {"2h"}}, nil)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	assert.NotNil(t, db.Item("override#dev"), "override item not written")

	rec = post(t, h, url.Values{"action": {"clear-override"}, "tag": {"dev"}}, nil)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Nil(t, db.Item("override#dev"), "override item not deleted")
}

func TestOpValidationErrorRedirects(t *testing.T) {
	_, h := newTestServer(t)
	rec := post(t, h, url.Values{"action": {"schedule"}, "tag": {"dev"}, "start": {"not a cron"}}, nil)
	require.Equal(t, http.StatusSeeOther, rec.Code, "invalid schedule must still redirect with err")
	assert.Contains(t, rec.Header().Get("Location"), "err=")
}

// A-8: a successful schedule op must persist the crons and mode, and redirect with a msg.
func TestOpScheduleSuccess(t *testing.T) {
	db, h := newTestServer(t)
	rec := post(t, h, url.Values{
		"action": {"schedule"}, "tag": {"dev"},
		"start": {"0 9 * * *"}, "stop": {"0 20 * * *"}, "timezone": {"UTC"},
	}, nil)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())

	item := db.Item("tag#dev")
	require.NotNil(t, item, "tag item not written")
	assert.Equal(t, model.ModeSchedule, item["mode"].(*types.AttributeValueMemberS).Value)
	assert.Equal(t, "0 9 * * *", item["start_cron"].(*types.AttributeValueMemberS).Value)
	assert.Contains(t, rec.Header().Get("Location"), "msg=")
}

// A-8: a successful disable op must flip mode to disabled and redirect with a msg.
func TestOpDisableSuccess(t *testing.T) {
	db, h := newTestServer(t)
	rec := post(t, h, url.Values{"action": {"disable"}, "tag": {"dev"}}, nil)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())

	item := db.Item("tag#dev")
	require.NotNil(t, item)
	assert.Equal(t, model.ModeDisabled, item["mode"].(*types.AttributeValueMemberS).Value)
	assert.Contains(t, rec.Header().Get("Location"), "msg=disabled")
}

func TestOpAddMember(t *testing.T) {
	db, h := newTestServer(t)
	rec := post(t, h, url.Values{"action": {"add"}, "tag": {"dev"}, "type": {"ecs"}, "cluster": {"dev-cluster"}, "service": {"api"}}, nil)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	assert.NotNil(t, db.Item("member#ecs#dev-cluster/api"))
}

func TestOpRemoveMember(t *testing.T) {
	db, h := newTestServer(t)
	rec := post(t, h, url.Values{"action": {"remove-member"}, "tag": {"dev"}, "id": {"rds-instance#db1"}}, nil)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	assert.Nil(t, db.Item("member#rds-instance#db1"))
	assert.NotNil(t, db.Item("tag#dev"), "removing a member must not remove the tag")
}

func TestOpRemoveTag(t *testing.T) {
	db, h := newTestServer(t)
	rec := post(t, h, url.Values{"action": {"remove-tag"}, "tag": {"dev"}}, nil)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	assert.Nil(t, db.Item("tag#dev"))
	assert.Nil(t, db.Item("member#rds-instance#db1"))
	assert.Equal(t, "/?msg="+url.QueryEscape("removed tag dev"), rec.Header().Get("Location"), "remove-tag should redirect to index")
}

// A-8: with a non-empty Base (the API Gateway stage prefix), every op redirect must carry that prefix so the browser stays under it.
func TestOpRedirectCarriesBasePath(t *testing.T) {
	ctrl := gomock.NewController(t)
	api, db := mocks.NewDynaStore(ctrl)
	seedDevTag(db)
	h := New(store.New(api, "t"), "/console", time.UTC).Handler()

	rec := post(t, h, url.Values{"action": {"pin"}, "tag": {"dev"}, "desired": {"running"}}, nil)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	assert.True(t, strings.HasPrefix(rec.Header().Get("Location"), "/console/tag"), "redirect must carry the base path")

	rec = post(t, h, url.Values{"action": {"remove-tag"}, "tag": {"dev"}}, nil)
	assert.Equal(t, "/console/?msg="+url.QueryEscape("removed tag dev"), rec.Header().Get("Location"))
}

// A-8: the tag page must render override and member status data, not just the tag config.
func TestTagPageRendersOverrideAndStatus(t *testing.T) {
	db, h := newTestServer(t)
	db.Seed(map[string]types.AttributeValue{
		"pk": &types.AttributeValueMemberS{Value: "override#dev"}, "desired": &types.AttributeValueMemberS{Value: model.DesiredRunning},
		"expires_at": &types.AttributeValueMemberN{Value: "99999999999"},
	})
	db.Seed(map[string]types.AttributeValue{
		"pk": &types.AttributeValueMemberS{Value: "status#rds-instance#db1"}, "last_action": &types.AttributeValueMemberS{Value: "stop"},
		"last_action_at": &types.AttributeValueMemberS{Value: "2026-07-19T00:00:00Z"}, "observed_state": &types.AttributeValueMemberS{Value: "stopped"},
	})

	rec := get(t, h, "/tag?name=dev")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := rec.Body.String()
	assert.Contains(t, body, "running", "tag page missing override desired state")
	assert.Contains(t, body, "stop at 2026-07-19T00:00:00Z", "tag page missing last action")
	assert.Contains(t, body, "stopped", "tag page missing observed state")
}

// B-8: every response must refuse to be framed, so an allowlisted operator visiting an attacker page can't have remove/pin actions driven via a transparent iframe (clickjacking).
func TestClickjackingHeadersPresent(t *testing.T) {
	_, h := newTestServer(t)
	rec := get(t, h, "/")
	assert.Equal(t, "DENY", rec.Header().Get("X-Frame-Options"))
	assert.Contains(t, rec.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'")
}

func TestCrossOriginPostRejected(t *testing.T) {
	_, h := newTestServer(t)
	form := url.Values{"action": {"pin"}, "tag": {"dev"}, "desired": {"running"}}
	rec := post(t, h, form, map[string]string{"Origin": "https://evil.example"})
	assert.Equal(t, http.StatusForbidden, rec.Code, "cross-origin Origin")

	rec = post(t, h, form, map[string]string{"Sec-Fetch-Site": "cross-site"})
	assert.Equal(t, http.StatusForbidden, rec.Code, "Sec-Fetch-Site cross-site")

	rec = post(t, h, form, map[string]string{"Origin": "http://example.com", "Sec-Fetch-Site": "same-origin"})
	assert.Equal(t, http.StatusSeeOther, rec.Code, "same-origin")
}
