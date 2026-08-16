// internal/app/groups の設定操作に対する、任意導入のブラウザフロントエンド
// HTML はサーバ側で描画し、JavaScript を用いない
// cheapskate-cli と同じく、操作対象は DynamoDB のアイテムと読み取り専用の tag:GetResources API に限る
// 一致したリソースの現在状態を表示する場合は、種別ごとの読み取り専用 Describe API (port.Describer) も用いる
// Stop/Start は呼ばない
// アクセス制御である IP 許可リストは、本パッケージではなく API Gateway のリソースポリシーが担う
package webconsole

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"cheapskate/internal/app/doctor"
	"cheapskate/internal/app/groups"
	"cheapskate/internal/app/port"
	"cheapskate/internal/core/model"
)

//go:embed templates/*.gohtml
var templateFS embed.FS

// web console が state テーブルに求める範囲
// 設定操作 (groups.Store) と診断 (doctor.Store) が必要とする範囲の和であり、それ以外を含まない
// UpdateStatus を含まないため、コンソールから reconciler の監査証跡を書き換える経路は存在しない
type Store interface {
	groups.Store
	doctor.Store
}

// web console を提供する
// Base はブラウザから見た URL のパス接頭辞であり、API Gateway のステージに対応する
// ローカル実行では空となる
type Server struct {
	store      Store
	discoverer port.Discoverer
	describers map[model.ResourceType]port.Describer
	base       string
	loc        *time.Location
	log        *slog.Logger
	types      []model.ResourceType
	index      *template.Template
	group      *template.Template
	doctor     *template.Template
	stuckAfter time.Duration
	now        func() time.Time
}

// log が nil の場合は何も出力しない
// ログを渡さない場合の扱いを、Server 側の nil 検査ではなく生成時に 1 回で解決する
func New(s Store, d port.Discoverer, describers map[model.ResourceType]port.Describer, base string, loc *time.Location, log *slog.Logger) *Server {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	funcs := template.FuncMap{
		"groupDesc": describeGroup,
		"selDesc":   describeSelector,
		"cfgDesc":   describeResourceConfig,
		"liveDesc":  describeLive,
		"ovDesc": func(o *model.Override) string {
			if o == nil {
				return ""
			}
			return fmt.Sprintf("%s until %s", o.Desired, time.Unix(o.ExpiresAt, 0).In(loc).Format("2006-01-02 15:04 MST"))
		},
		"actDesc": func(st model.Status) string {
			if st.LastAction == "" {
				return ""
			}
			return string(st.LastAction) + " at " + st.LastActionAt
		},
		"contains": slices.Contains[[]model.ResourceType],
	}
	parse := func(page string) *template.Template {
		return template.Must(template.New("base.gohtml").Funcs(funcs).ParseFS(templateFS, "templates/base.gohtml", "templates/"+page))
	}
	return &Server{
		store:      s,
		discoverer: d,
		describers: describers,
		base:       strings.TrimSuffix(base, "/"),
		loc:        loc,
		log:        log,
		types:      model.KnownTypes,
		index:      parse("index.gohtml"),
		group:      parse("group.gohtml"),
		doctor:     parse("doctor.gohtml"),
		stuckAfter: doctor.DefaultStuckAfter,
		now:        time.Now,
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("GET /group", s.handleGroup)
	mux.HandleFunc("POST /op", s.handleOp)
	mux.HandleFunc("GET /doctor", s.handleDoctor)
	mux.HandleFunc("POST /doctor", s.handleDoctorPrune)
	return securityHeaders(s.logRequests(mux))
}

// リクエストの開始と完了を 1 行ずつ記録する
//
// 開始と完了を別の行とするのは、完了しなかったリクエストを検出するためである
// Lambda のタイムアウトと実行時 panic により終了した場合、残るのは request-start のみとなる
// 完了行のみを出力した場合、リクエストの未到達と処理中の終了を区別できない
//
// client はリクエスト元の IP である (clientIP を参照)
// このコンソールは認証を持たず、アクセス制御は IP 許可リストのみであるため、操作の主体を特定する情報はこれに限る
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attrs := []any{"method", r.Method, "path", r.URL.Path, "query", r.URL.RawQuery, "client", clientIP(r)}
		s.log.Info("request-start", attrs...)

		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		s.log.Info("request-end", append(attrs, "status", rec.status, "duration_ms", time.Since(start).Milliseconds())...)
	})
}

