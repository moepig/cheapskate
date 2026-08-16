package webconsole

import (
	"bytes"
	"log/slog"
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
	mocks "cheapskate/internal/state/mocks"
)

func newTestServer(t *testing.T) (*mocks.DynaStore, *porttest.Discoverer, http.Handler) {
	t.Helper()
	db, disc, h, _ := newTestServerWithLog(t)
	return db, disc, h
}

// newTestServer が返すものに加え、サーバが出力したログを返す
func newTestServerWithLog(t *testing.T) (*mocks.DynaStore, *porttest.Discoverer, http.Handler, *bytes.Buffer) {
	t.Helper()
	ctrl := gomock.NewController(t)
	api, db := mocks.NewDynaStore(ctrl)
	disc := porttest.NewDiscoverer()
	seedDevGroup(db, disc)
	var logBuf bytes.Buffer
	// 本番と同じ JSON ハンドラを用いる。属性名を含め、実際に出力する行を検証するためである
	h := New(state.New(api, "t"), disc, nil, "", time.UTC, slog.New(slog.NewJSONHandler(&logBuf, nil))).Handler()
	return db, disc, h, &logBuf
}

// ログを検証しないテストにおいて、出力を抑止するためのロガー
func testLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.DiscardHandler)
}

// グループ "dev" と、そのセレクタに一致するリソース 1 つを投入する
// discoverer のテストダブルはセレクタのタグ値をキーとし、本ファイルのグループはタグ値をグループ名と一致させる
// したがって、実際の Tagging API は関与しない
func seedDevGroup(db *mocks.DynaStore, disc *porttest.Discoverer) {
	db.Seed(map[string]types.AttributeValue{
		"pk": &types.AttributeValueMemberS{Value: "group#dev"}, "mode": &types.AttributeValueMemberS{Value: string(model.ModePinned)},
		"desired": &types.AttributeValueMemberS{Value: string(model.DesiredStopped)},
		"tag_key": &types.AttributeValueMemberS{Value: "env"}, "tag_value": &types.AttributeValueMemberS{Value: "dev"},
		"types": &types.AttributeValueMemberSS{Value: []string{string(model.TypeRdsInstance)}},
	})
	disc.ByTagValue["dev"] = []model.Resource{{Type: model.TypeRdsInstance, Ref: "db1"}}
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

func TestDescribeGroup(t *testing.T) {
	cases := []struct {
		name string
		item model.GroupSpec
		want string
	}{
		{"pinned", model.GroupSpec{Mode: model.ModePinned, Desired: "running"}, "running"},
		{"disabled", model.GroupSpec{Mode: model.ModeDisabled}, "-"},
		{
			"schedule with all fields",
			model.GroupSpec{Mode: model.ModeSchedule, StartCron: "0 9 * * 1-5", StopCron: "0 21 * * 1-5", Timezone: "Asia/Tokyo"},
			"start: 0 9 * * 1-5<br>stop: 0 21 * * 1-5<br>timezone: Asia/Tokyo",
		},
		{
			"schedule with only start",
			model.GroupSpec{Mode: model.ModeSchedule, StartCron: "0 9 * * 1-5"},
			"start: 0 9 * * 1-5",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, string(describeGroup(tc.item)))
		})
	}
}

func TestDescribeSelector(t *testing.T) {
	cases := []struct {
		name string
		item model.GroupSpec
		want string
	}{
		{"empty", model.GroupSpec{}, "-"},
		{
			"tag and types",
			model.GroupSpec{TagKey: "cheapskate:group", TagValue: "dev", Types: []model.ResourceType{model.TypeEc2Instance, model.TypeEcsService, model.TypeRdsInstance}},
			"tag: cheapskate:group=dev<br>types: ec2-instance, ecs-service, rds-instance",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, string(describeSelector(tc.item)))
		})
	}
}

