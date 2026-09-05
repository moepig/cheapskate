package webconsole

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"cheapskate/internal/app/groups"
	"cheapskate/internal/app/port"
	"cheapskate/internal/core/model"
	"cheapskate/internal/state"
)

//go:embed templates/*.gohtml
var templateFS embed.FS

type Server struct {
	service  *groups.Service
	base     string
	location *time.Location
	log      *slog.Logger
	index    *template.Template
	group    *template.Template
	now      func() time.Time
}

func New(store groups.Store, discoverer port.Discoverer, describers map[model.ResourceType]port.Describer, base string, location *time.Location, log *slog.Logger) *Server {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	functions := template.FuncMap{
		"groupDesc": describeGroup,
		"cfgDesc":   describeResourceConfig,
		"liveDesc":  describeLive,
		"ovDesc": func(group model.GroupSpec) string {
			if group.Override == "" {
				return ""
			}
			text := string(group.Override)
			if group.OverrideExpiresAt != 0 {
				text += " until " + time.Unix(group.OverrideExpiresAt, 0).In(location).Format("2006-01-02 15:04 MST")
			}
			return text
		},
	}
	parse := func(page string) *template.Template {
		return template.Must(template.New("base.gohtml").Funcs(functions).ParseFS(templateFS, "templates/base.gohtml", "templates/"+page))
	}
	return &Server{
		service: groups.New(store, discoverer, describers, location),
		base:    strings.TrimSuffix(base, "/"), location: location, log: log,
		index: parse("index.gohtml"), group: parse("group.gohtml"), now: time.Now,
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("GET /group", s.handleGroup)
	mux.HandleFunc("POST /op", s.handleOp)
	return securityHeaders(s.logRequests(mux))
}

type view struct {
	Base         string
	Msg          string
	Err          string
	Rows         []groups.GroupRow
	Detail       groups.GroupDetail
	TZName       string
	DefaultUntil string
}

func (s *Server) newView(request *http.Request) view {
	return view{Base: s.base, Msg: request.URL.Query().Get("msg"), Err: request.URL.Query().Get("err"), TZName: s.location.String()}
}

func (s *Server) handleIndex(response http.ResponseWriter, request *http.Request) {
	rows, err := s.service.List(request.Context(), s.now())
	if err != nil {
		s.fail(response, request, http.StatusInternalServerError, "list groups: "+err.Error())
		return
	}
	page := s.newView(request)
	page.Rows = rows
	s.render(response, request, s.index, page)
}

func (s *Server) handleGroup(response http.ResponseWriter, request *http.Request) {
	name := request.URL.Query().Get("name")
	detail, err := s.service.Show(request.Context(), name, s.now())
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, state.ErrGroupNotFound):
			status = http.StatusNotFound
		case errors.Is(err, groups.ErrInvalidConfig), model.ValidGroupName(name) != nil:
			status = http.StatusBadRequest
		}
		s.fail(response, request, status, err.Error())
		return
	}
	page := s.newView(request)
	page.Detail = detail
	page.DefaultUntil = s.now().Add(2 * time.Hour).In(s.location).Format("2006-01-02T15:04")
	s.render(response, request, s.group, page)
}

