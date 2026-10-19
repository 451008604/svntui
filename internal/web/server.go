// 提供仅限回环地址的 SVN 浏览器界面与结构化 API。
package web

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"svntui/internal/config"
	"svntui/internal/operation"
	"svntui/internal/safepoint"
	"svntui/internal/service"
	"svntui/internal/svn"
)

type Options struct {
	Client      svn.Client
	ConfigStore config.Store
	SafePoints  *safepoint.Manager
	Operations  operation.Recorder
	LockDir     string
	StateDir    string
	Version     string
	Origin      string
	Assets      fs.FS
}

type Server struct {
	opts        Options
	actions     service.Actions
	origin      *url.URL
	handler     http.Handler
	startup     string
	cookieName  string
	session     string
	csrf        string
	reads       context.Context
	cancelReads context.CancelFunc
	slots       chan struct{}
	mu          sync.Mutex
	closed      bool
	startupUsed bool
	previews    map[string]*frozenPreview
	jobs        map[string]Job
	jobOrder    []string
	active      map[string]string
	subscribers map[chan Job]struct{}
	writes      sync.WaitGroup
}

func New(opts Options) (*Server, error) {
	origin, err := url.Parse(opts.Origin)
	if err != nil || origin.Scheme != "http" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return nil, errors.New("Web Origin 必须是明确的回环 HTTP 地址")
	}
	host, port, err := net.SplitHostPort(origin.Host)
	portNumber, portErr := strconv.Atoi(port)
	if err != nil || host != "127.0.0.1" || portErr != nil || portNumber < 1 || portNumber > 65535 {
		return nil, errors.New("Web Origin 必须使用 127.0.0.1 和实际监听端口")
	}
	if opts.StateDir == "" || opts.ConfigStore.Path == "" || opts.LockDir == "" {
		return nil, errors.New("Web 需要配置、状态和共享写锁目录")
	}
	if opts.Assets == nil {
		return nil, errors.New("缺少 Web 静态资源")
	}
	if _, err := fs.Stat(opts.Assets, "index.html"); err != nil {
		return nil, fmt.Errorf("读取 Web 入口: %w", err)
	}
	if opts.Client.Binary == "" {
		opts.Client.Binary = "svn"
	}
	if opts.Client.MaxOutputBytes <= 0 || opts.Client.MaxOutputBytes > svn.DefaultMaxOutputBytes {
		opts.Client.MaxOutputBytes = svn.DefaultMaxOutputBytes
	}
	// Web 只返回受限输出；SVN 溢出制品在观察器记录后及时清理。
	observer := opts.Client.Observer
	opts.Client.Observer = func(command svn.Command, result svn.Result, err error) {
		if observer != nil {
			observer(command, result, err)
		}
		if result.OutputTruncated && result.OutputPath != "" {
			_ = os.Remove(result.OutputPath)
		}
	}
	startup, err := secret()
	if err != nil {
		return nil, err
	}
	session, err := secret()
	if err != nil {
		return nil, err
	}
	csrf, err := secret()
	if err != nil {
		return nil, err
	}
	// Cookie 不区分端口，按监听 Origin 派生名称，避免另一个 Web 实例覆盖会话。
	cookieID := sha256.Sum256([]byte(opts.Origin))
	reads, cancel := context.WithCancel(context.Background())
	s := &Server{opts: opts, origin: origin, startup: startup, cookieName: "svntui_session_" + hex.EncodeToString(cookieID[:12]), session: session, csrf: csrf, reads: reads, cancelReads: cancel, slots: make(chan struct{}, 4), previews: make(map[string]*frozenPreview), jobs: make(map[string]Job), active: make(map[string]string), subscribers: make(map[chan Job]struct{})}
	s.actions = service.Actions{Client: opts.Client, SafePoints: opts.SafePoints, LockDir: opts.LockDir}
	mux := http.NewServeMux()
	s.routes(mux)
	s.handler = s.guard(mux)
	return s, nil
}

