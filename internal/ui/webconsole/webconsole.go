// internal/app/groups の設定操作に対する、任意導入のブラウザフロントエンド
// サーバ側で HTML を描画し、JavaScript は使わない
// cheapskate-cli と同じく、触れるのは DynamoDB のアイテムと読み取り専用の tag:GetResources API だけである
// マッチしたリソースのライブな現在状態を表示するときは、種別ごとの読み取り専用 Describe API（port.Describer）も使う
// Stop/Start を呼ぶことは決してない
// アクセス制御（IP 許可リスト）はここではなく API Gateway のリソースポリシーにある
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
// 設定操作（groups.Store）と診断（doctor.Store）の必要分を合わせたもので、それ以上は含まない
// とくに UpdateStatus がないので、コンソールから reconciler の監査証跡を書き換える経路は存在しない
type Store interface {
	groups.Store
	doctor.Store
}

// web console を提供する
// Base はブラウザから見た URL のパス接頭辞であり、API Gateway のステージ（"/console" など）にあたる
// ローカルで動かすときは空になる
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

// log が nil なら何も出力しない
// ログを渡さない選択を、Server 側の nil チェックではなく生成時に 1 回だけ解決しておく
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

// リクエストの開始と完了を 1 本ずつ残す
//
// 開始と完了を別の行にしているのは、完了しなかったリクエストを見つけられるようにするためである
// Lambda のタイムアウトや実行時パニックで落ちた場合、残るのは request-start だけになる
// 完了行しか出さないと、そもそも届いていなかったのか処理の途中で消えたのかが区別できない
//
// client はリクエスト元の IP である（clientIP を参照）
// このコンソールに認証はなく、アクセス制御は IP 許可リストだけなので、誰が操作したのかを後から辿れる手がかりはこれしかない
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

// 実際に書いたステータスコードを覚えておくだけの ResponseWriter
// WriteHeader を呼ばずに書き始めた場合、net/http と同じく 200 が既定になる
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
// X-Forwarded-For は見ない
// API Gateway はクライアントが送ってきた X-Forwarded-For の「末尾に」観測した送信元 IP を追記するので、先頭に入っているのはクライアントが自由に書ける値である
// 信用できるのは末尾側だが、そもそも読む必要がない
//
// Lambda 上では Lambda Web Adapter が元の呼び出しイベントの requestContext を x-amzn-request-context ヘッダに JSON で載せてくる
// そこにある sourceIp は API Gateway への TCP 接続元そのものであり、リソースポリシーの aws:SourceIp が許可リストの判定に使う値と同一である
// アクセス制御が IP 許可リストだけである以上、ログに残すべきなのは「実際に許可判定された IP」であって、クライアントの自己申告ではない
// このヘッダはアダプタが（クライアント由来の同名ヘッダを捨てて）差し替えるので詐称できない
// RemoteAddr はアダプタからのループバック接続になるため、Lambda 上では手がかりにならない
//
// ローカル実行時はこのヘッダが無く、素の TCP 接続元になる（こちらはポートが付くので落とす）
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
// 読めない・入っていないときは空を返し、呼び出し側を RemoteAddr へ落とす
//
// 場所はイベント形式によって違う: REST API(v1, 本番構成) は identity.sourceIp、HTTP API(v2) と Function URL は http.sourceIp である
// 前者しか見ないと、トリガを差し替えたときに黙って client が変わるので両方拾う
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
// Prunable を Report とは別に持たせているのは、テンプレート側で件数を数えさせないためである
// 0 件なら prune のフォーム自体を出さない
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

// エラーを画面に返すと同時にログにも残す
// 画面はブラウザを閉じれば消えるので、そこにしか出さなければ、あとから原因を追う手立てがなくなる
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

// グループの mode 固有の設定を "ラベル: 値" の行として、1 属性 1 行で描画する
// start/stop/timezone を 1 本の長い文字列へ詰め込むのを避けるためである
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

// グループのセレクタを "ラベル: 値" の行として、1 属性 1 行で描画する
// タグと types を 1 本の長い文字列へ詰め込むのを避けるためである
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

// リソース自身のタグが担う、種別固有の設定を描画する
// タグそのものを羅列するわけではなく、その種別が設定として意味を与えているタグだけを出す
// 現時点で該当があるのは ecs-service のスケーリング設定だけで、他の種別は "-" になる
//
// どのタグが設定なのかを知っているのは種別の宣言（model.TypeInfo.ConfigTags）なので、ここには種別ごとの分岐がない
// 別の種別にタグ由来の設定を加えるときも、この関数は変えずに宣言側へ足すことになる
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

// 都度問い合わせたリソースの現在状態（ResourceRow.Live と LiveErr）を描画する
// ECS サービスなら "running (desiredCount=2)" のようになる
// compute.EcsServiceTarget.Describe が desired count をそのまま Observation.Detail へ入れるためである
// 種別に Describer が結線されていなければ "-" を表示し、問い合わせが失敗していればそのエラーをその場に表示する
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
// Discover の失敗（tag:GetResources 権限の不足など）は HTTP エラーではなくデータとしてその場に描画する
// グループ自身の設定は依然として妥当で役に立つので、ページは 200 を返す
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

// state テーブルの診断結果を表示する（読み取りのみ）
// Discover の失敗は handleGroup と同じくデータとしてページ内に出す
// 失敗したグループがあっても、他のグループについて分かったことは依然として役に立つためである
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
// 表示済みの結果に対してではなく、押された時点で診断をやり直してから削除する
// ページを開いてからボタンを押すまでの間に孤立しなくなったレコード（タグを付け直した、グループを作り直した）を、古い画面を根拠に消してしまわないためである
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
	// 個々の削除失敗は doctor.Run のエラーにならず finding に残るので、ここで数えて伝える
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
	// 操作の失敗はリダイレクト先の画面にしか出ないので、ここで必ずログにも残す
	// 成功も 1 行残す（このコンソールは設定を書き換える経路であり、誰が何を変えたのかを追えなければならない）
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
// 守るべきセッションはない（アクセス制御は IP 許可リストである）
// それでも外部のページがオペレータのブラウザ経由で操作を走らせるのを防げる
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
