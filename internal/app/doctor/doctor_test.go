package doctor

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"cheapskate/internal/app/port/porttest"
	"cheapskate/internal/core/model"
	"cheapskate/internal/state"
	"cheapskate/internal/state/mocks"
)

var now = time.Date(2026, 7, 15, 3, 0, 0, 0, time.UTC)

type fixture struct {
	db         *mocks.DynaStore
	store      *state.Store
	discoverer *porttest.Discoverer
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	api, db := mocks.NewDynaStore(gomock.NewController(t))
	return &fixture{db: db, store: state.New(api, "state"), discoverer: porttest.NewDiscoverer()}
}

func s[T ~string](v T) types.AttributeValue { return &types.AttributeValueMemberS{Value: string(v)} }

// セレクタのタグ値をグループ名と一致させた group# アイテムを用意する
// reconcile のテストと同じ規約であり、discoverer.ByTagValue[name] がそのグループのメンバーとなる
func (f *fixture) seedGroup(name string, mode model.Mode, desired model.DesiredState) {
	f.db.Seed(map[string]types.AttributeValue{
		"pk": s("group#" + name), "mode": s(mode), "desired": s(desired),
		"tag_key": s("env"), "tag_value": s(name),
		"types": &types.AttributeValueMemberSS{Value: []string{string(model.TypeRdsInstance)}},
	})
}

func (f *fixture) seedOverride(group string, desired model.DesiredState, expiresAt time.Time) {
	f.db.Seed(map[string]types.AttributeValue{
		"pk": s("override#" + group), "desired": s(desired),
		"expires_at": &types.AttributeValueMemberN{Value: fmt.Sprint(expiresAt.Unix())},
	})
}

func (f *fixture) seedStatus(resourceID string, attrs map[string]string) {
	item := map[string]types.AttributeValue{"pk": s("status#" + resourceID)}
	for k, v := range attrs {
		item[k] = s(v)
	}
	f.db.Seed(item)
}

func (f *fixture) run(t *testing.T, opts Options) Report {
	t.Helper()
	report, err := Run(context.Background(), f.store, f.discoverer, now, opts)
	require.NoError(t, err)
	return report
}

// kind に一致する検出項目だけを返す
func only(r Report, kind Kind) []Finding {
	var out []Finding
	for _, f := range r.Findings {
		if f.Kind == kind {
			out = append(out, f)
		}
	}
	return out
}

func rds(ref string) model.Resource {
	return model.Resource{Type: model.TypeRdsInstance, Ref: ref}
}

func TestCleanTableReportsNothing(t *testing.T) {
	f := newFixture(t)
	f.seedGroup("dev", model.ModePinned, model.DesiredStopped)
	f.discoverer.ByTagValue["dev"] = []model.Resource{rds("dev-db")}
	f.seedStatus("rds-instance#dev-db", map[string]string{"last_action": "stop"})

	report := f.run(t, Options{})

	assert.True(t, report.Clean(), "unexpected findings: %+v", report.Findings)
	assert.Empty(t, report.Blocked)
}

// グループの削除後に override# / status#group# のみが残る状態は、RemoveGroup の中断または手作業によるレコード削除で発生する
// group# の不在は Scan だけで確定するため、削除の可否は一意に定まる
func TestOrphanedGroupRecordsAreFoundAndPruned(t *testing.T) {
	f := newFixture(t)
	f.seedOverride("ghost", model.DesiredRunning, now.Add(time.Hour))
	f.seedStatus("group#ghost", map[string]string{"last_error": "boom"})

	report := f.run(t, Options{})
	require.Len(t, only(report, KindOrphanOverride), 1)
	require.Len(t, only(report, KindOrphanGroupStatus), 1)
	for _, fi := range report.Findings {
		assert.True(t, fi.Prunable, "%s must be prunable", fi.Kind)
		assert.False(t, fi.Pruned, "a read-only run must not delete anything")
	}
	assert.NotNil(t, f.db.Item("override#ghost"), "a read-only run must not delete anything")

	report = f.run(t, Options{Prune: true})

	assert.Equal(t, 2, report.Pruned)
	assert.Nil(t, f.db.Item("override#ghost"))
	assert.Nil(t, f.db.Item("status#group#ghost"))
}