func (s *Server) Handler() http.Handler { return s.handler }
func (s *Server) BootstrapURL() string  { return s.opts.Origin + "/#token=" + s.startup }

// 停止接收操作并取消读取；已开始的写入由进程上下文完成。
func (s *Server) Close(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	s.cancelReads()
	s.mu.Unlock()
	done := make(chan struct{})
	go func() { s.writes.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func secret() (string, error) {
	var bytes [32]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes[:]), nil
}
func projectID(root string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(root)))
	return hex.EncodeToString(sum[:16])
}
func (s *Server) requestContext(r *http.Request) (context.Context, func(), error) {
	ctx, cancel := context.WithCancel(r.Context())
	stop := context.AfterFunc(s.reads, cancel)
	select {
	case s.slots <- struct{}{}:
		return ctx, func() { <-s.slots; stop(); cancel() }, nil
	case <-ctx.Done():
		stop()
		cancel()
		return nil, nil, ctx.Err()
	}
}

func (s *Server) routes(m *http.ServeMux) {
	m.HandleFunc("POST /api/v1/session", s.bootstrap)
	m.HandleFunc("GET /api/v1/session", func(w http.ResponseWriter, r *http.Request) { s.reply(w, 200, s.sessionData()) })
	m.HandleFunc("GET /api/v1/projects", s.projects)
	m.HandleFunc("POST /api/v1/projects", s.registerProject)
	m.HandleFunc("DELETE /api/v1/projects/{id}", s.removeProject)
	m.HandleFunc("GET /api/v1/projects/{id}/status", s.status)
	m.HandleFunc("GET /api/v1/projects/{id}/diff", s.diff)
	m.HandleFunc("GET /api/v1/projects/{id}/history", s.history)
	m.HandleFunc("GET /api/v1/projects/{id}/history/{revision}/diff", s.historyDiff)
	m.HandleFunc("GET /api/v1/projects/{id}/conflicts", s.conflicts)
	m.HandleFunc("GET /api/v1/projects/{id}/draft", s.getDraft)
	m.HandleFunc("PUT /api/v1/projects/{id}/draft", s.putDraft)
	m.HandleFunc("POST /api/v1/projects/{id}/previews", s.preview)
	m.HandleFunc("POST /api/v1/projects/{id}/operations", s.execute)
	m.HandleFunc("GET /api/v1/projects/{id}/operations", s.audit)
	m.HandleFunc("GET /api/v1/projects/{id}/safepoints", s.safePoints)
	m.HandleFunc("GET /api/v1/projects/{id}/safepoints/{sid}", s.safePoint)
	m.HandleFunc("GET /api/v1/jobs", s.listJobs)
	m.HandleFunc("GET /api/v1/jobs/{job}", s.getJob)
	m.HandleFunc("GET /api/v1/events", s.events)
	m.HandleFunc("/", s.assets)
}

func (s *Server) assets(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/api" {
		s.fail(w, problem(404, "not_found", "API 不存在"))
		return
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		s.fail(w, problem(405, "method_not_allowed", "不支持此方法"))
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		path = "index.html"
	}
	if !fs.ValidPath(path) || strings.HasPrefix(path, ".") {
		s.fail(w, problem(404, "not_found", "资源不存在"))
		return
	}
	info, err := fs.Stat(s.opts.Assets, path)
	if err != nil || info.IsDir() {
		// 仅页面导航可回退到 SPA 入口，缺失脚本返回 404。
		if strings.Contains(filepath.Base(path), ".") {
			s.fail(w, problem(404, "not_found", "资源不存在"))
			return
		}
		path = "index.html"
	}
	data, err := fs.ReadFile(s.opts.Assets, path)
	if err != nil {
		s.fail(w, err)
		return
	}
	if strings.HasSuffix(path, ".html") {
		w.Header().Set("Cache-Control", "no-store")
	}
	http.ServeContent(w, r, path, time.Time{}, strings.NewReader(string(data)))
}
