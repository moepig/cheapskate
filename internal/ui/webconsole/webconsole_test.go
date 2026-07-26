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

// newTestServer と同じものに加えて、サーバが書いたログを返す
func newTestServerWithLog(t *testing.T) (*mocks.DynaStore, *porttest.Discoverer, http.Handler, *bytes.Buffer) {
	t.Helper()
	ctrl := gomock.NewController(t)
	api, db := mocks.NewDynaStore(ctrl)
	disc := porttest.NewDiscoverer()
	seedDevGroup(db, disc)
	var logBuf bytes.Buffer
	// 本番と同じ JSON ハンドラで書く（属性名まで含めて、実際に出る行を検証するためである）
	h := New(state.New(api, "t"), disc, nil, "", time.UTC, slog.New(slog.NewJSONHandler(&logBuf, nil))).Handler()
	return db, disc, h, &logBuf
}

// ログを検証しないテストの出力を汚さないためのロガー
func testLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.DiscardHandler)
}

// グループ "dev" と、そのセレクタに合致することになっているリソース 1 つを投入する
// discoverer のテストダブルはセレクタのタグ値をキーにしており、ここでのグループはどれも自身の名前をその値に使う
// そのため実物の Tagging API は関与しない
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
			"desired count: 2<br>scaling min: 1<br>scaling max: 3",
		},
		{
			"ecs-service with only desired count",
			model.Resource{Type: model.TypeEcsService, Tags: map[string]string{model.EcsDesiredCountTagKey: "5"}},
			"desired count: 5",
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

// グループページはグループ設定だけでなく、override と実際に discover したリソースのステータスも描画しなければならない
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

// グループページの "Current state" 列は、その場で問い合わせたリソースの実際の現在状態を表示しなければならない
// ここでは ecs-service の実際の desiredCount を Observation.Detail に畳み込んだものであり、同じ行の他所に出る最後の reconcile ステータスとは別物である
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

// 1 リソースの Describe が失敗しても、その行だけにエラーを出して他は通常どおり描く
// live state が読めないことと、グループの構成が読めないことは別である
// 「不明」と黙って表示するのではなく、なぜ読めなかったのかをその場に出さなければ、オペレータは停止済みなのか権限が足りないのかを区別できない
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

// Discover の失敗（例えば tag:GetResources 権限の欠落）はリクエストを失敗させず、グループページ内にそのまま描画しなければならない
// IAM 権限の不足や誤設定が 500 になってはならない
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

// グループに cron のフィールドがあるなら unpin は mode=schedule へ戻さなければならない
// Pin の「cron のフィールドは保たれる」という約束が、CLI 経由だけでなくコンソールからも実際に再開できることを担保する
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

// cron が一度も設定されていなければ unpin に再開すべきものはなく、mode=disabled へ落ちる
func TestOpUnpinFallsBackToDisabledWithoutCrons(t *testing.T) {
	db, _, h := newTestServer(t)
	rec := post(t, h, url.Values{"action": {"unpin"}, "group": {"dev"}}, nil)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())

	item := db.Item("group#dev")
	require.NotNil(t, item)
	assert.Equal(t, string(model.ModeDisabled), item["mode"].(*types.AttributeValueMemberS).Value)
}

// 書式が壊れた until はリダイレクトで差し戻し、override は 1 件も書かない
// TestOpOverrideRejectsPastDateTime が見ているのは解析できた過去日時であり、これはその手前の解析そのものが失敗する経路である
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

// フォームの action と group は、操作を試みる前に弾く
// この 2 つはフォームが正しければ必ず妥当なので、壊れているならフォーム側の不具合か手製のリクエストである
// 利用者に直させる err リダイレクトではなく 400 で返す
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

// フォーム本体そのものが復号できない場合も 400 で返す
// action も group も読めていない以上、どのグループへ差し戻せばよいかが決まらない
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

// Base（API Gateway のステージプレフィックス）が空でないなら、操作後のリダイレクトはすべてそのプレフィックスを含まなければならない
// これによりブラウザはそのプレフィックス配下に留まる
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

// すべてのレスポンスはフレーム内への埋め込みを拒否しなければならない
// 許可リストに載ったオペレータが攻撃者のページを開いても、透明な iframe 経由で remove や pin を実行させられない（クリックジャッキング対策）
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

// 解析できない Origin は同一オリジンだと確認できないのだから、拒否側へ倒さなければならない
// url.Parse の失敗を「Origin なし」と同じに扱うと、壊れた Origin を送るだけで検査を素通りできてしまう
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

