// cmd/cheapskate-cli の実装であり、cheapskate の設定 CLI である
// DynamoDB の state テーブルにある group# と override# のアイテムを操作する
// 探索には読み取り専用の tag:GetResources API を用い、`show` のリソース単位の現在状態には種別ごとの読み取り専用 Describe API を用いる
// RDS/ECS/EC2 の Stop/Start コントロール API は呼ばない。これを呼ぶのは reconciler の Lambda である
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"

	"cheapskate/internal/app/doctor"
	"cheapskate/internal/app/groups"
	"cheapskate/internal/app/port"
	"cheapskate/internal/core/model"
	"cheapskate/internal/state"
	"cheapskate/internal/wire"
)

// Main が表示する -h/-help のテキスト
var Usage = `cheapskate-cli — cheapskate configuration CLI

Usage:
  cheapskate-cli [-table TABLE] <command> [arguments]

Commands:
  list                                     all target groups with their config, override,
                                           and group-level status
  show --group NAME                        group config + override + live-discovered
                                           member resources (with status)
  set-selector --group NAME --tag-key KEY --tag-value VALUE --types TYPES
                                           define which AWS resources the group manages
                                           via tag key/value + resource types
                                           (comma-separated); creates the group on first
                                           use (mode=disabled until pinned or scheduled)
  remove --group NAME                      delete the group, its override, and its
                                           group-level status (matched AWS resources are
                                           never touched — just untag them to detach)
  pin --group NAME running|stopped         keep every matched resource in a fixed state
  unpin --group NAME                       release mode=pinned: resumes mode=schedule if
                                           the group has cron settings, else mode=disabled
  schedule --group NAME [-start CRON] [-stop CRON] [-timezone TZ]
                                           cron-based start/stop for all matched resources
  disable --group NAME                     keep the config but stop managing
  override --group NAME running|stopped -for DURATION
                                           temporary override for all matched resources
                                           (expires via TTL); rejected while the group is
                                           disabled
  clear-override --group NAME              remove the override now
  doctor [--prune] [--stuck-after DUR]     diagnose state-table inconsistencies: orphaned
                                           override/status records, corrupt or unusable
                                           group config, overlapping selectors, and
                                           resources stuck mid-transition. Read-only unless
                                           --prune, which deletes only the unambiguously
                                           orphaned records (never config)

Selector types (comma-separated for --types): ` + strings.Join(model.TypeNames(model.KnownTypes), ", ") + `

Membership is computed live from AWS resource tags (tag:GetResources) — there is no
separate resource-registration step. Tag a resource with the group's selector's
key/value and it is picked up on the next reconcile cycle (and shown by "show").

Every command prints exactly one JSON object on stdout; a failure prints {"error": "..."} on
stderr and exits 1. This usage text is the only non-JSON output (-h / -help).

The table name comes from -table or the CHEAPSKATE_TABLE environment variable. AWS credentials/region use the standard SDK chain.
`

// プロセスのエントリポイントであり、args を実行し、結果を出力と終了コードへ変換する
// cmd/cheapskate-cli はこれを呼ぶのみである
func Main(args []string) {
	if err := Run(args, os.Stdout); err != nil {
		// -h/-help は失敗ではないため、usage テキストを返して 0 で終了する
		// このテキストは、JSON として出力しない唯一の出力である
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(os.Stderr, Usage)
			return
		}
		// エラーオブジェクトの書き出しが失敗した場合、追加の報告手段は存在しない
		// この場合も終了コードが失敗を伝える
		_ = writeJSON(os.Stderr, errorOutput{Error: err.Error()})
		os.Exit(1)
	}
}

// 失敗時に stderr へ出力する JSON オブジェクト
// これにより、この CLI の出力を解析する側は、失敗時もテキストの解析へ切り替えずに済む
type errorOutput struct {
	Error string `json:"error"`
}