// 書き込んだステータスコードを保持する ResponseWriter
// WriteHeader を呼ばずに書き込みを開始した場合、net/http と同じく既定を 200 とする
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusRecorder) WriteHeader(status int) {
	if !w.wroteHeader {
		w.status, w.wroteHeader = status, true
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusRecorder) Write(b []byte) (int, error) {
	w.wroteHeader = true
	return w.ResponseWriter.Write(b)
}

// リクエスト元の IP を返す
//
// X-Forwarded-For は参照しない
// API Gateway は、クライアントが送信した X-Forwarded-For の末尾へ観測した送信元 IP を追記するため、先頭の値はクライアントが任意に設定できる
// 信頼できるのは末尾の値であるが、以下の理由により参照を要しない
//
// Lambda 上では、Lambda Web Adapter が呼び出しイベントの requestContext を x-amzn-request-context ヘッダへ JSON として設定する
// そこに含まれる sourceIp は API Gateway への TCP 接続元であり、リソースポリシーの aws:SourceIp が許可の判定に用いる値と同一である
// アクセス制御が IP 許可リストのみであるため、記録すべきなのは許可の判定に用いた IP であり、クライアントが申告した値ではない
// このヘッダはアダプタがクライアント由来の同名ヘッダを破棄して設定するため、詐称できない
// RemoteAddr はアダプタからのループバック接続となるため、Lambda 上では送信元を示さない
//
// ローカル実行ではこのヘッダが存在せず、TCP 接続元を用いる (ポート番号を除去する)
func clientIP(r *http.Request) string {
	if ip := sourceIP(r.Header.Get("X-Amzn-Request-Context")); ip != "" {
		return ip
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// アダプタが渡す requestContext の JSON から送信元 IP を取り出す
// 読めない場合、および値が存在しない場合は空を返し、呼び出し側は RemoteAddr を用いる
//
// 位置はイベント形式により異なる。REST API(v1、本番構成) は identity.sourceIp、HTTP API(v2) と Function URL は http.sourceIp である
// 前者のみを参照した場合、トリガの変更により client の値が変化するため、両方を参照する
func sourceIP(requestContext string) string {
	if requestContext == "" {
		return ""
	}
	var rc struct {
		Identity struct {
			SourceIP string `json:"sourceIp"`
		} `json:"identity"`
		HTTP struct {
			SourceIP string `json:"sourceIp"`
		} `json:"http"`
	}
	if err := json.Unmarshal([]byte(requestContext), &rc); err != nil {
		return ""
	}
	if rc.Identity.SourceIP != "" {
		return rc.Identity.SourceIP
	}
	return rc.HTTP.SourceIP
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

// すべてのテンプレートへ渡すデータ
type view struct {
	Base         string
	Msg          string
	Err          string
	Types        []model.ResourceType
	Rows         []groups.GroupRow
	Detail       groups.GroupDetail
	Doctor       doctorView
	TZName       string // Override の "Until" フィールドを解釈するタイムゾーン
	DefaultUntil string // Override の "Until" フィールドの datetime-local 既定値
}

// /doctor ページのデータ
// Prunable を Report と別に持つのは、テンプレート側での件数の集計を避けるためである
// 0 件の場合、prune のフォームを出力しない
type doctorView struct {
	Report     doctor.Report
	Prunable   int
	StuckAfter time.Duration
}

func newDoctorView(r doctor.Report, stuckAfter time.Duration) doctorView {
	v := doctorView{Report: r, StuckAfter: stuckAfter}
	for _, f := range r.Findings {
		if f.Prunable {
			v.Prunable++
		}
	}
	return v
}

// エラーを画面へ返すとともにログへ記録する
// 画面の内容はブラウザの終了により失われるため、画面のみへ出力した場合、事後の原因の特定が不可能となる
func (s *Server) fail(w http.ResponseWriter, r *http.Request, status int, msg string) {
	s.log.Error("request-failed",
		"method", r.Method, "path", r.URL.Path, "query", r.URL.RawQuery, "client", clientIP(r),
		"status", status, "error", msg)
	http.Error(w, msg, status)
}

func (s *Server) view(r *http.Request) view {
	return view{
		Base:   s.base,
		Msg:    r.URL.Query().Get("msg"),
		Err:    r.URL.Query().Get("err"),
		Types:  s.types,
		TZName: s.loc.String(),
	}
}

// グループの mode 固有の設定を "ラベル: 値" の行として、1 属性につき 1 行で描画する
// start/stop/timezone を単一の文字列へまとめることを避けるためである
func describeGroup(item model.GroupSpec) template.HTML {
	switch item.Mode {
	case model.ModePinned:
		return template.HTML(template.HTMLEscapeString(string(item.Desired)))
	case model.ModeSchedule:
		var lines []string
		if item.StartCron != "" {
			lines = append(lines, "start: "+template.HTMLEscapeString(item.StartCron))
		}
		if item.StopCron != "" {
			lines = append(lines, "stop: "+template.HTMLEscapeString(item.StopCron))
		}
		if item.Timezone != "" {
			lines = append(lines, "timezone: "+template.HTMLEscapeString(item.Timezone))
		}
		return template.HTML(strings.Join(lines, "<br>"))
	default:
		return "-"
	}
}

// グループのセレクタを "ラベル: 値" の行として、1 属性につき 1 行で描画する
// タグと types を単一の文字列へまとめることを避けるためである
func describeSelector(item model.GroupSpec) template.HTML {
	sel := item.Selector()
	if sel.Empty() {
		return "-"
	}
	lines := []string{
		"tag: " + template.HTMLEscapeString(sel.TagKey+"="+sel.TagValue),
		"types: " + template.HTMLEscapeString(strings.Join(model.TypeNames(sel.Types), ", ")),
	}
	return template.HTML(strings.Join(lines, "<br>"))
}

// リソース自身のタグが保持する種別固有の設定を描画する
// タグを列挙するのではなく、その種別が設定として意味を定義したタグのみを出力する
// 該当を持たない種別は "-" を表示する
//
// 設定として扱うタグを定義するのは種別の宣言 (model.TypeInfo.ConfigTags) であるため、この関数は種別ごとの分岐を持たない
// 別の種別へタグ由来の設定を追加する場合も、変更するのは宣言側であり、この関数は変更しない
func describeResourceConfig(r model.Resource) template.HTML {
	cfg := r.Config()
	if len(cfg) == 0 {
		return `<span class="muted">-</span>`
	}
	lines := make([]string, 0, len(cfg))
	for _, c := range cfg {
		lines = append(lines, template.HTMLEscapeString(c.Label+": "+c.Value))
	}
	return template.HTML(strings.Join(lines, "<br>"))
}

// 問い合わせたリソースの現在状態 (ResourceRow.Live と LiveErr) を描画する
// State に続けて Observation.Detail を表示する
// 種別に Describer が結線されていない場合は "-" を表示し、問い合わせが失敗した場合はそのエラーを表示する
func describeLive(row groups.ResourceRow) template.HTML {
	if row.LiveErr != nil {
		return template.HTML(`<span class="danger">` + template.HTMLEscapeString(row.LiveErr.Error()) + `</span>`)
	}
	if row.Live == nil {
		return `<span class="muted">-</span>`
	}
	text := string(row.Live.State)
	if row.Live.Detail != "" {
		text += " (" + row.Live.Detail + ")"
	}
	return template.HTML(template.HTMLEscapeString(text))
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	rows, err := groups.List(r.Context(), s.store, s.now())
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "list groups: "+err.Error())
		return
	}
	v := s.view(r)
	v.Rows = rows
	s.render(w, r, s.index, v)
}

// グループ 1 件の設定と、動的に探索したメンバーリソースを表示する
// Discover の失敗は、HTTP エラーではなくデータとしてページ内へ描画する
// グループの設定は探索に依存せず有効であるため、ページは 200 を返す
func (s *Server) handleGroup(w http.ResponseWriter, r *http.Request) {
	group := r.URL.Query().Get("name")
	if err := model.ValidGroupName(group); err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}
	detail, err := groups.GetDetail(r.Context(), s.store, s.discoverer, s.describers, group, s.now())
	if err != nil {
		s.fail(w, r, http.StatusNotFound, err.Error())
		return
	}
	v := s.view(r)
	v.Detail = detail
	v.DefaultUntil = s.now().Add(2 * time.Hour).In(s.loc).Format("2006-01-02T15:04")
	s.render(w, r, s.group, v)
}

// state テーブルの診断結果を表示する (読み取りのみ)
// Discover の失敗は handleGroup と同じく、データとしてページ内へ出力する
// 一部のグループが失敗した場合も、他のグループの診断結果は有効であるためである
func (s *Server) handleDoctor(w http.ResponseWriter, r *http.Request) {
	report, err := doctor.Run(r.Context(), s.store, s.discoverer, s.now(), doctor.Options{StuckAfter: s.stuckAfter})
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "doctor: "+err.Error())
		return
	}
	v := s.view(r)
	v.Doctor = newDoctorView(report, s.stuckAfter)
	s.render(w, r, s.doctor, v)
}

