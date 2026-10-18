package automation

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestWebhookRetriesAndSignsExactBody(t *testing.T) {
	const secret = "test-secret"
	secretPath := filepath.Join(t.TempDir(), "webhook.secret")
	if err := os.WriteFile(secretPath, []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	attempts := 0
	signatureOK := false
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(request.Body)
		timestamp := request.Header.Get("X-SVNTUI-Timestamp")
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write([]byte(timestamp))
		_, _ = mac.Write([]byte("."))
		_, _ = mac.Write(body)
		want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
		signatureOK = hmac.Equal([]byte(request.Header.Get("X-SVNTUI-Signature")), []byte(want))
		attempts++
		status := http.StatusNoContent
		bodyText := ""
		if attempts == 1 {
			status = http.StatusInternalServerError
			bodyText = "retry"
		}
		return &http.Response{
			StatusCode: status,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(bodyText)),
			Request:    request,
		}, nil
	})

	client := WebhookClient{
		HTTPClient: &http.Client{Transport: transport},
		Now:        func() time.Time { return now },
		Sleep:      func(context.Context, time.Duration) error { return nil },
	}
	event := WebhookEvent{EventID: "event-1", Type: EventUpdateFailed, Time: now, Task: Task{ID: "task-1", Name: "main"}}
	delivery := client.Send(t.Context(), WebhookConfig{
		Name:      "test",
		URL:       "https://webhook.example.test/events",
		SecretRef: "file:" + secretPath,
	}, event)
	if !delivery.Delivered || delivery.Attempts != 2 || delivery.StatusCode != http.StatusNoContent {
		t.Fatalf("unexpected delivery: %+v", delivery)
	}
	if !signatureOK {
		t.Fatal("webhook signature did not cover timestamp and exact request body")
	}
}