func TestDescribeResourceConfig(t *testing.T) {
	cases := []struct {
		name string
		r    model.Resource
		want string
	}{
		{"non-ecs-service type", model.Resource{Type: model.TypeRdsInstance, Tags: map[string]string{model.EcsDesiredCountTagKey: "2"}}, `<span class="muted">-</span>`},
		{"ecs-service with no scaling tags", model.Resource{Type: model.TypeEcsService}, `<span class="muted">-</span>`},
		{
			"ecs-service with all scaling tags",
			model.Resource{Type: model.TypeEcsService, Tags: map[string]string{
				model.EcsDesiredCountTagKey: "2", model.EcsScalingMinTagKey: "1", model.EcsScalingMaxTagKey: "3",
			}},
			"desired: 2<br>scaling min: 1<br>scaling max: 3",
		},
		{
			"ecs-service with only desired count",
			model.Resource{Type: model.TypeEcsService, Tags: map[string]string{model.EcsDesiredCountTagKey: "5"}},
			"desired: 5",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, string(describeResourceConfig(tc.r)))
		})
	}
}

func TestIndexListsGroupsAndSelector(t *testing.T) {
	_, _, h := newTestServer(t)
	rec := get(t, h, "/")
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "dev", "index does not list the seeded group")
	assert.Contains(t, body, "env=dev", "index does not show the group's selector")
}

func TestGroupPage(t *testing.T) {
	_, _, h := newTestServer(t)
	rec := get(t, h, "/group?name=dev")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rec = get(t, h, "/group?name=ghost")
	assert.Equal(t, http.StatusNotFound, rec.Code, "unregistered group")

	rec = get(t, h, "/group?name="+url.QueryEscape("bad name"))
	assert.Equal(t, http.StatusBadRequest, rec.Code, "malformed group name")
}

// グループページは、グループ設定に加え、override と探索したリソースのステータスも描画しなければならない
func TestGroupPageRendersOverrideAndDiscoveredStatus(t *testing.T) {
	db, _, h := newTestServer(t)
	db.Seed(map[string]types.AttributeValue{
		"pk": &types.AttributeValueMemberS{Value: "override#dev"}, "desired": &types.AttributeValueMemberS{Value: string(model.DesiredRunning)},
		"expires_at": &types.AttributeValueMemberN{Value: "99999999999"},
	})
	db.Seed(map[string]types.AttributeValue{
		"pk": &types.AttributeValueMemberS{Value: "status#rds-instance#db1"}, "last_action": &types.AttributeValueMemberS{Value: "stop"},
		"last_action_at": &types.AttributeValueMemberS{Value: "2026-07-19T00:00:00Z"}, "observed_state": &types.AttributeValueMemberS{Value: "stopped"},
	})

	rec := get(t, h, "/group?name=dev")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := rec.Body.String()
	assert.Contains(t, body, "running", "group page missing override desired state")
	assert.Contains(t, body, "stop at 2026-07-19T00:00:00Z", "group page missing last action")
	assert.Contains(t, body, "stopped", "group page missing observed state")
	assert.Contains(t, body, "db1", "group page missing the discovered resource")
}

// グループページの "Current state" 列は、問い合わせたリソースの現在状態を表示しなければならない
// ここでは ecs-service の desiredCount を Observation.Detail へ格納した値であり、同じ行に表示する最後の reconcile ステータスとは異なる
func TestGroupPageRendersLiveState(t *testing.T) {
	ctrl := gomock.NewController(t)
	api, db := mocks.NewDynaStore(ctrl)
	disc := porttest.NewDiscoverer()
	seedDevGroup(db, disc)
	describers := map[model.ResourceType]port.Describer{
		model.TypeRdsInstance: porttest.Describer{Obs: model.Observation{State: model.StateRunning, Detail: "available"}},
	}
	h := New(state.New(api, "t"), disc, describers, "", time.UTC, testLogger(t)).Handler()

	rec := get(t, h, "/group?name=dev")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "running (available)", "group page missing live-queried current state")
}

