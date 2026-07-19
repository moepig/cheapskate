package webconsole

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"cheapskate/internal/dynafake"
	"cheapskate/internal/model"
	"cheapskate/internal/store"
)

func newTestServer(t *testing.T) (*dynafake.Fake, http.Handler) {
	t.Helper()
	fake := dynafake.New()
	fake.Seed(map[string]types.AttributeValue{
		"pk":      &types.AttributeValueMemberS{Value: "config#rds-instance#db1"},
		"type":    &types.AttributeValueMemberS{Value: model.TypeRdsInstance},
		"mode":    &types.AttributeValueMemberS{Value: model.ModePinned},
		"desired": &types.AttributeValueMemberS{Value: model.DesiredStopped},
	})
	return fake, New(store.New(fake, "t"), "", time.UTC).Handler()
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

func TestIndexListsResources(t *testing.T) {
	_, h := newTestServer(t)
	rec := get(t, h, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "rds-instance#db1") {
		t.Errorf("index does not list the seeded resource:\n%s", rec.Body.String())
	}
}

func TestDetailPage(t *testing.T) {
	_, h := newTestServer(t)
	rec := get(t, h, "/resource?id="+url.QueryEscape("rds-instance#db1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /resource = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if rec := get(t, h, "/resource?id="+url.QueryEscape("rds-instance#ghost")); rec.Code != http.StatusNotFound {
		t.Errorf("unregistered resource = %d, want 404", rec.Code)
	}
	if rec := get(t, h, "/resource?id=bogus"); rec.Code != http.StatusBadRequest {
		t.Errorf("malformed id = %d, want 400", rec.Code)
	}
}

func TestOpPin(t *testing.T) {
	fake, h := newTestServer(t)
	rec := post(t, h, url.Values{"action": {"pin"}, "id": {"rds-instance#db1"}, "desired": {"running"}}, nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("pin = %d, want 303: %s", rec.Code, rec.Body.String())
	}
	item := fake.Item("config#rds-instance#db1")
	if got := item["desired"].(*types.AttributeValueMemberS).Value; got != "running" {
		t.Errorf("desired = %q, want running", got)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "msg=") {
		t.Errorf("redirect %q carries no msg", loc)
	}
}

func TestOpOverrideAndClear(t *testing.T) {
	fake, h := newTestServer(t)
	rec := post(t, h, url.Values{"action": {"override"}, "id": {"rds-instance#db1"}, "desired": {"running"}, "for": {"2h"}}, nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("override = %d: %s", rec.Code, rec.Body.String())
	}
	if fake.Item("override#rds-instance#db1") == nil {
		t.Fatal("override item not written")
	}
	rec = post(t, h, url.Values{"action": {"clear-override"}, "id": {"rds-instance#db1"}}, nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("clear-override = %d", rec.Code)
	}
	if fake.Item("override#rds-instance#db1") != nil {
		t.Error("override item not deleted")
	}
}

func TestOpValidationErrorRedirects(t *testing.T) {
	_, h := newTestServer(t)
	rec := post(t, h, url.Values{"action": {"schedule"}, "id": {"rds-instance#db1"}, "start": {"not a cron"}}, nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("invalid schedule = %d, want 303 redirect with err", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "err=") {
		t.Errorf("redirect %q carries no err", loc)
	}
}

// A-8: a successful schedule op must persist the crons and mode, and redirect with a msg.
func TestOpScheduleSuccess(t *testing.T) {
	fake, h := newTestServer(t)
	rec := post(t, h, url.Values{
		"action": {"schedule"}, "id": {"rds-instance#db1"},
		"start": {"0 9 * * *"}, "stop": {"0 20 * * *"}, "timezone": {"UTC"},
	}, nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("schedule = %d, want 303: %s", rec.Code, rec.Body.String())
	}
	item := fake.Item("config#rds-instance#db1")
	if item == nil {
		t.Fatal("config item not written")
	}
	if got := item["mode"].(*types.AttributeValueMemberS).Value; got != model.ModeSchedule {
		t.Errorf("mode = %q, want %q", got, model.ModeSchedule)
	}
	if got := item["start_cron"].(*types.AttributeValueMemberS).Value; got != "0 9 * * *" {
		t.Errorf("start_cron = %q", got)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "msg=") {
		t.Errorf("redirect %q carries no msg", loc)
	}
}

// A-8: a successful disable op must flip mode to disabled and redirect with a msg.
func TestOpDisableSuccess(t *testing.T) {
	fake, h := newTestServer(t)
	rec := post(t, h, url.Values{"action": {"disable"}, "id": {"rds-instance#db1"}}, nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("disable = %d, want 303: %s", rec.Code, rec.Body.String())
	}
	item := fake.Item("config#rds-instance#db1")
	if got := item["mode"].(*types.AttributeValueMemberS).Value; got != model.ModeDisabled {
		t.Errorf("mode = %q, want %q", got, model.ModeDisabled)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "msg=disabled") {
		t.Errorf("redirect %q carries no disabled msg", loc)
	}
}

// A-8: with a non-empty Base (the API Gateway stage prefix), every op redirect must carry that prefix so the browser stays under it.
func TestOpRedirectCarriesBasePath(t *testing.T) {
	fake := dynafake.New()
	fake.Seed(map[string]types.AttributeValue{
		"pk": &types.AttributeValueMemberS{Value: "config#rds-instance#db1"}, "type": &types.AttributeValueMemberS{Value: model.TypeRdsInstance},
		"mode": &types.AttributeValueMemberS{Value: model.ModePinned}, "desired": &types.AttributeValueMemberS{Value: model.DesiredStopped},
	})
	h := New(store.New(fake, "t"), "/console", time.UTC).Handler()

	rec := post(t, h, url.Values{"action": {"pin"}, "id": {"rds-instance#db1"}, "desired": {"running"}}, nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("pin = %d: %s", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "/console/resource") {
		t.Errorf("redirect %q must carry the base path", loc)
	}

	rec = post(t, h, url.Values{"action": {"remove"}, "id": {"rds-instance#db1"}}, nil)
	if loc := rec.Header().Get("Location"); loc != "/console/?msg="+url.QueryEscape("removed rds-instance#db1") {
		t.Errorf("remove redirect = %q", loc)
	}
}

// A-8: the detail page must render override and status data, not just the config.
func TestDetailPageRendersOverrideAndStatus(t *testing.T) {
	fake, h := newTestServer(t)
	fake.Seed(map[string]types.AttributeValue{
		"pk": &types.AttributeValueMemberS{Value: "override#rds-instance#db1"}, "desired": &types.AttributeValueMemberS{Value: model.DesiredRunning},
		"expires_at": &types.AttributeValueMemberN{Value: "99999999999"},
	})
	fake.Seed(map[string]types.AttributeValue{
		"pk": &types.AttributeValueMemberS{Value: "status#rds-instance#db1"}, "last_action": &types.AttributeValueMemberS{Value: "stop"},
		"last_action_at": &types.AttributeValueMemberS{Value: "2026-07-19T00:00:00Z"}, "observed_state": &types.AttributeValueMemberS{Value: "stopped"},
	})

	rec := get(t, h, "/resource?id="+url.QueryEscape("rds-instance#db1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /resource = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "running") {
		t.Errorf("detail page missing override desired state:\n%s", body)
	}
	if !strings.Contains(body, "stop at 2026-07-19T00:00:00Z") {
		t.Errorf("detail page missing last action:\n%s", body)
	}
	if !strings.Contains(body, "stopped") {
		t.Errorf("detail page missing observed state:\n%s", body)
	}
}

func TestOpRemove(t *testing.T) {
	fake, h := newTestServer(t)
	rec := post(t, h, url.Values{"action": {"remove"}, "id": {"rds-instance#db1"}}, nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("remove = %d", rec.Code)
	}
	if fake.Item("config#rds-instance#db1") != nil {
		t.Error("config item not deleted")
	}
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/?") {
		t.Errorf("remove should redirect to index, got %q", loc)
	}
}

// B-8: every response must refuse to be framed, so an allowlisted operator visiting an attacker page can't have remove/pin actions driven via a transparent iframe (clickjacking).
func TestClickjackingHeadersPresent(t *testing.T) {
	_, h := newTestServer(t)
	rec := get(t, h, "/")
	if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options = %q, want DENY", got)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("CSP missing frame-ancestors 'none': %q", csp)
	}
}

func TestCrossOriginPostRejected(t *testing.T) {
	_, h := newTestServer(t)
	form := url.Values{"action": {"pin"}, "id": {"rds-instance#db1"}, "desired": {"running"}}
	if rec := post(t, h, form, map[string]string{"Origin": "https://evil.example"}); rec.Code != http.StatusForbidden {
		t.Errorf("cross-origin Origin = %d, want 403", rec.Code)
	}
	if rec := post(t, h, form, map[string]string{"Sec-Fetch-Site": "cross-site"}); rec.Code != http.StatusForbidden {
		t.Errorf("Sec-Fetch-Site cross-site = %d, want 403", rec.Code)
	}
	if rec := post(t, h, form, map[string]string{"Origin": "http://example.com", "Sec-Fetch-Site": "same-origin"}); rec.Code != http.StatusSeeOther {
		t.Errorf("same-origin = %d, want 303", rec.Code)
	}
}