// 孤立レコードを削除する
// 表示済みの結果ではなく、実行の時点で診断を再実行した結果に対して削除する
// ページの表示から実行までの間に孤立しなくなったレコードを、過去の診断結果を根拠として削除しないためである
func (s *Server) handleDoctorPrune(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		s.fail(w, r, http.StatusForbidden, "cross-origin form submission rejected")
		return
	}
	report, err := doctor.Run(r.Context(), s.store, s.discoverer, s.now(), doctor.Options{Prune: true, StuckAfter: s.stuckAfter})
	target := s.base + "/doctor"
	if err != nil {
		s.log.Error("operation-failed", "action", "doctor-prune", "client", clientIP(r), "error", err.Error())
		http.Redirect(w, r, target+"?err="+url.QueryEscape("doctor: "+err.Error()), http.StatusSeeOther)
		return
	}
	// 個々の削除の失敗は doctor.Run のエラーとならず finding に残るため、ここで集計して報告する
	var failed int
	for _, f := range report.Findings {
		if f.PruneErr != "" {
			failed++
		}
	}
	msg := fmt.Sprintf("pruned %d record(s)", report.Pruned)
	if failed > 0 {
		s.log.Error("operation-failed", "action", "doctor-prune", "client", clientIP(r), "pruned", report.Pruned,
			"error", fmt.Sprintf("%d record(s) failed to delete", failed))
		http.Redirect(w, r, target+"?err="+url.QueryEscape(fmt.Sprintf("%s; %d failed to delete — run it again", msg, failed)), http.StatusSeeOther)
		return
	}
	s.log.Info("operation", "action", "doctor-prune", "client", clientIP(r), "result", msg)
	http.Redirect(w, r, target+"?msg="+url.QueryEscape(msg), http.StatusSeeOther)
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, t *template.Template, v view) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.Execute(w, v); err != nil {
		s.fail(w, r, http.StatusInternalServerError, err.Error())
	}
}