// どのグループのセレクタにも一致しないリソースの status# は、タグの削除、リソースの削除、
// グループの削除のいずれかにより残存した監査記録である
func TestOrphanResourceStatusIsFoundAndPruned(t *testing.T) {
	f := newFixture(t)
	f.seedGroup("dev", model.ModePinned, model.DesiredStopped)
	f.discoverer.ByTagValue["dev"] = []model.Resource{rds("dev-db")}
	f.seedStatus("rds-instance#dev-db", map[string]string{"last_action": "stop"})
	f.seedStatus("rds-instance#untagged-db", map[string]string{"last_error": "stale"})

	report := f.run(t, Options{Prune: true})

	found := only(report, KindOrphanStatus)
	require.Len(t, found, 1, "only the unmatched resource is an orphan")
	assert.Equal(t, "rds-instance#untagged-db", found[0].Resource)
	assert.Equal(t, "status#rds-instance#untagged-db", found[0].PK)
	assert.True(t, found[0].Pruned)
	assert.Nil(t, f.db.Item("status#rds-instance#untagged-db"))
	assert.NotNil(t, f.db.Item("status#rds-instance#dev-db"), "a live resource's status must survive")
}

// 探索が 1 つでも失敗したサイクルでは、どのグループにも属していないことを根拠とする削除を見送らなければならない
// Tagging API の失敗により一時的に探索できないリソースの履歴を削除してはならない
func TestDiscoverFailureBlocksOrphanStatusPruning(t *testing.T) {
	f := newFixture(t)
	f.seedGroup("dev", model.ModePinned, model.DesiredStopped)
	f.discoverer.ErrByTagValue["dev"] = errors.New("AccessDenied")
	f.seedStatus("rds-instance#dev-db", map[string]string{"last_action": "stop"})

	report := f.run(t, Options{Prune: true})

	require.Len(t, only(report, KindDiscoverError), 1)
	assert.Empty(t, only(report, KindOrphanStatus), "membership is unknown, so nothing is an orphan")
	require.Len(t, report.Blocked, 1)
	assert.Contains(t, report.Blocked[0], "dev")
	assert.Zero(t, report.Pruned)
	assert.NotNil(t, f.db.Item("status#rds-instance#dev-db"))
}

// セレクタが重複すると、reconciler ではグループ名順で最初のグループのみが適用され、残りは反映されない
// 設定の不整合であるため、報告のみを行い削除はしない
func TestSelectorOverlapIsReportedButNotPrunable(t *testing.T) {
	f := newFixture(t)
	f.seedGroup("a-first", model.ModePinned, model.DesiredStopped)
	f.seedGroup("z-second", model.ModePinned, model.DesiredRunning)
	shared := rds("shared-db")
	f.discoverer.ByTagValue["a-first"] = []model.Resource{shared}
	f.discoverer.ByTagValue["z-second"] = []model.Resource{shared}

	report := f.run(t, Options{Prune: true})

	found := only(report, KindSelectorOverlap)
	require.Len(t, found, 1)
	assert.Equal(t, "rds-instance#shared-db", found[0].Resource)
	assert.Contains(t, found[0].Detail, `"a-first"`, "the winning group must be named")
	assert.False(t, found[0].Prunable)
	assert.Zero(t, report.Pruned)
}

// 遷移中のリソースは reconciler が毎サイクル skip し、エラーも通知も出さないため、transitioning_since が唯一の検知経路となる
func TestStuckTransitioningRespectsThreshold(t *testing.T) {
	f := newFixture(t)
	f.seedGroup("dev", model.ModePinned, model.DesiredStopped)
	f.discoverer.ByTagValue["dev"] = []model.Resource{rds("slow-db"), rds("normal-db")}
	f.seedStatus("rds-instance#slow-db", map[string]string{
		"transitioning_since": now.Add(-3 * time.Hour).Format(time.RFC3339),
	})
	f.seedStatus("rds-instance#normal-db", map[string]string{
		"transitioning_since": now.Add(-2 * time.Minute).Format(time.RFC3339),
	})

	report := f.run(t, Options{StuckAfter: 30 * time.Minute})

	found := only(report, KindStuckTransitioning)
	require.Len(t, found, 1, "only the one past the threshold counts as stuck")
	assert.Equal(t, "rds-instance#slow-db", found[0].Resource)
	assert.Equal(t, "dev", found[0].Group, "the owning group makes the finding actionable")
	assert.Contains(t, found[0].Detail, "3h0m0s")
	assert.False(t, found[0].Prunable, "a stuck resource needs a decision, not a deleted record")
}