// prune は表示済みの画面に対してではなく、押された時点で診断をやり直してから削除する
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

// 個々の削除失敗は doctor.Run のエラーにはならず finding に残るだけなので、画面側で数えて伝える
// 「N 件削除しました」だけを出すと、消せなかったレコードが残っていることに誰も気づかない
// 削除は冪等なので、もう一度実行すればよいことまで案内する
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

// 1 リクエストにつき開始と完了を 1 行ずつ残す
// 完了行しか出さないと、途中で消えたリクエスト（Lambda のタイムアウト等）が痕跡なしに消える
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

// 画面に返すエラーはログにも残す
// リダイレクトも 4xx/5xx も、ブラウザを閉じたら消えるものだけを記録先にしてはならない
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

// 設定を書き換える操作は、成功もグループと内容つきで残す
// リクエストの完了行だけではフォーム本体（どのグループを何にしたのか）が分からない
func TestSuccessfulOperationIsLogged(t *testing.T) {
	_, _, h, logBuf := newTestServerWithLog(t)

	require.Equal(t, http.StatusSeeOther, post(t, h, url.Values{"action": {"pin"}, "group": {"dev"}, "desired": {"running"}}, nil).Code)

	out := logBuf.String()
	assert.Contains(t, out, `"msg":"operation"`)
	assert.Contains(t, out, `"action":"pin"`)
	assert.Contains(t, out, `"group":"dev"`)
}

// client には接続元を残す
// これはリソースポリシーの aws:SourceIp が許可判定に使う値と同じものである
// X-Forwarded-For の先頭はクライアントが自由に書けるので、そちらを採ってはならない
// API Gateway は受け取った X-Forwarded-For の末尾に観測した IP を追記するだけであり、先頭は上書きしない
func TestClientIPIsTheConnectionSourceNotTheForwardedForHeader(t *testing.T) {
	_, _, h, logBuf := newTestServerWithLog(t)

	req := httptest.NewRequest("GET", "/", nil)          // RemoteAddr は 192.0.2.1:1234
	req.Header.Set("X-Forwarded-For", "198.51.100.66, ") // 許可リスト内の IP を騙る詐称値を先頭に置く
	h.ServeHTTP(httptest.NewRecorder(), req)

	out := logBuf.String()
	assert.Contains(t, out, `"client":"192.0.2.1"`, "接続元を残す（ポートは落とす）")
	assert.NotContains(t, out, "198.51.100.66", "クライアントが送った X-Forwarded-For を信じてはならない")
}

// Lambda 上では接続元は Lambda Web Adapter が渡す requestContext にしかない
// RemoteAddr はアダプタからのループバック接続なので、そちらを優先すると全リクエストが同じ 127.0.0.1 として記録され、許可された IP の記録が失われる
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

// REST API(v1) と HTTP API(v2)/Function URL では sourceIp の位置が違う
// 後者に切り替えたときに client が黙って空になることがあってはならない
func TestClientIPReadsTheHTTPAPIRequestContextShape(t *testing.T) {
	_, _, h, logBuf := newTestServerWithLog(t)

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Amzn-Request-Context", `{"http":{"method":"GET","sourceIp":"203.0.113.10"}}`)
	h.ServeHTTP(httptest.NewRecorder(), req)

	assert.Contains(t, logBuf.String(), `"client":"203.0.113.10"`)
}

// requestContext が読めないときに client を空にしてはならない
// アダプタの形式が変わっても、少なくとも接続元は残り続ける必要がある
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

// ポートの付かないアドレスで届いた場合に、SplitHostPort の失敗を握りつぶして値ごと捨ててはならない
func TestClientIPHandlesAddressWithoutPort(t *testing.T) {
	_, _, h, logBuf := newTestServerWithLog(t)

	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "203.0.113.9"
	h.ServeHTTP(httptest.NewRecorder(), req)

	assert.Contains(t, logBuf.String(), `"client":"203.0.113.9"`)
}

// 探索に失敗したサイクルでは孤立判定を見送ったことを画面に出す
// 「孤立レコードが 0 件」と「調べられなかった」を読み手が取り違えてはならない
func TestDoctorPageSurfacesBlockedChecks(t *testing.T) {
	_, disc, h := newTestServer(t)
	disc.ErrByTagValue["dev"] = assert.AnError

	rec := get(t, h, "/doctor")

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "discover-error")
	assert.Contains(t, rec.Body.String(), "were <strong>not</strong> checked")
}