// v を、コマンドが出力する唯一のインデント付き JSON オブジェクトとして出力する
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// 誤用を戻り値のエラーとしてのみ報告するフラグセットを返す
// flag 自身のテキスト出力は破棄する
// これにより、この CLI の JSON 出力へ混入しない
// -h/-help は flag.ErrHelp を返し、main が usage テキストで応答する
func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// cheapskate-cli が state テーブルに求める範囲
// 設定操作 (groups.Store) と診断 (doctor.Store) が必要とする範囲の和であり、それ以外を含まない
// UpdateStatus を含まないため、CLI から reconciler の監査証跡を書き換える経路は存在しない
type store interface {
	groups.Store
	doctor.Store
}

// CLI の呼び出し 1 回を実行し、単一の JSON 結果オブジェクトを out へ出力する
func Run(args []string, out io.Writer) error {
	global := newFlagSet("cheapskate-cli")
	table := global.String("table", os.Getenv("CHEAPSKATE_TABLE"), "DynamoDB state table name")
	if err := global.Parse(args); err != nil {
		return err
	}

	rest := global.Args()
	if len(rest) == 0 {
		return fmt.Errorf("missing command (see: cheapskate-cli -h)")
	}
	command, rest := rest[0], rest[1:]

	if *table == "" {
		return fmt.Errorf("state table not set (use -table or CHEAPSKATE_TABLE)")
	}
	ctx := context.Background()
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return fmt.Errorf("load AWS config: %w", err)
	}
	s := state.New(dynamodb.NewFromConfig(cfg), *table)
	discoverer := wire.Discoverer(cfg)

	switch command {
	case "list":
		return cmdList(ctx, s, out)
	case "show":
		return cmdShow(ctx, s, discoverer, wire.Describers(cfg), rest, out)
	case "set-selector":
		return cmdSetSelector(ctx, s, rest, out)
	case "remove":
		return cmdRemove(ctx, s, rest, out)
	case "pin":
		return cmdPin(ctx, s, rest, out)
	case "unpin":
		return cmdUnpin(ctx, s, rest, out)
	case "schedule":
		return cmdSchedule(ctx, s, rest, out)
	case "disable":
		return cmdDisable(ctx, s, rest, out)
	case "override":
		return cmdOverride(ctx, s, rest, out)
	case "clear-override":
		return cmdClearOverride(ctx, s, rest, out)
	case "doctor":
		return cmdDoctor(ctx, s, discoverer, rest, out)
	default:
		return fmt.Errorf("unknown command %q (see: cheapskate-cli -h)", command)
	}
}

// args に対して fs を解析し、フラグでないトークンを位置引数として収集する
// これにより、フラグと位置引数の順序を問わず解析できる
// ここでの位置引数は常に "running" または "stopped" であり、"-" で始まることはない
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos, args = append(pos, args[0]), args[1:]
	}
}

// 出力するグループの設定であり、model.GroupSpec とは異なる
// アイテムの "pk" は保存上の詳細であるため含めず、セレクタは 3 つの tag_* フィールドではなく入れ子のオブジェクトとして出力する
type configJSON struct {
	Mode      model.Mode         `json:"mode,omitempty"`
	Desired   model.DesiredState `json:"desired,omitempty"`
	StartCron string             `json:"start_cron,omitempty"`
	StopCron  string             `json:"stop_cron,omitempty"`
	Timezone  string             `json:"timezone,omitempty"`
	Selector  *selectorJSON      `json:"selector,omitempty"` // セレクタが未設定のグループでは nil
}

type selectorJSON struct {
	TagKey   string               `json:"tag_key"`
	TagValue string               `json:"tag_value"`
	Types    []model.ResourceType `json:"types"`
}

// 有効な override
// ExpiresAt は、保存アイテムの epoch 秒でもローカル時刻の文字列でもなく、RFC3339 の UTC とする
// これにより、機械的な比較が可能となり、解釈も一意に定まる
type overrideJSON struct {
	Desired   model.DesiredState `json:"desired"`
	ExpiresAt string             `json:"expires_at"`
}

// list と show が出力するグループ 1 件であり、設定に加えて、解決済みの override とグループ単位のステータスを持つ
// Error は、このグループの壊れたアイテムに対するエラーを保持する
// この場合もグループを一覧に残す
type groupJSON struct {
	Name string `json:"name"`
	configJSON
	Override *overrideJSON `json:"override,omitempty"`
	Status   model.Status  `json:"status"`
	Error    string        `json:"error,omitempty"`
}

