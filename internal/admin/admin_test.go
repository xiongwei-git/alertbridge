package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/xiongwei-git/alertbridge/internal/config"
	"github.com/xiongwei-git/alertbridge/internal/domain"
	"github.com/xiongwei-git/alertbridge/internal/runtimecfg"
	"github.com/xiongwei-git/alertbridge/internal/securestore"
	"github.com/xiongwei-git/alertbridge/internal/store"
)

func TestAdminAuthenticationCSRFAndClientCreation(t *testing.T) {
	handler, database := newTestHandler(t)
	mark := serve(handler, http.MethodGet, "/admin/assets/alertbridge-mark.svg", nil, nil)
	if mark.Code != http.StatusOK || mark.Header().Get("Content-Type") != "image/svg+xml" || !strings.Contains(mark.Body.String(), "<svg") || !strings.Contains(mark.Header().Get("Content-Security-Policy"), "img-src 'self'") {
		t.Fatalf("brand mark response = %d, type %q", mark.Code, mark.Header().Get("Content-Type"))
	}

	response := serve(handler, http.MethodGet, "/admin/", nil, nil)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/admin/login" {
		t.Fatalf("unauthenticated response = %d, location %q", response.Code, response.Header().Get("Location"))
	}

	invalid := serve(handler, http.MethodPost, "/admin/login", url.Values{"username": {"admin"}, "password": {"wrong-password-value"}}, nil)
	if invalid.Code != http.StatusUnauthorized || !strings.Contains(invalid.Body.String(), "用户名或密码不正确") {
		t.Fatalf("invalid login response = %d, %s", invalid.Code, invalid.Body.String())
	}

	login := serve(handler, http.MethodPost, "/admin/login", url.Values{"username": {"admin"}, "password": {"correct horse battery staple"}}, nil)
	if login.Code != http.StatusSeeOther || login.Header().Get("Location") != "/admin/" {
		t.Fatalf("login response = %d, location %q", login.Code, login.Header().Get("Location"))
	}
	cookies := login.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("session cookie is not hardened: %#v", cookies)
	}
	cookie := cookies[0]

	dashboard := serve(handler, http.MethodGet, "/admin/", nil, cookie)
	if dashboard.Code != http.StatusOK || !strings.Contains(dashboard.Body.String(), "运行概览") || !strings.Contains(dashboard.Body.String(), "alertbridge-mark.svg?v=7") {
		t.Fatalf("dashboard response = %d, %s", dashboard.Code, dashboard.Body.String())
	}
	csrf := extractCSRF(t, dashboard.Body.String())
	clientsPage := serve(handler, http.MethodGet, "/admin/clients", nil, cookie)
	if clientsPage.Code != http.StatusOK || !strings.Contains(clientsPage.Body.String(), `class="data-table client-table"`) || !strings.Contains(clientsPage.Body.String(), `form="client-edit-seed-client"`) || !strings.Contains(clientsPage.Body.String(), `data-label="操作"`) {
		t.Fatalf("client layout response = %d, %s", clientsPage.Code, clientsPage.Body.String())
	}
	if !strings.Contains(clientsPage.Body.String(), `href="/admin/guide">接入指南</a>`) {
		t.Fatalf("integration guide navigation is missing: %s", clientsPage.Body.String())
	}
	if !strings.Contains(clientsPage.Body.String(), `type="checkbox" name="routes" value="ops" checked`) || !strings.Contains(clientsPage.Body.String(), `type="checkbox" name="routes" value="security"`) || strings.Contains(clientsPage.Body.String(), `class="table-input route-input" name="routes"`) {
		t.Fatalf("client route selector response = %d, %s", clientsPage.Code, clientsPage.Body.String())
	}
	if !strings.Contains(clientsPage.Body.String(), `class="term-help"`) || !strings.Contains(clientsPage.Body.String(), `data-tip="决定客户端可以提交哪些逻辑路由`) {
		t.Fatalf("client terminology help is missing: %s", clientsPage.Body.String())
	}
	routesPage := serve(handler, http.MethodGet, "/admin/routes", nil, cookie)
	if routesPage.Code != http.StatusOK || !strings.Contains(routesPage.Body.String(), `type="checkbox" name="channels" value="feishu-test"`) || !strings.Contains(routesPage.Body.String(), `data-tip="告警的逻辑分类名称`) {
		t.Fatalf("route selector/help response = %d, %s", routesPage.Code, routesPage.Body.String())
	}
	guidePage := serve(handler, http.MethodGet, "/admin/guide", nil, cookie)
	if guidePage.Code != http.StatusOK || !strings.Contains(guidePage.Body.String(), "外部服务如何调用") || !strings.Contains(guidePage.Body.String(), "X-Notify-Signature") || !strings.Contains(guidePage.Body.String(), "openssl dgst") || !strings.Contains(guidePage.Body.String(), `POST http://example.com/api/v1/events`) || !strings.Contains(guidePage.Body.String(), `BASE_URL='http://example.com'`) {
		t.Fatalf("integration guide response = %d, %s", guidePage.Code, guidePage.Body.String())
	}
	for _, removed := range []string{"/hooks/", "Gatus", "Alertmanager", "Grafana", "Uptime Kuma"} {
		if strings.Contains(guidePage.Body.String(), removed) {
			t.Fatalf("integration guide still advertises removed compatibility surface %q: %s", removed, guidePage.Body.String())
		}
	}
	if !strings.Contains(guidePage.Body.String(), `ROUTING_KEY='ops'`) || !strings.Contains(guidePage.Body.String(), `"severity":"critical"`) {
		t.Fatalf("integration guide must use a configured route and severity: %s", guidePage.Body.String())
	}
	if strings.Contains(guidePage.Body.String(), "0123456789abcdef0123456789abcdef") {
		t.Fatal("integration guide must not expose a client secret")
	}
	channelsPage := serve(handler, http.MethodGet, "/admin/channels", nil, cookie)
	if channelsPage.Code != http.StatusOK || !strings.Contains(channelsPage.Body.String(), `name="keyword"`) || !strings.Contains(channelsPage.Body.String(), "安全关键词") {
		t.Fatalf("channel keyword UI response = %d, %s", channelsPage.Code, channelsPage.Body.String())
	}

	withoutCSRF := serve(handler, http.MethodPost, "/admin/clients/create", url.Values{"id": {"new-client"}, "routes": {"ops"}, "rate_limit": {"30"}, "enabled": {"on"}}, cookie)
	if withoutCSRF.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF response = %d", withoutCSRF.Code)
	}

	created := serve(handler, http.MethodPost, "/admin/clients/create", url.Values{"csrf": {csrf}, "id": {"new-client"}, "routes": {"ops", "security"}, "rate_limit": {"30"}, "enabled": {"on"}}, cookie)
	if created.Code != http.StatusCreated || !strings.Contains(created.Body.String(), "new-client 的新密钥") || !strings.Contains(created.Body.String(), `href="/admin/guide"`) {
		t.Fatalf("create client response = %d, %s", created.Code, created.Body.String())
	}
	record, err := database.GetClient(context.Background(), "new-client")
	if err != nil || !record.Enabled || record.RateLimitPerMinute != 30 || strings.Join(record.AllowedRoutes, ",") != "ops,security" {
		t.Fatalf("stored client = %#v, err = %v", record, err)
	}
}

