package admin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/xiongwei-git/alertbridge/internal/passwordhash"
	"github.com/xiongwei-git/alertbridge/internal/runtimecfg"
	"github.com/xiongwei-git/alertbridge/internal/store"
)

//go:embed templates/*.html static/*
var assets embed.FS

const sessionCookie = "alertbridge_admin"

type Config struct {
	Database        *store.Store
	Gateway         *runtimecfg.Manager
	Username        string
	PasswordHash    string
	SessionLifetime time.Duration
	SecureCookie    bool
	Now             func() time.Time
	DisplayLocation *time.Location
	Logger          *slog.Logger
}

type Handler struct {
	cfg          Config
	templates    map[string]*template.Template
	css          []byte
	mark         []byte
	passwordGate chan struct{}
}

type pageData struct {
	Title                string
	Active               string
	CSRF                 string
	Notice               string
	Error                string
	Stats                store.DashboardStats
	Clients              []runtimecfg.ClientView
	Channels             []runtimecfg.ChannelView
	Routes               []runtimecfg.RouteView
	Silences             []store.SilenceRecord
	Deliveries           []store.DeliveryView
	RouteOptions         []string
	Filter               string
	Page                 int
	TotalPages           int
	Secret               string
	SecretOwner          string
	NowLocal             string
	GuideClientID        string
	GuideClientEnabled   bool
	GuideBaseURL         string
	GuideRoute           string
	GuideSeverity        string
	GuideRouteConfigured bool
	GuideRoutes          []string
}

type sessionContextKey struct{}