// 1 リソースの Describe が失敗した場合も、その行にのみエラーを表示し、他の行は通常どおり描画する
// 現在状態の読み取りの失敗と、グループの構成の読み取りの失敗は独立している
// 状態を不明として表示するだけでは、停止済みであるか権限が不足しているかを区別できないため、失敗の理由を表示する
func TestGroupPageRendersLiveErrorInline(t *testing.T) {
	ctrl := gomock.NewController(t)
	api, db := mocks.NewDynaStore(ctrl)
	disc := porttest.NewDiscoverer()
	seedDevGroup(db, disc)
	describers := map[model.ResourceType]port.Describer{
		model.TypeRdsInstance: porttest.Describer{Err: assert.AnError},
	}
	h := New(state.New(api, "t"), disc, describers, "", time.UTC, testLogger(t)).Handler()

	rec := get(t, h, "/group?name=dev")

	require.Equal(t, http.StatusOK, rec.Code, "1 リソースの Describe 失敗でページ全体を落としてはならない")
	body := rec.Body.String()
	assert.Contains(t, body, assert.AnError.Error(), "なぜ読めなかったのかを出す")
	assert.Contains(t, body, "db1", "行そのものは残る")
}

// Discover の失敗は、リクエストを失敗させず、グループページ内へ描画しなければならない
// IAM 権限の不足と誤設定を 500 としてはならない
func TestGroupPageShowsDiscoverErrorInlineWith200(t *testing.T) {
	_, disc, h := newTestServer(t)
	disc.Err = assert.AnError

	rec := get(t, h, "/group?name=dev")
	require.Equal(t, http.StatusOK, rec.Code, "a discover error must still render 200")
	assert.Contains(t, rec.Body.String(), "discover error")
}

func TestOpPin(t *testing.T) {
	db, _, h := newTestServer(t)
	rec := post(t, h, url.Values{"action": {"pin"}, "group": {"dev"}, "desired": {"running"}}, nil)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())

	item := db.Item("group#dev")
	require.NotNil(t, item)
	assert.Equal(t, "running", item["desired"].(*types.AttributeValueMemberS).Value)
	assert.Contains(t, rec.Header().Get("Location"), "msg=")
}

func TestOpOverrideAndClear(t *testing.T) {
	db, _, h := newTestServer(t)
	until := time.Now().Add(2 * time.Hour).UTC().Format("2006-01-02T15:04")
	rec := post(t, h, url.Values{"action": {"override"}, "group": {"dev"}, "desired": {"running"}, "until": {until}}, nil)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	assert.NotNil(t, db.Item("override#dev"), "override item not written")

	rec = post(t, h, url.Values{"action": {"clear-override"}, "group": {"dev"}}, nil)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Nil(t, db.Item("override#dev"), "override item not deleted")
}

func TestOpOverrideRejectsPastDateTime(t *testing.T) {
	_, _, h := newTestServer(t)
	until := time.Now().Add(-2 * time.Hour).UTC().Format("2006-01-02T15:04")
	rec := post(t, h, url.Values{"action": {"override"}, "group": {"dev"}, "desired": {"running"}, "until": {until}}, nil)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Header().Get("Location"), "err=")
}

// グループが cron のフィールドを持つ場合、unpin は mode=schedule へ戻さなければならない
// Pin が cron のフィールドを保持するという規約により、コンソールからも schedule を再開できることを検証する
func TestOpUnpinRestoresScheduleWhenCronsPresent(t *testing.T) {
	db, _, h := newTestServer(t)
	rec := post(t, h, url.Values{
		"action": {"schedule"}, "group": {"dev"},
		"start": {"0 9 * * *"}, "stop": {"0 20 * * *"}, "timezone": {"UTC"},
	}, nil)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	rec = post(t, h, url.Values{"action": {"pin"}, "group": {"dev"}, "desired": {"running"}}, nil)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())

	rec = post(t, h, url.Values{"action": {"unpin"}, "group": {"dev"}}, nil)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())

	item := db.Item("group#dev")
	require.NotNil(t, item)
	assert.Equal(t, string(model.ModeSchedule), item["mode"].(*types.AttributeValueMemberS).Value)
	assert.Equal(t, "0 9 * * *", item["start_cron"].(*types.AttributeValueMemberS).Value, "unpin must not lose cron fields")
}

// cron が未設定の場合、unpin が復帰する先は存在せず、mode=disabled となる
func TestOpUnpinFallsBackToDisabledWithoutCrons(t *testing.T) {
	db, _, h := newTestServer(t)
	rec := post(t, h, url.Values{"action": {"unpin"}, "group": {"dev"}}, nil)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())

	item := db.Item("group#dev")
	require.NotNil(t, item)
	assert.Equal(t, string(model.ModeDisabled), item["mode"].(*types.AttributeValueMemberS).Value)
}