func TestGuideUsesForwardedHTTPSOriginAndRejectsUnsafeHost(t *testing.T) {
	handler, _ := newTestHandler(t)
	login := serve(handler, http.MethodPost, "/admin/login", url.Values{"username": {"admin"}, "password": {"correct horse battery staple"}}, nil)
	cookie := login.Result().Cookies()[0]

	forwarded := httptest.NewRequest(http.MethodGet, "/admin/guide", nil)
	forwarded.Host = "notify.tedxiong.com"
	forwarded.Header.Set("X-Forwarded-Proto", "https")
	forwarded.AddCookie(cookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, forwarded)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `BASE_URL='https://notify.tedxiong.com'`) || !strings.Contains(response.Body.String(), `BASE_URL = "https://notify.tedxiong.com"`) {
		t.Fatalf("forwarded guide response = %d, %s", response.Code, response.Body.String())
	}

	unsafe := httptest.NewRequest(http.MethodGet, "/admin/guide", nil)
	unsafe.Host = "notify.tedxiong.com';touch-pwned;'"
	unsafe.AddCookie(cookie)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, unsafe)
	if response.Code != http.StatusBadRequest || strings.Contains(response.Body.String(), "touch-pwned") {
		t.Fatalf("unsafe host response = %d, %s", response.Code, response.Body.String())
	}
}