func New(cfg Config) (*Handler, error) {
	if cfg.Database == nil || cfg.Gateway == nil {
		return nil, errors.New("admin database and gateway are required")
	}
	if cfg.Username == "" || passwordhash.Validate(cfg.PasswordHash) != nil {
		return nil, errors.New("valid admin credentials are required")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.DisplayLocation == nil {
		cfg.DisplayLocation = time.UTC
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.SessionLifetime <= 0 {
		cfg.SessionLifetime = 12 * time.Hour
	}
	funcs := template.FuncMap{
		"join": strings.Join,
		"has": func(values []string, candidate string) bool {
			for _, value := range values {
				if value == candidate {
					return true
				}
			}
			return false
		},
		"formatTime": func(value time.Time) string {
			if value.IsZero() {
				return "—"
			}
			return value.In(cfg.DisplayLocation).Format("2006-01-02 15:04:05")
		},
		"formatOptionalTime": func(value *time.Time) string {
			if value == nil {
				return "—"
			}
			return value.In(cfg.DisplayLocation).Format("2006-01-02 15:04:05")
		},
		"statusClass": func(value string) string {
			switch value {
			case "sent", "resolved":
				return "status-ok"
			case "dead", "critical":
				return "status-bad"
			case "retrying", "warning":
				return "status-warn"
			default:
				return "status-neutral"
			}
		},
		"sub": func(a, b int) int { return a - b }, "add": func(a, b int) int { return a + b },
	}
	pages := []string{"login.html", "dashboard.html", "clients.html", "channels.html", "routes.html", "silences.html", "deliveries.html", "guide.html", "secret.html"}
	templates := make(map[string]*template.Template, len(pages))
	for _, page := range pages {
		tmpl, err := template.New("layout.html").Funcs(funcs).ParseFS(assets, "templates/layout.html", "templates/"+page)
		if err != nil {
			return nil, fmt.Errorf("parse admin template %s: %w", page, err)
		}
		templates[page] = tmpl
	}
	css, err := assets.ReadFile("static/app.css")
	if err != nil {
		return nil, err
	}
	mark, err := assets.ReadFile("static/alertbridge-mark.svg")
	if err != nil {
		return nil, err
	}
	return &Handler{cfg: cfg, templates: templates, css: css, mark: mark, passwordGate: make(chan struct{}, 1)}, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.adminHeaders(w)
	if r.URL.Path == "/admin/assets/app.css" {
		h.serveAsset(w, r, "text/css; charset=utf-8", h.css)
		return
	}
	if r.URL.Path == "/admin/assets/alertbridge-mark.svg" {
		h.serveAsset(w, r, "image/svg+xml", h.mark)
		return
	}
	if r.URL.Path == "/admin" {
		http.Redirect(w, r, "/admin/", http.StatusSeeOther)
		return
	}
	if r.URL.Path == "/admin/login" {
		h.login(w, r)
		return
	}
	session, ok := h.authenticate(r)
	if !ok {
		http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
		return
	}
	r = r.WithContext(context.WithValue(r.Context(), sessionContextKey{}, session))
	if r.Method == http.MethodPost {
		if err := h.parseForm(w, r); err != nil {
			h.renderError(w, http.StatusBadRequest, "请求格式无效")
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Form.Get("csrf")), []byte(session.CSRFToken)) != 1 {
			h.renderError(w, http.StatusForbidden, "安全校验失败，请刷新页面后重试")
			return
		}
	}
	switch r.URL.Path {
	case "/admin/":
		h.dashboard(w, r, session)
	case "/admin/logout":
		h.logout(w, r)
	case "/admin/clients":
		h.clients(w, r, session)
	case "/admin/clients/create":
		h.createClient(w, r, session)
	case "/admin/clients/action":
		h.clientAction(w, r, session)
	case "/admin/channels":
		h.channels(w, r, session)
	case "/admin/channels/save":
		h.saveChannel(w, r, session)
	case "/admin/channels/action":
		h.channelAction(w, r, session)
	case "/admin/routes":
		h.routes(w, r, session)
	case "/admin/routes/save":
		h.saveRoute(w, r, session)
	case "/admin/routes/delete":
		h.deleteRoute(w, r, session)
	case "/admin/silences":
		h.silences(w, r, session)
	case "/admin/silences/create":
		h.createSilence(w, r, session)
	case "/admin/silences/delete":
		h.deleteSilence(w, r, session)
	case "/admin/deliveries":
		h.deliveries(w, r, session)
	case "/admin/deliveries/retry":
		h.retryDelivery(w, r, session)
	case "/admin/guide":
		h.guide(w, r, session)
	default:
		http.NotFound(w, r)
	}
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		h.render(w, http.StatusOK, "login.html", pageData{Title: "登录"})
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodGet+", "+http.MethodPost)
		return
	}
	if err := h.parseForm(w, r); err != nil {
		h.render(w, http.StatusBadRequest, "login.html", pageData{Title: "登录", Error: "请求格式无效"})
		return
	}
	now := h.cfg.Now().UTC()
	if err := h.cfg.Database.RecordAdminLoginAttempt(r.Context(), now, 10); err != nil {
		if errors.Is(err, store.ErrRateLimit) {
			w.Header().Set("Retry-After", "60")
			h.render(w, http.StatusTooManyRequests, "login.html", pageData{Title: "登录", Error: "尝试次数过多，请一分钟后再试"})
			return
		}
		h.cfg.Logger.Error("admin login rate limit", "error", err)
		h.render(w, http.StatusServiceUnavailable, "login.html", pageData{Title: "登录", Error: "服务暂时不可用"})
		return
	}
	userDigest, expectedUser := sha256.Sum256([]byte(r.Form.Get("username"))), sha256.Sum256([]byte(h.cfg.Username))
	select {
	case h.passwordGate <- struct{}{}:
		defer func() { <-h.passwordGate }()
	case <-r.Context().Done():
		h.render(w, http.StatusRequestTimeout, "login.html", pageData{Title: "登录", Error: "登录请求已取消"})
		return
	}
	passwordOK := passwordhash.Verify([]byte(r.Form.Get("password")), h.cfg.PasswordHash)
	if subtle.ConstantTimeCompare(userDigest[:], expectedUser[:]) != 1 || !passwordOK {
		h.render(w, http.StatusUnauthorized, "login.html", pageData{Title: "登录", Error: "用户名或密码不正确"})
		return
	}
	token, err := randomToken(32)
	if err != nil {
		h.renderError(w, http.StatusInternalServerError, "无法创建会话")
		return
	}
	csrf, err := randomToken(24)
	if err != nil {
		h.renderError(w, http.StatusInternalServerError, "无法创建会话")
		return
	}
	hash := sha256.Sum256([]byte(token))
	if err := h.cfg.Database.CreateAdminSession(r.Context(), hash[:], csrf, now.Add(h.cfg.SessionLifetime), now); err != nil {
		h.cfg.Logger.Error("create admin session", "error", err)
		h.renderError(w, http.StatusServiceUnavailable, "服务暂时不可用")
		return
	}
	h.setSessionCookie(w, token, int(h.cfg.SessionLifetime.Seconds()))
	h.cfg.Logger.Info("admin login succeeded")
	http.Redirect(w, r, "/admin/", http.StatusSeeOther)
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		hash := sha256.Sum256([]byte(cookie.Value))
		_ = h.cfg.Database.DeleteAdminSession(r.Context(), hash[:])
	}
	h.setSessionCookie(w, "", -1)
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