// 解析できない transitioning_since は、遷移の長期化ではなく書き込み側の不具合を示す
// 待機では解消しないため、stuck ではなく壊れたレコードとして報告する
func TestUnparsableTransitioningSinceIsReportedAsCorruptNotStuck(t *testing.T) {
	f := newFixture(t)
	f.seedGroup("dev", model.ModePinned, model.DesiredStopped)
	f.discoverer.ByTagValue["dev"] = []model.Resource{rds("dev-db")}
	f.seedStatus("rds-instance#dev-db", map[string]string{"transitioning_since": "yesterday"})

	report := f.run(t, Options{Prune: true, StuckAfter: time.Minute})

	found := only(report, KindCorruptRecord)
	require.Len(t, found, 1)
	assert.Equal(t, "rds-instance#dev-db", found[0].Resource)
	assert.Equal(t, "status#rds-instance#dev-db", found[0].PK, "手で直せるよう生の pk を出す")
	assert.Contains(t, found[0].Detail, "not RFC3339")
	assert.Empty(t, only(report, KindStuckTransitioning), "経過時間が測れない以上 stuck とは言えない")
	assert.False(t, found[0].Prunable, "壊れた値の意味が分からないうちは消さない")
	assert.Zero(t, report.Pruned)
	assert.NotNil(t, f.db.Item("status#rds-instance#dev-db"))
}

// 登録済みだが reconciler が従えない設定は、そのグループのリソースが 1 件も操作されないことを意味する
// 設定は診断の対象外であるため、報告のみを行い削除はしない
func TestUnusableGroupConfigIsReported(t *testing.T) {
	f := newFixture(t)
	f.seedGroup("broken", model.ModePinned, "") // pinned なのに desired がない
	f.discoverer.ByTagValue["broken"] = []model.Resource{rds("dev-db")}

	report := f.run(t, Options{Prune: true})

	found := only(report, KindConfigError)
	require.Len(t, found, 1)
	assert.Equal(t, "broken", found[0].Group)
	assert.Contains(t, found[0].Detail, "requires desired")
	assert.False(t, found[0].Prunable)
	assert.NotNil(t, f.db.Item("group#broken"), "配置の誤った設定でも削除してはならない")
}

// mode=schedule で cron が壊れているグループも、reconciler が従えない設定に該当する
// reconciler はこのグループを毎サイクル同じ設定エラーとするが、doctor が報告しない場合、症状は SNS 通知にのみ現れ、state テーブルからは検出できない
// この検出は model.ParseGroup が cron と timezone を検証することで成立する
func TestUnusableScheduleCronIsReported(t *testing.T) {
	cases := map[string]map[string]types.AttributeValue{
		"unparsable start cron": {"start_cron": s("every morning")},
		"unparsable stop cron":  {"start_cron": s("0 9 * * 1-5"), "stop_cron": s("nightly")},
		"unknown timezone":      {"start_cron": s("0 9 * * 1-5"), "timezone": s("Mars/Olympus")},
		"no cron at all":        {"timezone": s("Asia/Tokyo")},
	}
	for name, attrs := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			item := map[string]types.AttributeValue{
				"pk": s("group#sched"), "mode": s(model.ModeSchedule),
				"tag_key": s("env"), "tag_value": s("sched"),
				"types": &types.AttributeValueMemberSS{Value: []string{string(model.TypeRdsInstance)}},
			}
			for k, v := range attrs {
				item[k] = v
			}
			f.db.Seed(item)
			f.discoverer.ByTagValue["sched"] = []model.Resource{rds("dev-db")}

			report := f.run(t, Options{Prune: true})

			found := only(report, KindConfigError)
			require.Len(t, found, 1)
			assert.Equal(t, "sched", found[0].Group)
			assert.False(t, found[0].Prunable)
			assert.NotNil(t, f.db.Item("group#sched"), "設定は報告するだけで消さない")
		})
	}
}