func newConfigJSON(item model.GroupSpec) configJSON {
	cfg := configJSON{
		Mode: item.Mode, Desired: item.Desired,
		StartCron: item.StartCron, StopCron: item.StopCron, Timezone: item.Timezone,
	}
	if sel := item.Selector(); !sel.Empty() {
		cfg.Selector = &selectorJSON{TagKey: sel.TagKey, TagValue: sel.TagValue, Types: sel.Types}
	}
	return cfg
}

func newOverrideJSON(o *model.Override) *overrideJSON {
	if o == nil {
		return nil
	}
	return &overrideJSON{Desired: o.Desired, ExpiresAt: time.Unix(o.ExpiresAt, 0).UTC().Format(time.RFC3339)}
}

type listOutput struct {
	Command string      `json:"command"`
	Groups  []groupJSON `json:"groups"`
}

func cmdList(ctx context.Context, s store, out io.Writer) error {
	rows, err := groups.List(ctx, s, time.Now())
	if err != nil {
		return err
	}
	// nil としない
	// 空の一覧は null ではなく "groups": [] として出力しなければならない
	groups := make([]groupJSON, 0, len(rows))
	for _, row := range rows {
		g := groupJSON{Name: row.Name, configJSON: newConfigJSON(row.Group), Override: newOverrideJSON(row.Override), Status: row.Status}
		if row.Err != nil {
			g.Error = row.Err.Error()
		}
		groups = append(groups, g)
	}
	return writeJSON(out, listOutput{Command: "list", Groups: groups})
}

// showResource と showOutput は cmdShow の JSON を構成する
// グループの動的に探索したメンバーリソースを、ステータスとともに解決した結果を保持する
type showResource struct {
	Type    model.ResourceType `json:"type"`
	Ref     string             `json:"ref"`
	ARN     string             `json:"arn"`
	Config  any                `json:"config,omitempty"` // タグから読んだ種別固有の設定であり、該当がない種別では省略される
	Live    *model.Observation `json:"live,omitempty"`   // 都度問い合わせた現在の状態であり、Describer が結線されていない種別では省略される
	LiveErr string             `json:"live_error,omitempty"`
	Status  model.Status       `json:"status"`
}

// r のタグ由来の設定を JSON オブジェクトへ変換する。設定が存在しない場合は nil を返す
// 設定として扱うタグを定義するのは種別の宣言 (model.TypeInfo.ConfigTags) であるため、この関数は種別を参照しない
func resourceConfig(r model.Resource) any {
	cfg := r.Config()
	if len(cfg) == 0 {
		return nil
	}
	out := make(map[string]string, len(cfg))
	for _, c := range cfg {
		out[c.Name] = c.Value
	}
	return out
}

type showOutput struct {
	Command     string         `json:"command"`
	Group       groupJSON      `json:"group"`
	Resources   []showResource `json:"resources"`
	DiscoverErr string         `json:"discover_error,omitempty"`
}

func cmdShow(ctx context.Context, s store, d port.Discoverer, describers map[model.ResourceType]port.Describer, args []string, w io.Writer) error {
	fs := newFlagSet("show")
	var group string
	fs.StringVar(&group, "group", "", "group name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if group == "" {
		return fmt.Errorf("--group is required")
	}
	detail, err := groups.GetDetail(ctx, s, d, describers, group, time.Now())
	if err != nil {
		return err
	}
	out := showOutput{
		Command: "show",
		Group: groupJSON{
			Name:       detail.Name,
			configJSON: newConfigJSON(detail.Group),
			Override:   newOverrideJSON(detail.Override),
			Status:     detail.Status,
		},
		Resources: make([]showResource, 0, len(detail.Resources)),
	}
	if detail.Err != nil {
		out.Group.Error = detail.Err.Error()
	}
	for _, r := range detail.Resources {
		res := showResource{
			Type: r.Resource.Type, Ref: r.Resource.Ref, ARN: r.Resource.ARN, Config: resourceConfig(r.Resource), Live: r.Live, Status: r.Status,
		}
		if r.LiveErr != nil {
			res.LiveErr = r.LiveErr.Error()
		}
		out.Resources = append(out.Resources, res)
	}
	if detail.DiscoverErr != nil {
		out.DiscoverErr = detail.DiscoverErr.Error()
	}
	return writeJSON(w, out)
}

