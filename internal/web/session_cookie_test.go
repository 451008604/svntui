package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"svntui/internal/config"
)

func TestTwoListenerSessionsShareCookieJarWithoutOverwriting(t *testing.T) {
	root := t.TempDir()
	opts := Options{ConfigStore: config.Store{Path: filepath.Join(root, "config.json")}, StateDir: filepath.Join(root, "state"), LockDir: filepath.Join(root, "locks"), Version: "test", Assets: fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("SVN")}}}
	makeServer := func(origin string) *Server {
		t.Helper()
		options := opts
		options.Origin = origin
		server, err := New(options)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := server.Close(context.Background()); err != nil {
				t.Error(err)
			}
		})
		return server
	}
	a := makeServer("http://127.0.0.1:60738")
	b := makeServer("http://127.0.0.1:60739")
	if a.cookieName == b.cookieName {
		t.Fatal("different listeners share a cookie name")
	}
	if !strings.HasPrefix(a.cookieName, "svntui_session_") || strings.Contains(a.cookieName, ":") {
		t.Fatalf("cookie name is not an opaque token: %q", a.cookieName)
	}
	restarted := makeServer(a.opts.Origin)
	if restarted.cookieName != a.cookieName {
		t.Fatal("same listener does not have a stable cookie name")
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	originURL := func(server *Server) *url.URL {
		t.Helper()
		u, err := url.Parse(server.opts.Origin)
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	call := func(server *Server, method, path string, body any, csrf, origin string) *httptest.ResponseRecorder {
		t.Helper()
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(method, server.opts.Origin+path, bytes.NewReader(data)).WithContext(t.Context())
		request.Header.Set("Content-Type", "application/json")
		if origin != "" {
			request.Header.Set("Origin", origin)
		}
		if csrf != "" {
			request.Header.Set("X-CSRF-Token", csrf)
		}
		for _, cookie := range jar.Cookies(request.URL) {
			request.AddCookie(cookie)
		}
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		jar.SetCookies(request.URL, response.Result().Cookies())
		return response
	}
	bootstrap := func(server *Server) string {
		t.Helper()
		response := call(server, "POST", "/api/v1/session", map[string]string{"token": server.startup}, "", server.opts.Origin)
		requireStatus(t, response, 200)
		cookies := response.Result().Cookies()
		if len(cookies) != 1 {
			t.Fatalf("bootstrap cookies: %d", len(cookies))
		}
		cookie := cookies[0]
		if err := cookie.Valid(); err != nil {
			t.Fatal(err)
		}
		if cookie.Name != server.cookieName || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || cookie.Domain != "" {
			t.Fatalf("cookie attributes changed: %+v", cookie)
		}
		var state struct {
			CSRF string `json:"csrf_token"`
		}
		readData(t, response, &state)
		return state.CSRF
	}
	csrfA := bootstrap(a)
	requireStatus(t, call(a, "GET", "/api/v1/session", nil, "", ""), 200)
	// 真正的 CookieJar 会把 A 的 Cookie 同样发送给 B：Cookie 的作用域忽略端口。
	if !slices.ContainsFunc(jar.Cookies(originURL(b)), func(cookie *http.Cookie) bool { return cookie.Name == a.cookieName }) {
		t.Fatal("test jar unexpectedly isolates cookies by port")
	}
	requireStatus(t, call(b, "GET", "/api/v1/session", nil, "", ""), 401)
	csrfB := bootstrap(b)
	requireStatus(t, call(b, "GET", "/api/v1/session", nil, "", ""), 200)
	requireStatus(t, call(a, "GET", "/api/v1/session", nil, "", ""), 200)
	for _, server := range []*Server{a, b} {
		cookies := jar.Cookies(originURL(server))
		if len(cookies) != 2 || !slices.ContainsFunc(cookies, func(cookie *http.Cookie) bool { return cookie.Name == a.cookieName }) || !slices.ContainsFunc(cookies, func(cookie *http.Cookie) bool { return cookie.Name == b.cookieName }) {
			t.Fatalf("listener cookies overwritten: %+v", cookies)
		}
		requireStatus(t, call(server, "POST", "/api/v1/session", map[string]string{"token": server.startup}, "", server.opts.Origin), 401)
	}
	if csrfA == csrfB {
		t.Fatal("listeners share CSRF tokens")
	}
	// 会话并存不改变每个服务自己的 Origin/CSRF 校验。
	requireStatus(t, call(a, "POST", "/api/v1/projects/missing/previews", PreviewRequest{Action: "cleanup"}, csrfB, a.opts.Origin), 403)
	requireStatus(t, call(a, "POST", "/api/v1/projects/missing/previews", PreviewRequest{Action: "cleanup"}, csrfA, b.opts.Origin), 403)
	requireStatus(t, call(a, "POST", "/api/v1/projects/missing/previews", PreviewRequest{Action: "cleanup"}, csrfA, a.opts.Origin), 404)
}
