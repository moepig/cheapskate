// Package webconsole is the opt-in browser frontend for the configuration operations in internal/ops. It renders server-side HTML (no JavaScript) and, like cheapskate-cli, only touches DynamoDB items. Access control (IP allowlist) lives in the API Gateway resource policy, not here.
package webconsole

import (
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"cheapskate/internal/model"
	"cheapskate/internal/ops"
	"cheapskate/internal/store"
)

//go:embed templates/*.gohtml
var templateFS embed.FS

// Server serves the web console. Base is the URL path prefix as seen by the browser (the API Gateway stage, e.g. "/console"); empty when serving locally.
type Server struct {
	store  *store.Store
	base   string
	loc    *time.Location
	index  *template.Template
	detail *template.Template
	now    func() time.Time
}

func New(s *store.Store, base string, loc *time.Location) *Server {
	funcs := template.FuncMap{
		"cfgDesc": describeConfig,
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
			return st.LastAction + " at " + st.LastActionAt
		},
	}
	parse := func(page string) *template.Template {
		return template.Must(template.New("base.gohtml").Funcs(funcs).ParseFS(templateFS, "templates/base.gohtml", "templates/"+page))
	}
	return &Server{
		store:  s,
		base:   strings.TrimSuffix(base, "/"),
		loc:    loc,
		index:  parse("index.gohtml"),
		detail: parse("detail.gohtml"),
		now:    time.Now,
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("GET /resource", s.handleResource)
	mux.HandleFunc("POST /op", s.handleOp)
	return securityHeaders(mux)
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

// view is the data passed to every template.
type view struct {
	Base string
	Msg  string
	Err  string
	Row  ops.Row
	Rows []ops.Row
}

func (s *Server) view(r *http.Request) view {
	return view{
		Base: s.base,
		Msg:  r.URL.Query().Get("msg"),
		Err:  r.URL.Query().Get("err"),
	}
}

func describeConfig(item model.ConfigItem) string {
	switch item.Mode {
	case model.ModePinned:
		return item.Desired
	case model.ModeSchedule:
		parts := []string{}
		if item.StartCron != "" {
			parts = append(parts, "start["+item.StartCron+"]")
		}
		if item.StopCron != "" {
			parts = append(parts, "stop["+item.StopCron+"]")
		}
		if item.Timezone != "" {
			parts = append(parts, item.Timezone)
		}
		return strings.Join(parts, " ")
	default:
		return "-"
	}
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	rows, err := ops.List(r.Context(), s.store, s.now())
	if err != nil {
		http.Error(w, "list resources: "+err.Error(), http.StatusInternalServerError)
		return
	}
	v := s.view(r)
	v.Rows = rows
	s.render(w, s.index, v)
}

func (s *Server) handleResource(w http.ResponseWriter, r *http.Request) {
	resourceID := r.URL.Query().Get("id")
	if _, err := model.ResourceIDType(resourceID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	row, err := ops.Get(r.Context(), s.store, resourceID, s.now())
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	v := s.view(r)
	v.Row = row
	s.render(w, s.detail, v)
}

func (s *Server) render(w http.ResponseWriter, t *template.Template, v view) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.Execute(w, v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleOp(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "cross-origin form submission rejected", http.StatusForbidden)
		return
	}
	action := r.PostFormValue("action")
	resourceID := r.PostFormValue("id")
	if _, err := model.ResourceIDType(resourceID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	var msg string
	var err error
	switch action {
	case "pin":
		desired := r.PostFormValue("desired")
		err = ops.Pin(ctx, s.store, resourceID, desired)
		msg = "pinned to " + desired
	case "schedule":
		spec := ops.ScheduleSpec{
			StartCron: strings.TrimSpace(r.PostFormValue("start")),
			StopCron:  strings.TrimSpace(r.PostFormValue("stop")),
			Timezone:  strings.TrimSpace(r.PostFormValue("timezone")),
		}
		if raw := strings.TrimSpace(r.PostFormValue("restore_count")); raw != "" {
			if spec.RestoreCount, err = strconv.Atoi(raw); err != nil {
				err = fmt.Errorf("restore count must be a number, got %q", raw)
				break
			}
		}
		_, err = ops.Schedule(ctx, s.store, resourceID, spec)
		msg = "schedule saved"
	case "disable":
		err = ops.Disable(ctx, s.store, resourceID)
		msg = "disabled"
	case "override":
		desired := r.PostFormValue("desired")
		var d time.Duration
		if d, err = time.ParseDuration(r.PostFormValue("for")); err != nil {
			err = fmt.Errorf("invalid duration %q", r.PostFormValue("for"))
			break
		}
		var until time.Time
		if until, err = ops.SetOverride(ctx, s.store, resourceID, desired, d, s.now()); err == nil {
			msg = fmt.Sprintf("override %s until %s", desired, until.In(s.loc).Format("2006-01-02 15:04 MST"))
		}
	case "clear-override":
		err = ops.ClearOverride(ctx, s.store, resourceID)
		msg = "override cleared"
	case "remove":
		err = ops.Remove(ctx, s.store, resourceID)
		msg = "removed " + resourceID
	default:
		http.Error(w, fmt.Sprintf("unknown action %q", action), http.StatusBadRequest)
		return
	}

	target := s.base + "/resource?id=" + url.QueryEscape(resourceID)
	if action == "remove" && err == nil {
		target = s.base + "/"
	}
	sep := "&"
	if !strings.Contains(target, "?") {
		sep = "?"
	}
	if err != nil {
		target += sep + "err=" + url.QueryEscape(err.Error())
	} else {
		target += sep + "msg=" + url.QueryEscape(msg)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// sameOrigin rejects cross-site form posts. There is no session to protect (access control is the IP allowlist), but this keeps external pages from driving operations via the operator's browser.
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