func (h *Handler) dashboard(w http.ResponseWriter, r *http.Request, session store.AdminSession) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	stats, err := h.cfg.Database.Dashboard(r.Context(), h.cfg.Now().UTC())
	if err != nil {
		h.operationError(w, r, err)
		return
	}
	h.render(w, http.StatusOK, "dashboard.html", h.page(r, session, "运行概览", "dashboard", "", stats))
}

func (h *Handler) clients(w http.ResponseWriter, r *http.Request, session store.AdminSession) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	data := h.page(r, session, "客户端", "clients", "", store.DashboardStats{})
	data.Clients = h.cfg.Gateway.Clients()
	data.Routes = h.cfg.Gateway.Routes()
	data.RouteOptions = routeOptions(data.Clients, data.Routes)
	h.render(w, http.StatusOK, "clients.html", data)
}

func (h *Handler) createClient(w http.ResponseWriter, r *http.Request, session store.AdminSession) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	limit, _ := strconv.Atoi(r.Form.Get("rate_limit"))
	secret, err := h.cfg.Gateway.CreateClient(r.Context(), strings.TrimSpace(r.Form.Get("id")), r.Form.Get("enabled") == "on", formList(r.Form, "routes"), limit)
	if err != nil {
		h.redirectFailure(w, r, "/admin/clients", err)
		return
	}
	h.render(w, http.StatusCreated, "secret.html", pageData{Title: "客户端密钥", Active: "clients", CSRF: session.CSRFToken, Secret: secret, SecretOwner: r.Form.Get("id")})
}

func (h *Handler) clientAction(w http.ResponseWriter, r *http.Request, session store.AdminSession) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	id, action := r.Form.Get("id"), r.Form.Get("action")
	switch action {
	case "update":
		limit, _ := strconv.Atoi(r.Form.Get("rate_limit"))
		if err := h.cfg.Gateway.UpdateClient(r.Context(), id, r.Form.Get("enabled") == "on", formList(r.Form, "routes"), limit); err != nil {
			h.redirectFailure(w, r, "/admin/clients", err)
			return
		}
	case "rotate":
		secret, err := h.cfg.Gateway.RotateClientSecret(r.Context(), id)
		if err != nil {
			h.redirectFailure(w, r, "/admin/clients", err)
			return
		}
		h.render(w, http.StatusOK, "secret.html", pageData{Title: "新客户端密钥", Active: "clients", CSRF: session.CSRFToken, Secret: secret, SecretOwner: id})
		return
	case "delete":
		if err := h.cfg.Gateway.DeleteClient(r.Context(), id); err != nil {
			h.redirectFailure(w, r, "/admin/clients", err)
			return
		}
	default:
		h.redirectFailure(w, r, "/admin/clients", errors.New("unknown action"))
		return
	}
	http.Redirect(w, r, "/admin/clients?ok=saved", http.StatusSeeOther)
}

func (h *Handler) channels(w http.ResponseWriter, r *http.Request, session store.AdminSession) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	data := h.page(r, session, "通知渠道", "channels", "", store.DashboardStats{})
	data.Channels = h.cfg.Gateway.Channels()
	h.render(w, http.StatusOK, "channels.html", data)
}

func (h *Handler) saveChannel(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	port, _ := strconv.Atoi(r.Form.Get("smtp_port"))
	keyword := strings.TrimSpace(r.Form.Get("keyword"))
	input := runtimecfg.ChannelInput{ID: strings.TrimSpace(r.Form.Get("id")), Type: r.Form.Get("type"), Enabled: r.Form.Get("enabled") == "on", Endpoint: strings.TrimSpace(r.Form.Get("endpoint")), Secret: r.Form.Get("secret"), ChatID: strings.TrimSpace(r.Form.Get("chat_id")), SMTPHost: strings.TrimSpace(r.Form.Get("smtp_host")), SMTPPort: port, SMTPUsername: strings.TrimSpace(r.Form.Get("smtp_username")), SMTPFrom: strings.TrimSpace(r.Form.Get("smtp_from")), SMTPRecipients: splitCSV(r.Form.Get("smtp_recipients")), SMTPMode: r.Form.Get("smtp_mode"), MessageType: r.Form.Get("message_type"), Keyword: &keyword}
	if err := h.cfg.Gateway.UpsertChannel(r.Context(), input); err != nil {
		h.redirectFailure(w, r, "/admin/channels", err)
		return
	}
	http.Redirect(w, r, "/admin/channels?ok=saved", http.StatusSeeOther)
}