// グループを変更する各コマンドの出力であり、実行したコマンド、対象グループ、そのコマンドが確定した設定を含む
// 含めるのはこれらのフィールドに限る
// 変更操作は、グループ全体の再読み取りではなく、自身が書き込んだ内容を報告する
// Created が現れるのは set-selector に限り、かつグループが存在しなかった場合に限る
type mutationResult struct {
	Command string `json:"command"`
	Group   string `json:"group"`
	configJSON
	Override *overrideJSON `json:"override,omitempty"`
	Created  bool          `json:"created,omitempty"`
}

func cmdSetSelector(ctx context.Context, s store, args []string, out io.Writer) error {
	fs := newFlagSet("set-selector")
	var group, tagKey, tagValue, types string
	fs.StringVar(&group, "group", "", "group name")
	fs.StringVar(&tagKey, "tag-key", "", "AWS resource tag key to match")
	fs.StringVar(&tagValue, "tag-value", "", "AWS resource tag value to match")
	fs.StringVar(&types, "types", "", "comma-separated resource types: "+strings.Join(model.TypeNames(model.KnownTypes), ", "))
	if err := fs.Parse(args); err != nil {
		return err
	}
	if group == "" {
		return fmt.Errorf("--group is required")
	}
	var typeNames []string
	for t := range strings.SplitSeq(types, ",") {
		if t = strings.TrimSpace(t); t != "" {
			typeNames = append(typeNames, t)
		}
	}
	typeList := model.ResourceTypes(typeNames)
	sel := model.Selector{TagKey: tagKey, TagValue: tagValue, Types: typeList}
	created, err := groups.SetSelector(ctx, s, group, sel)
	if err != nil {
		return err
	}
	res := mutationResult{Command: "set-selector", Group: group, Created: created}
	res.Selector = &selectorJSON{TagKey: sel.TagKey, TagValue: sel.TagValue, Types: typeList}
	if created {
		res.Mode = model.ModeDisabled
	}
	return writeJSON(out, res)
}

func cmdRemove(ctx context.Context, s store, args []string, out io.Writer) error {
	fs := newFlagSet("remove")
	var group string
	fs.StringVar(&group, "group", "", "group name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if group == "" {
		return fmt.Errorf("--group is required")
	}
	if err := groups.RemoveGroup(ctx, s, group); err != nil {
		return err
	}
	return writeJSON(out, mutationResult{Command: "remove", Group: group})
}

func cmdPin(ctx context.Context, s store, args []string, out io.Writer) error {
	fs := newFlagSet("pin")
	var group string
	fs.StringVar(&group, "group", "", "group name")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if group == "" {
		return fmt.Errorf("--group is required")
	}
	if len(pos) != 1 {
		return fmt.Errorf("pin requires exactly one of running|stopped, got %q", pos)
	}
	desired, err := model.ParseDesired(pos[0])
	if err != nil {
		return err
	}
	if err := groups.Pin(ctx, s, group, desired); err != nil {
		return err
	}
	res := mutationResult{Command: "pin", Group: group}
	res.Mode, res.Desired = model.ModePinned, desired
	return writeJSON(out, res)
}

func cmdUnpin(ctx context.Context, s store, args []string, out io.Writer) error {
	fs := newFlagSet("unpin")
	var group string
	fs.StringVar(&group, "group", "", "group name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if group == "" {
		return fmt.Errorf("--group is required")
	}
	item, err := groups.Unpin(ctx, s, group)
	if err != nil {
		return err
	}
	return writeJSON(out, mutationResult{Command: "unpin", Group: group, configJSON: newConfigJSON(item)})
}