// 書式が不正な until はリダイレクトで差し戻し、override を書き込まない
// TestOpOverrideRejectsPastDateTime の対象は解析できた過去日時であり、本テストの対象は解析自体が失敗する経路である
func TestOpOverrideRejectsUnparsableDateTime(t *testing.T) {
	db, _, h := newTestServer(t)

	rec := post(t, h, url.Values{
		"action": {"override"}, "group": {"dev"}, "desired": {"running"}, "until": {"2026-07-15 15:04"}, // T 区切りでない
	}, nil)

	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	loc, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)
	assert.Contains(t, loc.Query().Get("err"), "invalid date/time", "利用者が直せるよう入力値を添えて差し戻す")
	assert.Nil(t, db.Item("override#dev"), "解析に失敗した時点で override を書いてはならない")
}

// フォームの action と group は、操作の実行前に検証する
// この 2 つはフォームが正しい限り妥当であるため、不正な値はフォーム側の不具合または手動で構成したリクエストを示す
// 修正を促す err リダイレクトではなく 400 を返す
func TestOpRejectsMalformedRequestWithBadRequest(t *testing.T) {
	cases := []struct {
		name string
		form url.Values
	}{
		{"unknown action", url.Values{"action": {"self-destruct"}, "group": {"dev"}}},
		{"missing action", url.Values{"group": {"dev"}}},
		{"invalid group name", url.Values{"action": {"pin"}, "group": {"-bad"}, "desired": {"running"}}},
		{"missing group", url.Values{"action": {"pin"}, "desired": {"running"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, _, h := newTestServer(t)

			rec := post(t, h, tc.form, nil)

			assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.Equal(t, string(model.ModePinned), db.Item("group#dev")["mode"].(*types.AttributeValueMemberS).Value,
				"弾いたリクエストが既存のグループを書き換えてはならない")
		})
	}
}

// フォーム本体を復号できない場合も 400 を返す
// action と group のいずれも読めないため、差し戻し先のグループを決定できないためである
func TestOpRejectsUndecodableForm(t *testing.T) {
	db, _, h := newTestServer(t)
	req := httptest.NewRequest("POST", "/op", strings.NewReader("action=pin&group=%zz"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, string(model.ModePinned), db.Item("group#dev")["mode"].(*types.AttributeValueMemberS).Value)
}

func TestOpValidationErrorRedirects(t *testing.T) {
	_, _, h := newTestServer(t)
	rec := post(t, h, url.Values{"action": {"schedule"}, "group": {"dev"}, "start": {"not a cron"}}, nil)
	require.Equal(t, http.StatusSeeOther, rec.Code, "invalid schedule must still redirect with err")
	assert.Contains(t, rec.Header().Get("Location"), "err=")
}

// schedule 操作が成功したら cron と mode を永続化し、msg 付きでリダイレクトしなければならない
func TestOpScheduleSuccess(t *testing.T) {
	db, _, h := newTestServer(t)
	rec := post(t, h, url.Values{
		"action": {"schedule"}, "group": {"dev"},
		"start": {"0 9 * * *"}, "stop": {"0 20 * * *"}, "timezone": {"UTC"},
	}, nil)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())

	item := db.Item("group#dev")
	require.NotNil(t, item, "group item not written")
	assert.Equal(t, string(model.ModeSchedule), item["mode"].(*types.AttributeValueMemberS).Value)
	assert.Equal(t, "0 9 * * *", item["start_cron"].(*types.AttributeValueMemberS).Value)
	assert.Contains(t, rec.Header().Get("Location"), "msg=")
}

// disable 操作が成功したら mode を disabled に切り替え、msg 付きでリダイレクトしなければならない
func TestOpDisableSuccess(t *testing.T) {
	db, _, h := newTestServer(t)
	rec := post(t, h, url.Values{"action": {"disable"}, "group": {"dev"}}, nil)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())

	item := db.Item("group#dev")
	require.NotNil(t, item)
	assert.Equal(t, string(model.ModeDisabled), item["mode"].(*types.AttributeValueMemberS).Value)
	assert.Contains(t, rec.Header().Get("Location"), "msg=disabled")
}