func (h *Handler) channelAction(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	id, action := r.Form.Get("id"), r.Form.Get("action")
	switch action {
	case "toggle":
		var found *runtimecfg.ChannelView
		for _, view := range h.cfg.Gateway.Channels() {
			if view.ID == id {
				copy := view
				found = &copy
				break
			}
		}
		if found == nil {
			h.redirectFailure(w, r, "/admin/channels", store.ErrNotFound)
			return
		}
		if err := h.cfg.Gateway.UpsertChannel(r.Context(), runtimecfg.ChannelInput{ID: found.ID, Type: found.Type, Enabled: !found.Enabled}); err != nil {
			h.redirectFailure(w, r, "/admin/channels", err)
			return
		}
	case "test":
		if err := h.cfg.Gateway.QueueChannelTest(r.Context(), id); err != nil {
			h.redirectFailure(w, r, "/admin/channels", err)
			return
		}
	case "delete":
		if err := h.cfg.Gateway.DeleteChannel(r.Context(), id); err != nil {
			h.redirectFailure(w, r, "/admin/channels", err)
			return
		}
	default:
		h.redirectFailure(w, r, "/admin/channels", errors.New("unknown action"))
		return
	}
	http.Redirect(w, r, "/admin/channels?ok="+url.QueryEscape(action), http.StatusSeeOther)
}

func (h *Handler) routes(w http.ResponseWriter, r *http.Request, session store.AdminSession) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	data := h.page(r, session, "路由规则", "routes", "", store.DashboardStats{})
	data.Routes, data.Channels = h.cfg.Gateway.Routes(), h.cfg.Gateway.Channels()
	data.RouteOptions = routeOptions(nil, data.Routes)
	h.render(w, http.StatusOK, "routes.html", data)
}

func (h *Handler) saveRoute(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if err := h.cfg.Gateway.ReplaceRoute(r.Context(), strings.TrimSpace(r.Form.Get("routing_key")), r.Form.Get("severity"), formList(r.Form, "channels")); err != nil {
		h.redirectFailure(w, r, "/admin/routes", err)
		return
	}
	http.Redirect(w, r, "/admin/routes?ok=saved", http.StatusSeeOther)
}

func (h *Handler) deleteRoute(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if err := h.cfg.Gateway.DeleteRoute(r.Context(), r.Form.Get("routing_key"), r.Form.Get("severity")); err != nil {
		h.redirectFailure(w, r, "/admin/routes", err)
		return
	}
	http.Redirect(w, r, "/admin/routes?ok=deleted", http.StatusSeeOther)
}

func (h *Handler) silences(w http.ResponseWriter, r *http.Request, session store.AdminSession) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	records, err := h.cfg.Database.ListSilences(r.Context())
	if err != nil {
		h.operationError(w, r, err)
		return
	}
	data := h.page(r, session, "静默窗口", "silences", "", store.DashboardStats{})
	data.Silences = records
	data.Routes = h.cfg.Gateway.Routes()
	data.RouteOptions = routeOptions(nil, data.Routes)
	data.NowLocal = h.cfg.Now().In(h.cfg.DisplayLocation).Format("2006-01-02T15:04")
	h.render(w, http.StatusOK, "silences.html", data)
}

func (h *Handler) createSilence(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	starts, err1 := time.ParseInLocation("2006-01-02T15:04", r.Form.Get("starts_at"), time.Local)
	ends, err2 := time.ParseInLocation("2006-01-02T15:04", r.Form.Get("ends_at"), time.Local)
	if err1 != nil || err2 != nil {
		h.redirectFailure(w, r, "/admin/silences", errors.New("invalid time"))
		return
	}
	if err := h.cfg.Gateway.CreateSilence(r.Context(), r.Form.Get("routing_key"), r.Form.Get("severity"), starts, ends, r.Form.Get("reason")); err != nil {
		h.redirectFailure(w, r, "/admin/silences", err)
		return
	}
	http.Redirect(w, r, "/admin/silences?ok=saved", http.StatusSeeOther)
}