func (s *Server) handleOp(response http.ResponseWriter, request *http.Request) {
	if !sameOrigin(request) {
		s.fail(response, request, http.StatusForbidden, "cross-origin form submission rejected")
		return
	}
	if err := request.ParseForm(); err != nil {
		s.fail(response, request, http.StatusBadRequest, err.Error())
		return
	}
	action := request.PostFormValue("action")
	name := request.PostFormValue("group")
	if err := model.ValidGroupName(name); err != nil {
		s.fail(response, request, http.StatusBadRequest, err.Error())
		return
	}
	now := s.now()
	var message string
	var err error
	removed := false
	switch action {
	case "schedule":
		_, err = s.service.Schedule(request.Context(), name, model.ScheduleSpec{
			StartCron: strings.TrimSpace(request.PostFormValue("start")),
			StopCron:  strings.TrimSpace(request.PostFormValue("stop")),
		}, now)
		message = "schedule saved"
	case "override":
		override, parseErr := model.ParseOverride(request.PostFormValue("override"))
		if parseErr != nil {
			err = parseErr
			break
		}
		var duration time.Duration
		if rawUntil := request.PostFormValue("until"); rawUntil != "" {
			until, parseErr := time.ParseInLocation("2006-01-02T15:04", rawUntil, s.location)
			if parseErr != nil || !until.After(now) {
				err = fmt.Errorf("invalid future date/time %q", rawUntil)
				break
			}
			duration = until.Sub(now)
		}
		_, err = s.service.Override(request.Context(), name, override, duration, now)
		message = "override saved"
	case "clear-override":
		_, err = s.service.ClearOverride(request.Context(), name, now)
		message = "override cleared"
	case "remove":
		err = s.service.Remove(request.Context(), name)
		message = "group removed"
		removed = err == nil
	default:
		s.fail(response, request, http.StatusBadRequest, fmt.Sprintf("unknown action %q", action))
		return
	}
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, state.ErrConflict) {
			status = http.StatusConflict
		} else if errors.Is(err, state.ErrGroupNotFound) {
			status = http.StatusNotFound
		}
		s.fail(response, request, status, err.Error())
		return
	}
	s.log.Info("operation", "action", action, "group", name, "client", clientIP(request))
	target := s.base + "/group?name=" + url.QueryEscape(name) + "&msg=" + url.QueryEscape(message)
	if removed {
		target = s.base + "/?msg=" + url.QueryEscape(message)
	}
	http.Redirect(response, request, target, http.StatusSeeOther)
}

func (s *Server) render(response http.ResponseWriter, request *http.Request, page *template.Template, data view) {
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := page.Execute(response, data); err != nil {
		s.fail(response, request, http.StatusInternalServerError, err.Error())
	}
}

func describeGroup(group model.GroupSpec) template.HTML {
	if group.StartCron == "" {
		return `<span class="muted">-</span>`
	}
	return template.HTML("start: " + template.HTMLEscapeString(group.StartCron) + "<br>stop: " + template.HTMLEscapeString(group.StopCron))
}

func describeResourceConfig(resource model.Resource) template.HTML {
	config := resource.Config()
	if len(config) == 0 {
		return `<span class="muted">-</span>`
	}
	lines := make([]string, 0, len(config))
	for _, value := range config {
		lines = append(lines, template.HTMLEscapeString(value.Label+": "+value.Value))
	}
	return template.HTML(strings.Join(lines, "<br>"))
}

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

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		start := time.Now()
		recorder := &statusRecorder{ResponseWriter: response, status: http.StatusOK}
		next.ServeHTTP(recorder, request)
		s.log.Info("request", "method", request.Method, "path", request.URL.Path, "client", clientIP(request), "status", recorder.status, "duration_ms", time.Since(start).Milliseconds())
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (writer *statusRecorder) WriteHeader(status int) {
	if !writer.wroteHeader {
		writer.status, writer.wroteHeader = status, true
	}
	writer.ResponseWriter.WriteHeader(status)
}

func (writer *statusRecorder) Write(body []byte) (int, error) {
	writer.wroteHeader = true
	return writer.ResponseWriter.Write(body)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		headers := response.Header()
		headers.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
		headers.Set("X-Content-Type-Options", "nosniff")
		headers.Set("Referrer-Policy", "same-origin")
		headers.Set("X-Frame-Options", "DENY")
		next.ServeHTTP(response, request)
	})
}

func (s *Server) fail(response http.ResponseWriter, request *http.Request, status int, message string) {
	s.log.Error("request-failed", "method", request.Method, "path", request.URL.Path, "client", clientIP(request), "status", status, "error", message)
	http.Error(response, message, status)
}

func clientIP(request *http.Request) string {
	if ip := sourceIP(request.Header.Get("X-Amzn-Request-Context")); ip != "" {
		return ip
	}
	if host, _, err := net.SplitHostPort(request.RemoteAddr); err == nil {
		return host
	}
	return request.RemoteAddr
}

func sourceIP(requestContext string) string {
	var contextValue struct {
		Identity struct {
			SourceIP string `json:"sourceIp"`
		} `json:"identity"`
		HTTP struct {
			SourceIP string `json:"sourceIp"`
		} `json:"http"`
	}
	if json.Unmarshal([]byte(requestContext), &contextValue) != nil {
		return ""
	}
	if contextValue.Identity.SourceIP != "" {
		return contextValue.Identity.SourceIP
	}
	return contextValue.HTTP.SourceIP
}

func sameOrigin(request *http.Request) bool {
	if site := request.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return false
	}
	origin := request.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	return err == nil && strings.EqualFold(parsed.Host, request.Host)
}
