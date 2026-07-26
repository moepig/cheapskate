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

// セレクタのタグ値をグループ名そのものにした group# アイテムを用意する
// reconcile のテストと同じ約束で、discoverer.ByTagValue[name] がそのグループの中身になる
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

// グループを消したあとに override# / status#group# だけが残るのは、RemoveGroup が途中で落ちたか、生のレコードを手で消したときに起きる
// group# が存在しないことは Scan だけで確定するので、これは曖昧さなく削除できる
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

// どのグループのセレクタにも一致しないリソースの status# は、タグを外した・リソースを消した・
// グループを消したのいずれかで取り残された監査記録である
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

// 探索が 1 つでも失敗したサイクルでは、「どのグループにも属していない」を根拠にした削除を必ず見送る
// Tagging API の失敗で一時的に見えていないだけのリソースの履歴を消してはならない
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

// セレクタが重なると、reconciler ではグループ名順で最初のグループだけが効き、残りは黙って無視される
// 人間が直すべき設定の不整合なので、報告はするが削除はしない
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

// 遷移中のリソースは reconciler が毎サイクル黙って skip するので、transitioning_since が唯一の手がかりになる
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

// 解析できない transitioning_since は「遷移が長引いている」ではなく、書き込み側の不具合を示す
// 待っても直らないので stuck ではなく壊れたレコードとして報告し、判断を人間に委ねる
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

// 登録済みだが reconciler が従えない設定は、そのグループのリソースが 1 件も動かないことを意味する
// 設定そのものは利用者のものなので、報告はしても消さない
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
	assert.NotNil(t, f.db.Item("group#broken"), "配置ミスの設定でも勝手に消してはならない")
}

// mode=schedule で cron が壊れているグループも「reconciler が従えない設定」である
// reconciler はこのグループを毎サイクル同じ設定エラーで落とすが、doctor が黙っていると症状は SNS 通知にしか現れず、state テーブルは健全に見えてしまう
// この検出が成り立つのは model.ParseGroup が cron と timezone まで検証するからである
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

// 読めないレコードは、そのグループのメンバーが何なのかを分からなくする
// 孤立判定はグループのメンバー全体を数え上げられて初めて成立するので、判定ごと見送る
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

// 読めない group# アイテムを「登録されていないグループ」と取り違えてはならない
// state.ScanAll の HasGroup は「group# を読めた」であって「group# がない」ではないので、group# 自身が壊れている行では HasGroup=false と Err の両方が立つ（state_test.go を参照）
// これを孤立していることの根拠にすると、doctor は同じレポートの中で「壊れている」と「登録されていない」を同時に主張し、後者を理由に生きている override と group-status を削除してしまう
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
	assert.Empty(t, only(report, KindOrphanOverride), "group# アイテムは存在する（読めないだけ）ので孤立していない")
	assert.Empty(t, only(report, KindOrphanGroupStatus))
	assert.NotEmpty(t, report.Blocked)
	assert.Zero(t, report.Pruned)
	assert.NotNil(t, f.db.Item("override#dev"), "壊れたのは group# なので、有効な override を消してはならない")
	assert.NotNil(t, f.db.Item("status#group#dev"))
}

// 検出項目は kind → group → resource の順に並べる
// CLI の JSON も web console の一覧もこの順をそのまま見せるので、同じテーブルなら毎回同じ並びでなければならない
// 順が揺れると、doctor の出力どうしの差分が実際の変化を表さなくなる
func TestFindingsAreSortedDeterministically(t *testing.T) {
	f := newFixture(t)
	// 同じ kind（orphan-override）を group 名の逆順で投入し、並べ替えが実際に起きることを確かめる
	f.seedOverride("z-ghost", model.DesiredRunning, now.Add(time.Hour))
	f.seedOverride("a-ghost", model.DesiredRunning, now.Add(time.Hour))
	// 同じ kind・同じ group（空）で resource だけが違う 2 件
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

// セレクタ未設定のグループはメンバーを持たないので、探索を呼ぶ意味がない
// Tagging API への無駄な往復であり、失敗すれば孤立判定まで巻き添えで見送ることになる
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

// 空ではないが妥当でないセレクタは Discover に渡せない
// そのグループのメンバーを数え上げられない以上、孤立判定は見送らなければならない
// 「どのグループにも属していない」の根拠が、実際には「調べられなかった」になってしまうためである
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

// Prunable な検出項目だけが削除の対象になる
// 設定エラーやセレクタ重複は人間が直すべき判断なので、--prune を付けても触らない
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
		assert.Empty(t, fi.PruneErr, "そもそも削除を試みてすらいないので失敗の記録も残らない")
	}
}

// 削除が 1 件失敗しても残りの削除は続け、失敗した項目にだけ理由を残す
// DeleteItem は冪等なので、直したうえで doctor をもう一度流せばよい
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