// 読めないレコードは、そのグループのメンバーの特定を不能にする
// 孤立判定はグループのメンバー全体の列挙を前提とするため、判定を見送る
func TestCorruptRecordIsReportedAndBlocksPruning(t *testing.T) {
	f := newFixture(t)
	f.db.Seed(map[string]types.AttributeValue{
		"pk": s("override#dev"), "desired": s(model.DesiredRunning),
		"expires_at": s("not-a-number"),
	})
	f.seedStatus("rds-instance#somewhere", map[string]string{"last_error": "stale"})

	report := f.run(t, Options{Prune: true})

	require.Len(t, only(report, KindCorruptRecord), 1)
	assert.Empty(t, only(report, KindOrphanStatus))
	assert.NotEmpty(t, report.Blocked)
	assert.Zero(t, report.Pruned)
}

// 読めない group# アイテムを、未登録のグループとして扱ってはならない
// state.ScanAll の HasGroup は group# の読み取り成否を表し、group# の不在を表さない
// group# 自身が壊れている行では HasGroup=false と Err の両方が立つ (state_test.go を参照)
// これを孤立の根拠とすると、doctor は同一のレポートで破損と未登録を同時に報告し、後者を理由に有効な override と group-status を削除する
func TestCorruptGroupRecordDoesNotOrphanItsOwnRecords(t *testing.T) {
	f := newFixture(t)
	f.db.Seed(map[string]types.AttributeValue{
		"pk":   s("group#dev"),
		"mode": &types.AttributeValueMemberBOOL{Value: true}, // 文字列でなければならず、UnmarshalMap が失敗する
	})
	f.seedOverride("dev", model.DesiredRunning, now.Add(time.Hour))
	f.seedStatus("group#dev", map[string]string{"last_error": "boom"})

	report := f.run(t, Options{Prune: true})

	require.Len(t, only(report, KindCorruptRecord), 1)
	assert.Empty(t, only(report, KindOrphanOverride), "group# アイテムは存在する (読めないだけである) ので孤立していない")
	assert.Empty(t, only(report, KindOrphanGroupStatus))
	assert.NotEmpty(t, report.Blocked)
	assert.Zero(t, report.Pruned)
	assert.NotNil(t, f.db.Item("override#dev"), "壊れたのは group# なので、有効な override を消してはならない")
	assert.NotNil(t, f.db.Item("status#group#dev"))
}

// 検出項目は kind → group → resource の順に並べる
// CLI の JSON と web console の一覧はこの順序をそのまま表示するため、同一のテーブルに対する並びは常に一致しなければならない
// 順序が変動すると、doctor の出力間の差分が状態の変化を表さなくなる
func TestFindingsAreSortedDeterministically(t *testing.T) {
	f := newFixture(t)
	// 同じ kind を group 名の逆順で投入し、並べ替えが行われることを確かめる
	f.seedOverride("z-ghost", model.DesiredRunning, now.Add(time.Hour))
	f.seedOverride("a-ghost", model.DesiredRunning, now.Add(time.Hour))
	// kind と group が同じで resource のみが異なる 2 件
	f.seedGroup("dev", model.ModePinned, model.DesiredStopped)
	f.discoverer.ByTagValue["dev"] = []model.Resource{rds("dev-db")}
	f.seedStatus("rds-instance#z-orphan", map[string]string{"last_error": "stale"})
	f.seedStatus("rds-instance#a-orphan", map[string]string{"last_error": "stale"})

	report := f.run(t, Options{})

	var got []string
	for _, fi := range report.Findings {
		got = append(got, fmt.Sprintf("%s/%s/%s", fi.Kind, fi.Group, fi.Resource))
	}
	assert.Equal(t, []string{
		"orphan-override/a-ghost/",
		"orphan-override/z-ghost/",
		"orphan-status//rds-instance#a-orphan",
		"orphan-status//rds-instance#z-orphan",
	}, got, "findings must be sorted by kind, then group, then resource")
}

