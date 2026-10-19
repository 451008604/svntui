package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWebFlagsAndStartupOutput(t *testing.T) {
	jsonContext := context.WithValue(t.Context(), jsonOutputKey{}, true)
	if err := runWeb(jsonContext, environment{}, nil, io.Discard, io.Discard); err == nil {
		t.Fatal("Web service accepted script-only JSON mode")
	}
	for _, args := range [][]string{{"--port", "-1"}, {"--port", "65536"}, {"one", "two"}} {
		var output bytes.Buffer
		if err := runWeb(t.Context(), environment{}, args, &output, &output); err == nil {
			t.Fatalf("accepted invalid arguments: %v", args)
		}
	}
	var output bytes.Buffer
	if err := runWeb(t.Context(), environment{}, []string{"-h"}, &output, &output); !errors.Is(err, errHelpDisplayed) {
		t.Fatalf("help: %v", err)
	}
	if !strings.Contains(output.String(), "no-open") {
		t.Fatalf("missing web help: %s", output.String())
	}
	output.Reset()
	writeWebStartup(&output, "http://127.0.0.1:12345", "http://127.0.0.1:12345/#token=secret-must-not-appear")
	if strings.Contains(output.String(), "secret-must-not-appear") || !strings.Contains(output.String(), "127.0.0.1:12345") {
		t.Fatalf("unsafe redirected startup output: %s", output.String())
	}
}

func TestWebServesAndStopsWithoutWorkingCopy(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	env, err := newEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	opened := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- serveWeb(ctx, env, webSettings{}, io.Discard, io.Discard, func(address string) error {
			opened <- address
			return errors.New("test browser unavailable")
		})
	}()
	var address string
	select {
	case address = <-opened:
	case err := <-done:
		t.Fatalf("server exited early: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("server failed to start")
	}
	startup, err := url.Parse(address)
	if err != nil {
		t.Fatal(err)
	}
	host, port, err := net.SplitHostPort(startup.Host)
	if err != nil || host != "127.0.0.1" || port == "0" || startup.Scheme != "http" {
		t.Fatalf("invalid loopback address: %s (%v)", address, err)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Get(startup.Scheme + "://" + startup.Host + "/")
	if err != nil {
		t.Fatal(err)
	}
	index, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || !bytes.Contains(index, []byte("<html")) {
		t.Fatalf("index response: status %d, err %v", response.StatusCode, err)
	}
	fragment, err := url.ParseQuery(startup.Fragment)
	if err != nil || fragment.Get("token") == "" {
		t.Fatal("browser was not given a fragment startup token")
	}
	data, _ := json.Marshal(map[string]string{"token": fragment.Get("token")})
	request, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, startup.Scheme+"://"+startup.Host+"/api/v1/session", bytes.NewReader(data))
	request.Header.Set("Origin", startup.Scheme+"://"+startup.Host)
	request.Header.Set("Content-Type", "application/json")
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || len(response.Cookies()) == 0 {
		t.Fatalf("session startup failed: status %d", response.StatusCode)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("server did not stop")
	}
	if connection, err := net.DialTimeout("tcp4", startup.Host, time.Second); err == nil {
		connection.Close()
		t.Fatal("listener still open after shutdown")
	}
}

func TestWebPortConflictAndExplicitInvalidPath(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	if err := serveWeb(t.Context(), environment{}, webSettings{port: port}, io.Discard, io.Discard, nil); err == nil {
		t.Fatal("occupied port was accepted")
	}
	if err := serveWeb(t.Context(), environment{}, webSettings{workingCopy: t.TempDir()}, io.Discard, io.Discard, nil); err == nil {
		t.Fatal("explicit non-working-copy path was accepted")
	}
	repository := filepath.Join(t.TempDir(), "repository")
	runCommand(t, filepath.Dir(repository), "svnadmin", "create", repository)
	address := repositoryURL(repository)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	err = serveWeb(ctx, environment{}, webSettings{workingCopy: address, noOpen: true}, io.Discard, io.Discard, nil)
	if err == nil || !strings.Contains(err.Error(), "本机 SVN 工作副本") {
		t.Fatalf("repository URL was not rejected as a local working-copy argument: %v", err)
	}
}