func TestOpSetSelectorCreatesGroup(t *testing.T) {
	db, _, h := newTestServer(t)
	rec := post(t, h, url.Values{
		"action": {"set-selector"}, "group": {"staging"},
		"tag_key": {"env"}, "tag_value": {"staging"},
		"types": {string(model.TypeRdsInstance), string(model.TypeEcsService)}, // 複数値のチェックボックス
	}, nil)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())

	item := db.Item("group#staging")
	require.NotNil(t, item, "group item not written")
	assert.Equal(t, string(model.ModeDisabled), item["mode"].(*types.AttributeValueMemberS).Value)
	assert.Equal(t, "env", item["tag_key"].(*types.AttributeValueMemberS).Value)
	ss := item["types"].(*types.AttributeValueMemberSS).Value
	assert.ElementsMatch(t, model.TypeNames([]model.ResourceType{model.TypeRdsInstance, model.TypeEcsService}), ss, "both checked types must be read via r.PostForm, not PostFormValue")
	assert.Contains(t, rec.Header().Get("Location"), "created+group")
}

func TestOpSetSelectorOnExistingGroupPreservesMode(t *testing.T) {
	db, _, h := newTestServer(t)
	rec := post(t, h, url.Values{
		"action": {"set-selector"}, "group": {"dev"},
		"tag_key": {"env"}, "tag_value": {"dev"}, "types": {string(model.TypeRdsInstance), string(model.TypeEc2Instance)},
	}, nil)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())

	item := db.Item("group#dev")
	require.NotNil(t, item)
	assert.Equal(t, string(model.ModePinned), item["mode"].(*types.AttributeValueMemberS).Value, "existing mode must be preserved")
}

func TestOpRemoveGroup(t *testing.T) {
	db, _, h := newTestServer(t)
	rec := post(t, h, url.Values{"action": {"remove-group"}, "group": {"dev"}}, nil)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	assert.Nil(t, db.Item("group#dev"))
	assert.Equal(t, "/?msg="+url.QueryEscape("removed group dev"), rec.Header().Get("Location"), "remove-group should redirect to index")
}

// Base (API Gateway のステージの接頭辞) が空でない場合、操作後のリダイレクトはすべてその接頭辞を含まなければならない
// これによりブラウザは、その接頭辞の配下に留まる
func TestOpRedirectCarriesBasePath(t *testing.T) {
	ctrl := gomock.NewController(t)
	api, db := mocks.NewDynaStore(ctrl)
	disc := porttest.NewDiscoverer()
	seedDevGroup(db, disc)
	h := New(state.New(api, "t"), disc, nil, "/console", time.UTC, testLogger(t)).Handler()

	rec := post(t, h, url.Values{"action": {"pin"}, "group": {"dev"}, "desired": {"running"}}, nil)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	assert.True(t, strings.HasPrefix(rec.Header().Get("Location"), "/console/group"), "redirect must carry the base path")

	rec = post(t, h, url.Values{"action": {"remove-group"}, "group": {"dev"}}, nil)
	assert.Equal(t, "/console/?msg="+url.QueryEscape("removed group dev"), rec.Header().Get("Location"))
}

