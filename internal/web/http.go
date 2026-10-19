package web

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"strings"

	"svntui/internal/safepoint"
	"svntui/internal/svn"
	"svntui/internal/wclock"
)

const outputLimit = 1 << 20
const cookieName = "svntui_session"

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	status  int
	exit    int
}

func (e *apiError) Error() string { return e.Message }
func problem(status int, code, message string) error {
	return &apiError{Code: code, Message: message, status: status, exit: 1}
}

type envelope struct {
	OK       bool      `json:"ok"`
	Data     any       `json:"data"`
	Error    *apiError `json:"error"`
	ExitCode int       `json:"exit_code"`
}

func (s *Server) reply(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(envelope{OK: true, Data: data})
}
func errorInfo(err error) *apiError {
	var p *apiError
	if errors.As(err, &p) {
		return p
	}
	p = &apiError{Code: "internal", Message: err.Error(), status: 500, exit: 1}
	var command *svn.CommandError
	if errors.As(err, &command) {
		p.Code = string(command.Kind)
		p.exit = command.Result.ExitCode
		if p.exit == 0 {
			p.exit = 1
		}
		switch command.Kind {
		case svn.ErrorAuth:
			p.status = 401
		case svn.ErrorPermission:
			p.status = 403
		case svn.ErrorNotWorkingCopy:
			p.status = 400
		case svn.ErrorConflict, svn.ErrorLocked, svn.ErrorOutOfDate:
			p.status = 409
		case svn.ErrorCanceled:
			p.status = 408
		case svn.ErrorOutputLimit:
			p.status = 413
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		p.Code = "canceled"
		p.status = 408
	}
	if errors.Is(err, wclock.ErrBusy) {
		p.Code = "busy"
		p.status = 409
	}
	if errors.Is(err, safepoint.ErrInvalidID) {
		p.Code = "invalid_argument"
		p.status = 400
	}
	if errors.Is(err, os.ErrNotExist) {
		p.Code = "not_found"
		p.status = 404
	}
	if errors.Is(err, safepoint.ErrNotFound) {
		p.Code = "not_found"
		p.status = 404
	}
	if errors.Is(err, safepoint.ErrRevisionMismatch) || errors.Is(err, safepoint.ErrRepositoryMismatch) || errors.Is(err, safepoint.ErrDirtyWorkingCopy) {
		p.Code = "conflict"
		p.status = 409
	}
	return p
}
func (s *Server) fail(w http.ResponseWriter, err error) {
	p := errorInfo(err)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(p.status)
	_ = json.NewEncoder(w).Encode(envelope{Error: p, ExitCode: p.exit})
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	if mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mediaType != "application/json" {
		return problem(415, "invalid_content_type", "请求必须使用 application/json")
	}
	r.Body = http.MaxBytesReader(w, r.Body, outputLimit)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return problem(400, "invalid_argument", "无效 JSON: "+err.Error())
	}
	if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
		return problem(400, "invalid_argument", "请求只能包含一个 JSON 对象")
	}
	return nil
}
func equalSecret(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		if r.Host != s.origin.Host || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != s.opts.Origin) {
			s.fail(w, problem(403, "origin", "请求地址与回环监听地址不匹配"))
			return
		}
		if site := r.Header.Get("Sec-Fetch-Site"); site == "cross-site" {
			s.fail(w, problem(403, "origin", "拒绝跨站请求"))
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		s.mu.Lock()
		closed := s.closed
		s.mu.Unlock()
		if closed {
			s.fail(w, problem(503, "closed", "服务正在关闭"))
			return
		}
		mutation := r.Method != "GET" && r.Method != "HEAD"
		if mutation && r.Header.Get("Origin") != s.opts.Origin {
			s.fail(w, problem(403, "origin", "写入请求需要相同 Origin"))
			return
		}
		if r.URL.Path == "/api/v1/session" && r.Method == "POST" {
			next.ServeHTTP(w, r)
			return
		}
		cookie, err := r.Cookie(cookieName)
		if err != nil || !equalSecret(cookie.Value, s.session) {
			s.fail(w, problem(401, "unauthorized", "请从启动链接建立会话"))
			return
		}
		s.mu.Lock()
		used := s.startupUsed
		s.mu.Unlock()
		if !used {
			s.fail(w, problem(401, "unauthorized", "会话尚未建立"))
			return
		}
		if mutation && !equalSecret(r.Header.Get("X-CSRF-Token"), s.csrf) {
			s.fail(w, problem(403, "csrf", "CSRF 令牌无效"))
			return
		}
		next.ServeHTTP(w, r)
	})
}
func (s *Server) sessionData() any {
	return struct {
		CSRF    string `json:"csrf_token"`
		Version string `json:"version"`
	}{s.csrf, s.opts.Version}
}
func (s *Server) bootstrap(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if err := decode(w, r, &body); err != nil {
		s.fail(w, err)
		return
	}
	s.mu.Lock()
	valid := !s.startupUsed && equalSecret(body.Token, s.startup)
	if valid {
		s.startupUsed = true
	}
	s.mu.Unlock()
	if !valid {
		s.fail(w, problem(401, "unauthorized", "启动令牌无效或已使用"))
		return
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: s.session, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
	s.reply(w, 200, s.sessionData())
}