func TestAdminRejectsNonFormWritesAndEscapesDeliveryContent(t *testing.T) {
	handler, database := newTestHandler(t)
	login := serve(handler, http.MethodPost, "/admin/login", url.Values{"username": {"admin"}, "password": {"correct horse battery staple"}}, nil)
	cookie := login.Result().Cookies()[0]

	request := httptest.NewRequest(http.MethodPost, "/admin/logout", strings.NewReader(`{"csrf":"x"}`))
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("non-form write response = %d", response.Code)
	}

	// Insert a title that would execute if templates did not auto-escape it.
	now := time.Now().UTC()
	_, err := database.AcceptEvent(context.Background(), store.AcceptParams{ClientID: "seed-client", Event: domain.Event{EventID: "external-xss", Source: "test", RoutingKey: "ops", Status: domain.StatusFiring, Severity: domain.SeverityCritical, Title: "<script>alert(1)</script>", Message: "body", OccurredAt: now}, Targets: []string{"feishu-test"}, Now: now, DedupeWindow: time.Minute, RawPayload: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	deliveries := serve(handler, http.MethodGet, "/admin/deliveries", nil, cookie)
	if strings.Contains(deliveries.Body.String(), "<script>alert(1)</script>") || !strings.Contains(deliveries.Body.String(), "&lt;script&gt;") {
		t.Fatalf("delivery title was not HTML-escaped: %s", deliveries.Body.String())
	}
}

func newTestHandler(t *testing.T) (*Handler, *store.Store) {
	t.Helper()
	database, err := store.Open(filepath.Join(t.TempDir(), "alertbridge.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	cipher, err := securestore.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	bootstrap := config.Config{
		Clients:  map[string]config.ClientConfig{"seed-client": {Enabled: true, Secret: []byte("0123456789abcdef0123456789abcdef"), AllowedRoutes: []string{"ops"}, RateLimitPerMinute: 60}},
		Channels: map[string]config.ChannelConfig{"feishu-test": {Type: "feishu", Enabled: true, Webhook: "https://open.feishu.cn/open-apis/bot/v2/hook/test", MessageType: "text", Keyword: "AlertBridge", AllowedHosts: []string{"open.feishu.cn"}}},
		Routes:   map[string]map[string][]string{"ops": {"critical": {"feishu-test"}}, "security": {"critical": {"feishu-test"}}},
	}
	gateway, err := runtimecfg.New(context.Background(), runtimecfg.Options{Database: database, Cipher: cipher, Bootstrap: bootstrap})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := New(Config{Database: database, Gateway: gateway, Username: "admin", Password: []byte("correct horse battery staple"), SessionLifetime: time.Hour, SecureCookie: false})
	if err != nil {
		t.Fatal(err)
	}
	return handler, database
}

func serve(handler http.Handler, method, path string, form url.Values, cookie *http.Cookie) *httptest.ResponseRecorder {
	var body *strings.Reader
	if form == nil {
		body = strings.NewReader("")
	} else {
		body = strings.NewReader(form.Encode())
	}
	request := httptest.NewRequest(method, path, body)
	if form != nil {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func extractCSRF(t *testing.T, body string) string {
	t.Helper()
	match := regexp.MustCompile(`name="csrf" value="([^"]+)"`).FindStringSubmatch(body)
	if len(match) != 2 {
		t.Fatalf("CSRF token not found in %s", body)
	}
	return match[1]
}