func (s *Server) handleOp(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		s.fail(w, r, http.StatusForbidden, "cross-origin form submission rejected")
		return
	}
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}
	action := r.PostFormValue("action")
	group := r.PostFormValue("group")
	if err := model.ValidGroupName(group); err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}

	ctx := r.Context()
	var msg string
	var err error
	removedGroup := false
	switch action {
	case "set-selector":
		sel := model.Selector{
			TagKey:   strings.TrimSpace(r.PostFormValue("tag_key")),
			TagValue: strings.TrimSpace(r.PostFormValue("tag_value")),
			Types:    model.ResourceTypes(r.PostForm["types"]), // 複数値のチェックボックスであり、PostFormValue は先頭しか返さない
		}
		var created bool
		created, err = groups.SetSelector(ctx, s.store, group, sel)
		if err == nil {
			msg = "selector saved"
			if created {
				msg += " (created group)"
			}
		}
	case "remove-group":
		err = groups.RemoveGroup(ctx, s.store, group)
		msg = "removed group " + group
		removedGroup = err == nil
	case "pin":
		var desired model.DesiredState
		if desired, err = model.ParseDesired(r.PostFormValue("desired")); err != nil {
			break
		}
		err = groups.Pin(ctx, s.store, group, desired)
		msg = "pinned to " + string(desired)
	case "unpin":
		var item model.GroupSpec
		if item, err = groups.Unpin(ctx, s.store, group); err == nil {
			if item.Mode == model.ModeSchedule {
				msg = "pin released; schedule resumed"
			} else {
				msg = "pin released; group disabled (no schedule configured)"
			}
		}
	case "schedule":
		spec := model.ScheduleSpec{
			StartCron: strings.TrimSpace(r.PostFormValue("start")),
			StopCron:  strings.TrimSpace(r.PostFormValue("stop")),
			Timezone:  strings.TrimSpace(r.PostFormValue("timezone")),
		}
		_, err = groups.Schedule(ctx, s.store, group, spec)
		msg = "schedule saved"
	case "disable":
		err = groups.Disable(ctx, s.store, group)
		msg = "disabled"
	case "override":
		var desired model.DesiredState
		if desired, err = model.ParseDesired(r.PostFormValue("desired")); err != nil {
			break
		}
		var until time.Time
		if until, err = time.ParseInLocation("2006-01-02T15:04", r.PostFormValue("until"), s.loc); err != nil {
			err = fmt.Errorf("invalid date/time %q", r.PostFormValue("until"))
			break
		}
		var expiresAt time.Time
		if expiresAt, err = groups.SetOverride(ctx, s.store, group, desired, until.Sub(s.now()), s.now()); err == nil {
			msg = fmt.Sprintf("override %s until %s", desired, expiresAt.In(s.loc).Format("2006-01-02 15:04 MST"))
		}
	case "clear-override":
		err = groups.ClearOverride(ctx, s.store, group)
		msg = "override cleared"
	default:
		s.fail(w, r, http.StatusBadRequest, fmt.Sprintf("unknown action %q", action))
		return
	}

	target := s.base + "/group?name=" + url.QueryEscape(group)
	if removedGroup {
		target = s.base + "/"
	}
	sep := "&"
	if !strings.Contains(target, "?") {
		sep = "?"
	}
	// 操作の失敗はリダイレクト先の画面にのみ現れるため、ここでログへ記録する
	// 成功も 1 行記録する。このコンソールは設定を書き換える経路であり、変更の主体と内容を追跡できなければならないためである
	if err != nil {
		s.log.Error("operation-failed", "action", action, "group", group, "client", clientIP(r), "error", err.Error())
		target += sep + "err=" + url.QueryEscape(err.Error())
	} else {
		s.log.Info("operation", "action", action, "group", group, "client", clientIP(r), "result", msg)
		target += sep + "msg=" + url.QueryEscape(msg)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// クロスサイトのフォーム送信を拒否する
// 保護対象のセッションは存在しない (アクセス制御は IP 許可リストである)
// 外部のページが、許可された IP のブラウザを経由して操作を実行することを防ぐためである
func sameOrigin(r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}