func (h *Handler) deleteSilence(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if err := h.cfg.Gateway.DeleteSilence(r.Context(), r.Form.Get("id")); err != nil {
		h.redirectFailure(w, r, "/admin/silences", err)
		return
	}
	http.Redirect(w, r, "/admin/silences?ok=deleted", http.StatusSeeOther)
}

func (h *Handler) deliveries(w http.ResponseWriter, r *http.Request, session store.AdminSession) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	status := r.URL.Query().Get("status")
	if !store.ValidateDeliveryStatus(status) {
		status = ""
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	items, total, err := h.cfg.Database.ListDeliveries(r.Context(), status, 50, (page-1)*50)
	if err != nil {
		h.operationError(w, r, err)
		return
	}
	totalPages := (total + 49) / 50
	if totalPages < 1 {
		totalPages = 1
	}
	data := h.page(r, session, "投递记录", "deliveries", "", store.DashboardStats{})
	data.Deliveries, data.Filter, data.Page, data.TotalPages = items, status, page, totalPages
	h.render(w, http.StatusOK, "deliveries.html", data)
}

func (h *Handler) guide(w http.ResponseWriter, r *http.Request, session store.AdminSession) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	baseURL, err := requestBaseURL(r)
	if err != nil {
		http.Error(w, "invalid request host", http.StatusBadRequest)
		return
	}
	data := h.page(r, session, "接入指南", "guide", "", store.DashboardStats{})
	data.GuideBaseURL = baseURL
	data.Clients = h.cfg.Gateway.Clients()
	data.Routes = h.cfg.Gateway.Routes()
	data.GuideClientID = "your-client"
	data.GuideRoute = "your-route"
	data.GuideSeverity = "info"
	requested := strings.TrimSpace(r.URL.Query().Get("client"))
	selected := -1
	for index, client := range data.Clients {
		if client.ID == requested {
			selected = index
			break
		}
		if selected == -1 && client.Enabled {
			selected = index
		}
	}
	if selected == -1 && len(data.Clients) > 0 {
		selected = 0
	}
	if selected >= 0 {
		client := data.Clients[selected]
		data.GuideClientID = client.ID
		data.GuideClientEnabled = client.Enabled
		data.GuideRoutes = append([]string(nil), client.AllowedRoutes...)
		if len(data.GuideRoutes) > 0 {
			data.GuideRoute = data.GuideRoutes[0]
		}
		for _, allowedRoute := range data.GuideRoutes {
			available := map[string]bool{}
			for _, route := range data.Routes {
				if route.RoutingKey == allowedRoute && len(route.Channels) > 0 {
					available[route.Severity] = true
				}
			}
			for _, severity := range []string{"info", "warning", "critical"} {
				if available[severity] {
					data.GuideRoute = allowedRoute
					data.GuideSeverity = severity
					data.GuideRouteConfigured = true
					break
				}
			}
			if data.GuideRouteConfigured {
				break
			}
		}
	}
	h.render(w, http.StatusOK, "guide.html", data)
}

func requestBaseURL(r *http.Request) (string, error) {
	host, err := validatedRequestHost(r.Host)
	if err != nil {
		return "", err
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	} else {
		// The production container is only reachable through the local Baota
		// reverse proxy, which sets this header from Nginx's trusted $scheme.
		forwardedProto := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0])
		if forwardedProto == "http" || forwardedProto == "https" {
			scheme = forwardedProto
		}
	}
	return scheme + "://" + host, nil
}

func validatedRequestHost(raw string) (string, error) {
	if raw == "" || len(raw) > 320 || strings.TrimSpace(raw) != raw {
		return "", errors.New("invalid request host")
	}
	hostname, port := raw, ""
	switch {
	case strings.HasPrefix(raw, "["):
		if strings.HasSuffix(raw, "]") {
			hostname = raw[1 : len(raw)-1]
		} else {
			var err error
			hostname, port, err = net.SplitHostPort(raw)
			if err != nil {
				return "", errors.New("invalid request host")
			}
		}
	case strings.Count(raw, ":") == 1:
		var err error
		hostname, port, err = net.SplitHostPort(raw)
		if err != nil {
			return "", errors.New("invalid request host")
		}
	case strings.Contains(raw, ":"):
		return "", errors.New("IPv6 request hosts must use brackets")
	}
	if port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return "", errors.New("invalid request port")
		}
	}
	if net.ParseIP(hostname) == nil && !validDNSHostname(hostname) {
		return "", errors.New("invalid request hostname")
	}
	return raw, nil
}

