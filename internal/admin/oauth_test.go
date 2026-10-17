package admin

import (
	"net/http"
	"net/url"
	"testing"
)

func TestOAuthLoginReturnAndRevocationCSRF(t *testing.T) {
	h, _ := newTestHandler(t)
	for _, bad := range []string{"https://evil.example/", "//evil.example/oauth/authorize", "/admin/", "/oauth/authorize#fragment"} {
		if oauthLoginNext(bad) != "" {
			t.Fatal("unsafe next", bad)
		}
	}
	next := "/admin/mcp/authorize?client_id=alertbridge-dots"
	login := serve(h, http.MethodPost, "/admin/login", url.Values{"username": {"admin"}, "password": {"correct horse battery staple"}, "next": {next}}, nil)
	if login.Code != 303 || login.Header().Get("Location") != next {
		t.Fatal(login.Code, login.Header())
	}
	cookie := login.Result().Cookies()[0]
	noCSRF := serve(h, http.MethodPost, "/admin/mcp/revoke", url.Values{}, cookie)
	if noCSRF.Code != 403 {
		t.Fatal(noCSRF.Code)
	}
	guide := serve(h, http.MethodGet, "/admin/guide", nil, cookie)
	csrf := extractCSRF(t, guide.Body.String())
	revoke := serve(h, http.MethodPost, "/admin/mcp/revoke", url.Values{"csrf": {csrf}}, cookie)
	if revoke.Code != 303 {
		t.Fatal(revoke.Code)
	}
}