// セレクタ未設定のグループはメンバーを持たないため、探索を呼ばない
// Tagging API への不要な呼び出しとなり、失敗した場合は孤立判定も見送りとなる
func TestGroupWithoutSelectorIsNotDiscovered(t *testing.T) {
	f := newFixture(t)
	f.db.Seed(map[string]types.AttributeValue{
		"pk": s("group#empty"), "mode": s(model.ModeDisabled), // セレクタのタグなし
	})
	f.seedStatus("rds-instance#somewhere", map[string]string{"last_error": "stale"})

	report := f.run(t, Options{})

	assert.Zero(t, f.discoverer.Calls(), "セレクタがなければ探索してはならない")
	assert.Empty(t, report.Blocked, "メンバーがいないことは分かっているので孤立判定は見送らない")
	assert.Len(t, only(report, KindOrphanStatus), 1)
}

// 空ではないが妥当でないセレクタは Discover へ渡せない
// そのグループのメンバーを列挙できないため、孤立判定を見送らなければならない
// 見送らない場合、どのグループにも属していないという判定の根拠が、実際には判定の未実施となるためである
func TestGroupWithInvalidSelectorBlocksOrphanPruning(t *testing.T) {
	f := newFixture(t)
	f.db.Seed(map[string]types.AttributeValue{
		"pk": s("group#broken"), "mode": s(model.ModeDisabled),
		"tag_key": s("env"), "tag_value": s("dev"),
		"types": &types.AttributeValueMemberSS{Value: []string{"sqs-queue"}}, // 未知のリソース種別
	})
	f.seedStatus("rds-instance#somewhere", map[string]string{"last_error": "stale"})

	report := f.run(t, Options{Prune: true})

	assert.Zero(t, f.discoverer.Calls(), "妥当でないセレクタを Tagging API へ投げてはならない")
	require.Len(t, report.Blocked, 1)
	assert.Contains(t, report.Blocked[0], "broken")
	assert.Empty(t, only(report, KindOrphanStatus), "メンバーが不明な以上、孤立していると言える根拠がない")
	assert.Zero(t, report.Pruned)
	assert.NotNil(t, f.db.Item("status#rds-instance#somewhere"))
}

// Prunable な検出項目のみが削除の対象となる
// 設定エラーとセレクタ重複は、--prune を指定しても変更しない
func TestPruneLeavesNonPrunableFindingsAlone(t *testing.T) {
	f := newFixture(t)
	f.seedGroup("broken", model.ModePinned, "") // pinned なのに desired がない
	f.discoverer.ByTagValue["broken"] = []model.Resource{rds("dev-db")}
	f.seedOverride("ghost", model.DesiredRunning, now.Add(time.Hour))

	report := f.run(t, Options{Prune: true})

	assert.Equal(t, 1, report.Pruned, "消えてよいのは孤立した override だけ")
	assert.Nil(t, f.db.Item("override#ghost"))
	assert.NotNil(t, f.db.Item("group#broken"), "設定エラーは報告するだけで消さない")
	for _, fi := range only(report, KindConfigError) {
		assert.False(t, fi.Pruned)
		assert.Empty(t, fi.PruneErr, "削除を試みていないため失敗の記録も残らない")
	}
}

// 削除が 1 件失敗しても残りの削除を継続し、失敗した項目にのみ理由を記録する
// DeleteItem は冪等であるため、原因の解消後に doctor を再実行すればよい
func TestPruneFailureIsIsolatedPerFinding(t *testing.T) {
	f := newFixture(t)
	f.seedOverride("ghost", model.DesiredRunning, now.Add(time.Hour))
	f.seedStatus("group#ghost", map[string]string{"last_error": "boom"})
	f.db.FailOn("delete", "override#ghost", errors.New("throttled"))

	report := f.run(t, Options{Prune: true})

	assert.Equal(t, 1, report.Pruned)
	var failed, pruned int
	for _, fi := range report.Findings {
		if fi.PruneErr != "" {
			failed++
			assert.Contains(t, fi.PruneErr, "throttled")
		}
		if fi.Pruned {
			pruned++
		}
	}
	assert.Equal(t, 1, failed)
	assert.Equal(t, 1, pruned)
}