func validDNSHostname(hostname string) bool {
	hostname = strings.TrimSuffix(hostname, ".")
	if hostname == "" || len(hostname) > 253 {
		return false
	}
	for _, label := range strings.Split(hostname, ".") {
		if len(label) == 0 || len(label) > 63 || !isASCIILetterOrDigit(label[0]) || !isASCIILetterOrDigit(label[len(label)-1]) {
			return false
		}
		for index := 1; index < len(label)-1; index++ {
			if !isASCIILetterOrDigit(label[index]) && label[index] != '-' {
				return false
			}
		}
	}
	return true
}

func isASCIILetterOrDigit(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9'
}

func (h *Handler) retryDelivery(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if err := h.cfg.Database.RetryDeadDelivery(r.Context(), r.Form.Get("id"), h.cfg.Now().UTC()); err != nil {
		h.redirectFailure(w, r, "/admin/deliveries?status=dead", err)
		return
	}
	http.Redirect(w, r, "/admin/deliveries?status=dead&ok=retry", http.StatusSeeOther)
}

func (h *Handler) authenticate(r *http.Request) (store.AdminSession, bool) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil || len(cookie.Value) < 32 {
		return store.AdminSession{}, false
	}
	hash := sha256.Sum256([]byte(cookie.Value))
	session, err := h.cfg.Database.GetAdminSession(r.Context(), hash[:], h.cfg.Now().UTC())
	return session, err == nil
}

func (h *Handler) parseForm(w http.ResponseWriter, r *http.Request) error {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/x-www-form-urlencoded" {
		return errors.New("invalid content type")
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	return r.ParseForm()
}

func (h *Handler) page(r *http.Request, session store.AdminSession, title, active, notice string, stats store.DashboardStats) pageData {
	if notice == "" && r.URL.Query().Get("ok") != "" {
		notice = "操作已完成"
	}
	return pageData{Title: title, Active: active, CSRF: session.CSRFToken, Notice: notice, Error: r.URL.Query().Get("error"), Stats: stats}
}

func (h *Handler) redirectFailure(w http.ResponseWriter, r *http.Request, path string, err error) {
	h.cfg.Logger.Warn("admin operation failed", "path", r.URL.Path, "error", err)
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	http.Redirect(w, r, path+separator+"error="+url.QueryEscape("操作失败，请检查输入和当前状态"), http.StatusSeeOther)
}

func (h *Handler) operationError(w http.ResponseWriter, _ *http.Request, err error) {
	h.cfg.Logger.Error("admin read failed", "error", err)
	h.renderError(w, http.StatusServiceUnavailable, "服务暂时不可用")
}
func (h *Handler) renderError(w http.ResponseWriter, status int, message string) {
	h.render(w, status, "login.html", pageData{Title: "错误", Error: message})
}

func (h *Handler) render(w http.ResponseWriter, status int, name string, data pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := h.templates[name].ExecuteTemplate(w, "layout", data); err != nil {
		h.cfg.Logger.Error("render admin template", "template", name, "error", err)
	}
}

func (h *Handler) serveAsset(w http.ResponseWriter, r *http.Request, contentType string, content []byte) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write(content)
}

func (h *Handler) adminHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; img-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
}

func (h *Handler) setSessionCookie(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: value, Path: "/admin", MaxAge: maxAge, HttpOnly: true, Secure: h.cfg.SecureCookie, SameSite: http.SameSiteStrictMode})
}

func randomToken(size int) (string, error) {
	value := make([]byte, size)
	if _, err := io.ReadFull(rand.Reader, value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}
func splitCSV(value string) []string {
	return splitValues([]string{value})
}
func formList(values url.Values, key string) []string {
	return splitValues(values[key])
}
func splitValues(values []string) []string {
	result := []string{}
	seen := map[string]struct{}{}
	for _, value := range values {
		for _, item := range strings.Split(value, ",") {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			if _, ok := seen[item]; !ok {
				seen[item] = struct{}{}
				result = append(result, item)
			}
		}
	}
	return result
}
func routeOptions(clients []runtimecfg.ClientView, routes []runtimecfg.RouteView) []string {
	seen := map[string]struct{}{}
	for _, route := range routes {
		seen[route.RoutingKey] = struct{}{}
	}
	for _, client := range clients {
		for _, route := range client.AllowedRoutes {
			seen[route] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for route := range seen {
		result = append(result, route)
	}
	sort.Strings(result)
	return result
}
func methodNotAllowed(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}