// すべてのレスポンスは、フレーム内への埋め込みを拒否しなければならない
// 許可リストに含まれる環境から外部のページを開いた場合も、iframe を経由した remove と pin の実行を防ぐためである
func TestClickjackingHeadersPresent(t *testing.T) {
	_, _, h := newTestServer(t)
	rec := get(t, h, "/")
	assert.Equal(t, "DENY", rec.Header().Get("X-Frame-Options"))
	assert.Contains(t, rec.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'")
}

func TestCrossOriginPostRejected(t *testing.T) {
	_, _, h := newTestServer(t)
	form := url.Values{"action": {"pin"}, "group": {"dev"}, "desired": {"running"}}
	rec := post(t, h, form, map[string]string{"Origin": "https://evil.example"})
	assert.Equal(t, http.StatusForbidden, rec.Code, "cross-origin Origin")

	rec = post(t, h, form, map[string]string{"Sec-Fetch-Site": "cross-site"})
	assert.Equal(t, http.StatusForbidden, rec.Code, "Sec-Fetch-Site cross-site")

	rec = post(t, h, form, map[string]string{"Origin": "http://example.com", "Sec-Fetch-Site": "same-origin"})
	assert.Equal(t, http.StatusSeeOther, rec.Code, "same-origin")
}

// 解析できない Origin は同一オリジンであることを確認できないため、拒否しなければならない
// url.Parse の失敗を Origin なしと同じに扱った場合、不正な Origin の送信により検査を回避できる
func TestUnparsableOriginRejected(t *testing.T) {
	_, _, h := newTestServer(t)
	form := url.Values{"action": {"pin"}, "group": {"dev"}, "desired": {"running"}}

	rec := post(t, h, form, map[string]string{"Origin": "http://[::1"}) // 閉じ括弧がなく url.Parse が失敗する

	assert.Equal(t, http.StatusForbidden, rec.Code, "an Origin we cannot parse must not be treated as same-origin")
}

func TestDoctorPageListsFindings(t *testing.T) {
	db, _, h := newTestServer(t)
	db.Seed(map[string]types.AttributeValue{
		"pk":         &types.AttributeValueMemberS{Value: "override#ghost"},
		"desired":    &types.AttributeValueMemberS{Value: string(model.DesiredRunning)},
		"expires_at": &types.AttributeValueMemberN{Value: "99999999999"},
	})

	rec := get(t, h, "/doctor")

	assert.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "orphan-override")
	assert.Contains(t, body, "override#ghost", "the raw key lets an operator delete it by hand")
	assert.Contains(t, body, "Prune 1 record(s)")
}

func TestDoctorPageWithNothingToReportHidesPruneForm(t *testing.T) {
	_, _, h := newTestServer(t)

	rec := get(t, h, "/doctor")

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "the state table is consistent")
	assert.NotContains(t, rec.Body.String(), "Prune ")
}

// prune は表示済みの画面に対してではなく、実行の時点で診断を再実行した結果に対して削除する
func TestDoctorPrune(t *testing.T) {
	db, _, h := newTestServer(t)
	db.Seed(map[string]types.AttributeValue{
		"pk":         &types.AttributeValueMemberS{Value: "override#ghost"},
		"desired":    &types.AttributeValueMemberS{Value: string(model.DesiredRunning)},
		"expires_at": &types.AttributeValueMemberN{Value: "99999999999"},
	})

	req := httptest.NewRequest("POST", "/doctor", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Contains(t, rec.Header().Get("Location"), "pruned+1+record")
	assert.Nil(t, db.Item("override#ghost"))
}

// 個々の削除の失敗は doctor.Run のエラーとならず finding に残るため、画面側で集計して報告する
// 削除件数のみを表示した場合、削除できなかったレコードの残存を検知できない
// 削除は冪等であるため、再実行により解消できることも併せて表示する
func TestDoctorPruneReportsPartialFailure(t *testing.T) {
	db, _, h := newTestServer(t)
	for _, name := range []string{"ghost-a", "ghost-b"} {
		db.Seed(map[string]types.AttributeValue{
			"pk":         &types.AttributeValueMemberS{Value: "override#" + name},
			"desired":    &types.AttributeValueMemberS{Value: string(model.DesiredRunning)},
			"expires_at": &types.AttributeValueMemberN{Value: "99999999999"},
		})
	}
	db.FailOn("delete", "override#ghost-a", assert.AnError)

	req := httptest.NewRequest("POST", "/doctor", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusSeeOther, rec.Code)
	loc, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)
	msg := loc.Query().Get("err")
	assert.Contains(t, msg, "pruned 1 record(s)", "消せたぶんは正しく数える")
	assert.Contains(t, msg, "1 failed to delete")
	assert.Contains(t, msg, "run it again", "削除は冪等なので再実行が正しい対処である")
	assert.Empty(t, loc.Query().Get("msg"), "一部でも失敗したなら成功として見せてはならない")
	assert.NotNil(t, db.Item("override#ghost-a"), "失敗したものは残る")
	assert.Nil(t, db.Item("override#ghost-b"), "成功したものは消える")
}