func cmdSchedule(ctx context.Context, s store, args []string, out io.Writer) error {
	fs := newFlagSet("schedule")
	var group string
	fs.StringVar(&group, "group", "", "group name")
	startCron := fs.String("start", "", "cron for starting (5-field)")
	stopCron := fs.String("stop", "", "cron for stopping (5-field)")
	timezone := fs.String("timezone", "", "IANA timezone for the crons")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if group == "" {
		return fmt.Errorf("--group is required")
	}

	item, err := groups.Schedule(ctx, s, group, model.ScheduleSpec{
		StartCron: *startCron, StopCron: *stopCron, Timezone: *timezone,
	})
	if err != nil {
		return err
	}
	return writeJSON(out, mutationResult{Command: "schedule", Group: group, configJSON: newConfigJSON(item)})
}

func cmdDisable(ctx context.Context, s store, args []string, out io.Writer) error {
	fs := newFlagSet("disable")
	var group string
	fs.StringVar(&group, "group", "", "group name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if group == "" {
		return fmt.Errorf("--group is required")
	}
	if err := groups.Disable(ctx, s, group); err != nil {
		return err
	}
	res := mutationResult{Command: "disable", Group: group}
	res.Mode = model.ModeDisabled
	return writeJSON(out, res)
}

func cmdOverride(ctx context.Context, s store, args []string, out io.Writer) error {
	fs := newFlagSet("override")
	var group string
	fs.StringVar(&group, "group", "", "group name")
	duration := fs.Duration("for", 0, "how long the override lasts (e.g. 2h, 90m)")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if group == "" {
		return fmt.Errorf("--group is required")
	}
	if len(pos) != 1 {
		return fmt.Errorf("override requires exactly one of running|stopped, got %q", pos)
	}
	if *duration <= 0 {
		return fmt.Errorf("override requires -for DURATION")
	}
	desired, err := model.ParseDesired(pos[0])
	if err != nil {
		return err
	}
	expiresAt, err := groups.SetOverride(ctx, s, group, desired, *duration, time.Now())
	if err != nil {
		return err
	}
	return writeJSON(out, mutationResult{
		Command: "override", Group: group,
		Override: &overrideJSON{Desired: desired, ExpiresAt: expiresAt.UTC().Format(time.RFC3339)},
	})
}

// doctor の出力
// findings は空の場合も null ではなく [] を出力する (他のコマンドの groups / resources と同じ規約である)
// pruned は --prune を指定しない場合も 0 として出力する。削除の有無を、フラグの有無ではなく出力から判定できるようにするためである
type doctorOutput struct {
	Command  string              `json:"command"`
	Findings []doctor.Finding    `json:"findings"`
	Blocked  []string            `json:"blocked,omitempty"`
	Pruned   int                 `json:"pruned"`
	Counts   map[doctor.Kind]int `json:"counts,omitempty"`
}

func cmdDoctor(ctx context.Context, s store, d port.Discoverer, args []string, out io.Writer) error {
	fs := newFlagSet("doctor")
	prune := fs.Bool("prune", false, "delete the unambiguously orphaned records that were found")
	stuckAfter := fs.Duration("stuck-after", doctor.DefaultStuckAfter, "report resources transitioning longer than this")
	if err := fs.Parse(args); err != nil {
		return err
	}
	report, err := doctor.Run(ctx, s, d, time.Now(), doctor.Options{Prune: *prune, StuckAfter: *stuckAfter})
	if err != nil {
		return err
	}
	counts := map[doctor.Kind]int{}
	for _, f := range report.Findings {
		counts[f.Kind]++
	}
	if report.Findings == nil {
		report.Findings = []doctor.Finding{}
	}
	return writeJSON(out, doctorOutput{
		Command: "doctor", Findings: report.Findings, Blocked: report.Blocked, Pruned: report.Pruned, Counts: counts,
	})
}

func cmdClearOverride(ctx context.Context, s store, args []string, out io.Writer) error {
	fs := newFlagSet("clear-override")
	var group string
	fs.StringVar(&group, "group", "", "group name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if group == "" {
		return fmt.Errorf("--group is required")
	}
	if err := groups.ClearOverride(ctx, s, group); err != nil {
		return err
	}
	return writeJSON(out, mutationResult{Command: "clear-override", Group: group})
}
