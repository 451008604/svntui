package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"svntui/internal/config"
	"svntui/internal/web"
	"svntui/internal/webui"
)

type webSettings struct {
	port        int
	noOpen      bool
	workingCopy string
}

func runWeb(ctx context.Context, env environment, args []string, stdout, stderr io.Writer) error {
	if wantsJSON(ctx) {
		return usage("Web 服务不支持 --json；请使用工作副本脚本命令")
	}
	flags := newFlagSet("web", stderr)
	settings := webSettings{}
	flags.IntVar(&settings.port, "port", 0, "本机监听端口；0 自动分配")
	flags.BoolVar(&settings.noOpen, "no-open", false, "只启动服务，不自动打开浏览器")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if settings.port < 0 || settings.port > 65535 || flags.NArg() > 1 {
		return usage("用法: svntui web [--port 0..65535] [--no-open] [工作副本]")
	}
	if flags.NArg() == 1 {
		settings.workingCopy = flags.Arg(0)
	}
	return serveWeb(ctx, env, settings, stdout, stderr, openWebBrowser)
}

func serveWeb(ctx context.Context, env environment, settings webSettings, stdout, stderr io.Writer, openBrowser func(string) error) error {
	if ctx.Err() != nil {
		return nil
	}
	// 显式目录必须有效；从普通目录启动仍可进入项目选择页。
	path := settings.workingCopy
	if path == "" {
		path, _ = os.Getwd()
	}
	info, infoErr := env.client.Info(ctx, path)
	if settings.workingCopy != "" {
		if infoErr != nil {
			return fmt.Errorf("打开 Web 工作副本: %w", infoErr)
		}
		if info.WCAbsolute == "" {
			return errors.New("Web 需要本机 SVN 工作副本目录，不能使用仓库 URL")
		}
	}
	if infoErr == nil && info.WCAbsolute != "" {
		root, err := filepath.EvalSymlinks(info.WCAbsolute)
		if err != nil {
			return fmt.Errorf("解析工作副本根目录: %w", err)
		}
		if err := env.configStore.Update(func(cfg *config.Config) error {
			cfg.RememberProject(root)
			return nil
		}); err != nil {
			return err
		}
	}

	// 仅绑定回环地址；实际端口用于精确校验浏览器请求来源。
	listener, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(settings.port)))
	if err != nil {
		return fmt.Errorf("启动本地 Web 服务: %w", err)
	}
	defer listener.Close()
	origin := "http://" + listener.Addr().String()
	application, err := web.New(web.Options{
		Client: env.client, ConfigStore: env.configStore, SafePoints: env.safePoints,
		Operations: env.operations, LockDir: filepath.Join(env.dirs.State, "working-copy-locks"),
		StateDir: env.dirs.State, Version: version, Origin: origin, Assets: webui.Assets(),
	})
	if err != nil {
		return err
	}
	server := &http.Server{
		Handler: application.Handler(), ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout: time.Minute, MaxHeaderBytes: 32 << 10,
	}
	finished := make(chan error, 1)
	go func() { finished <- server.Serve(listener) }()
	writeWebStartup(stdout, origin, application.BootstrapURL())
	if !settings.noOpen {
		if err := openBrowser(application.BootstrapURL()); err != nil {
			fmt.Fprintln(stderr, "未能自动打开浏览器，请使用终端中的启动链接。")
		}
	}

	select {
	case err := <-finished:
		_ = application.Close(context.Background())
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		// 先阻止新任务并等待写入结束，再关闭 HTTP 和事件流。
		fmt.Fprintln(stdout, "正在结束 Web 服务，等待正在执行的操作完成…")
		applicationErr := application.Close(context.Background())
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutdownErr := server.Shutdown(shutdownCtx)
		if shutdownErr != nil {
			_ = server.Close()
		}
		serveErr := <-finished
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
		return errors.Join(applicationErr, shutdownErr, serveErr)
	}
}

func writeWebStartup(writer io.Writer, origin, startupURL string) {
	fmt.Fprintf(writer, "svntui %s Web 已启动：%s\n", version, origin)
	// 启动凭证只显示在交互设备，避免重定向时写入普通日志。
	if file, ok := writer.(*os.File); ok {
		if info, err := file.Stat(); err == nil && info.Mode()&os.ModeCharDevice != 0 {
			fmt.Fprintln(writer, "首次访问链接："+startupURL)
			fmt.Fprintln(writer, "按 Ctrl-C 停止服务。请勿分享首次访问链接。")
			return
		}
	}
	fmt.Fprintln(writer, "首次访问链接仅在交互终端显示；浏览器将通过启动凭证建立本机会话。")
}

func openWebBrowser(address string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", address)
	case "windows":
		command = exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", address)
	default:
		command = exec.Command("xdg-open", address)
	}
	if err := command.Start(); err != nil {
		return err
	}
	go func() { _ = command.Wait() }()
	return nil
}