func TestDoctorPruneRejectsCrossOrigin(t *testing.T) {
	db, _, h := newTestServer(t)
	db.Seed(map[string]types.AttributeValue{
		"pk":         &types.AttributeValueMemberS{Value: "override#ghost"},
		"desired":    &types.AttributeValueMemberS{Value: string(model.DesiredRunning)},
		"expires_at": &types.AttributeValueMemberN{Value: "99999999999"},
	})

	req := httptest.NewRequest("POST", "/doctor", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.NotNil(t, db.Item("override#ghost"), "a rejected request must not delete anything")
}

// 1 リクエストにつき、開始と完了を 1 行ずつ記録する
// 完了行のみを出力した場合、Lambda のタイムアウトなどで中断したリクエストの記録が残らない
func TestRequestLoggingRecordsStartAndEnd(t *testing.T) {
	_, _, h, logBuf := newTestServerWithLog(t)

	require.Equal(t, http.StatusOK, get(t, h, "/group?name=dev").Code)

	out := logBuf.String()
	assert.Contains(t, out, `"msg":"request-start"`)
	assert.Contains(t, out, `"msg":"request-end"`)
	assert.Contains(t, out, `"method":"GET"`)
	assert.Contains(t, out, `"path":"/group"`)
	assert.Contains(t, out, `"query":"name=dev"`)
	assert.Contains(t, out, `"status":200`)
	assert.Contains(t, out, `"duration_ms":`)
}

// 画面へ返すエラーは、ログへも記録する
// リダイレクトと 4xx/5xx のいずれについても、記録先をブラウザの表示のみとしてはならない
func TestErrorsGoToTheLogAndNotOnlyTheScreen(t *testing.T) {
	cases := []struct {
		name     string
		do       func(h http.Handler) *httptest.ResponseRecorder
		wantMsg  string
		wantAttr string
	}{
		{
			"未登録のグループ",
			func(h http.Handler) *httptest.ResponseRecorder { return get(t, h, "/group?name=ghost") },
			`"msg":"request-failed"`, `"status":404`,
		},
		{
			"クロスオリジンの拒否",
			func(h http.Handler) *httptest.ResponseRecorder {
				return post(t, h, url.Values{"action": {"pin"}, "group": {"dev"}, "desired": {"running"}}, map[string]string{"Origin": "https://evil.example"})
			},
			`"msg":"request-failed"`, `"status":403`,
		},
		{
			"err= でリダイレクトされる操作の失敗",
			func(h http.Handler) *httptest.ResponseRecorder {
				return post(t, h, url.Values{"action": {"schedule"}, "group": {"dev"}, "start": {"not a cron"}}, nil)
			},
			`"msg":"operation-failed"`, `"action":"schedule"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, h, logBuf := newTestServerWithLog(t)

			tc.do(h)

			out := logBuf.String()
			assert.Contains(t, out, tc.wantMsg)
			assert.Contains(t, out, tc.wantAttr)
			assert.Contains(t, out, `"level":"ERROR"`, "失敗は ERROR で出さなければ絞り込めない")
		})
	}
}

// 設定を書き換える操作は、成功時もグループと内容を併せて記録する
// リクエストの完了行のみでは、対象のグループと変更の内容を特定できないためである
func TestSuccessfulOperationIsLogged(t *testing.T) {
	_, _, h, logBuf := newTestServerWithLog(t)

	require.Equal(t, http.StatusSeeOther, post(t, h, url.Values{"action": {"pin"}, "group": {"dev"}, "desired": {"running"}}, nil).Code)

	out := logBuf.String()
	assert.Contains(t, out, `"msg":"operation"`)
	assert.Contains(t, out, `"action":"pin"`)
	assert.Contains(t, out, `"group":"dev"`)
}

// client には接続元を記録する
// これはリソースポリシーの aws:SourceIp が許可の判定に用いる値と同一である
// X-Forwarded-For の先頭はクライアントが任意に設定できるため、これを採用してはならない
// API Gateway は受信した X-Forwarded-For の末尾へ観測した IP を追記し、先頭を上書きしない
func TestClientIPIsTheConnectionSourceNotTheForwardedForHeader(t *testing.T) {
	_, _, h, logBuf := newTestServerWithLog(t)

	req := httptest.NewRequest("GET", "/", nil)          // RemoteAddr は 192.0.2.1:1234
	req.Header.Set("X-Forwarded-For", "198.51.100.66, ") // 許可リスト内の IP を騙る詐称値を先頭に置く
	h.ServeHTTP(httptest.NewRecorder(), req)

	out := logBuf.String()
	assert.Contains(t, out, `"client":"192.0.2.1"`, "接続元を残す (ポートは除去する)")
	assert.NotContains(t, out, "198.51.100.66", "クライアントが送った X-Forwarded-For を信じてはならない")
}

// Lambda 上では、接続元は Lambda Web Adapter が渡す requestContext にのみ存在する
// RemoteAddr はアダプタからのループバック接続であるため、これを優先した場合、全リクエストが 127.0.0.1 として記録され、許可の判定に用いた IP の記録が失われる
func TestClientIPComesFromTheAdapterRequestContext(t *testing.T) {
	_, _, h, logBuf := newTestServerWithLog(t)

	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "127.0.0.1:41234"
	req.Header.Set("X-Amzn-Request-Context", `{"identity":{"sourceIp":"203.0.113.9"},"stage":"console"}`)
	h.ServeHTTP(httptest.NewRecorder(), req)

	out := logBuf.String()
	assert.Contains(t, out, `"client":"203.0.113.9"`)
	assert.NotContains(t, out, "127.0.0.1", "アダプタとの間のループバックアドレスを残してはならない")
}

// REST API(v1) と HTTP API(v2)/Function URL では、sourceIp の位置が異なる
// 後者へ切り替えたとき、client が空となってはならない
func TestClientIPReadsTheHTTPAPIRequestContextShape(t *testing.T) {
	_, _, h, logBuf := newTestServerWithLog(t)

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Amzn-Request-Context", `{"http":{"method":"GET","sourceIp":"203.0.113.10"}}`)
	h.ServeHTTP(httptest.NewRecorder(), req)

	assert.Contains(t, logBuf.String(), `"client":"203.0.113.10"`)
}

// requestContext を読めない場合も、client を空としてはならない
// アダプタの形式が変化した場合も、接続元の記録を維持する必要があるためである
func TestClientIPFallsBackToTheConnectionWhenRequestContextIsUnusable(t *testing.T) {
	for name, header := range map[string]string{
		"malformed":     `{"identity":`,
		"no source ip":  `{"identity":{},"stage":"console"}`,
		"not an object": `"console"`,
	} {
		t.Run(name, func(t *testing.T) {
			_, _, h, logBuf := newTestServerWithLog(t)

			req := httptest.NewRequest("GET", "/", nil) // RemoteAddr は 192.0.2.1:1234
			req.Header.Set("X-Amzn-Request-Context", header)
			h.ServeHTTP(httptest.NewRecorder(), req)

			assert.Contains(t, logBuf.String(), `"client":"192.0.2.1"`)
		})
	}
}

// ポートを含まないアドレスを受信した場合、SplitHostPort の失敗を理由に値を破棄してはならない
func TestClientIPHandlesAddressWithoutPort(t *testing.T) {
	_, _, h, logBuf := newTestServerWithLog(t)

	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "203.0.113.9"
	h.ServeHTTP(httptest.NewRecorder(), req)

	assert.Contains(t, logBuf.String(), `"client":"203.0.113.9"`)
}

// 探索に失敗したサイクルでは、孤立判定を見送ったことを画面へ表示する
// 孤立レコードの不在と、判定の未実施とを区別できなければならないためである
func TestDoctorPageSurfacesBlockedChecks(t *testing.T) {
	_, disc, h := newTestServer(t)
	disc.ErrByTagValue["dev"] = assert.AnError

	rec := get(t, h, "/doctor")

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "discover-error")
	assert.Contains(t, rec.Body.String(), "were <strong>not</strong> checked")
}
