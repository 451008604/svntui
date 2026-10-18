package automation

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	defaultWebhookAttempts = 3
	maxWebhookResponse     = 4096
)

type WebhookEvent struct {
	EventID string     `json:"event_id"`
	Type    EventType  `json:"type"`
	Time    time.Time  `json:"time"`
	Task    Task       `json:"task"`
	Run     *RunRecord `json:"run,omitempty"`
}

// WebhookClient 发送带可选 HMAC-SHA256 签名的通用 JSON 事件。
type WebhookClient struct {
	HTTPClient  *http.Client
	MaxAttempts int
	Now         func() time.Time
	Sleep       func(context.Context, time.Duration) error
}

func (c WebhookClient) Send(ctx context.Context, config WebhookConfig, event WebhookEvent) WebhookDelivery {
	finished := func() time.Time { return nowUTC(c.Now) }
	delivery := WebhookDelivery{EventID: event.EventID}
	if err := config.Normalize(); err != nil {
		delivery.Error = err.Error()
		delivery.Finished = finished()
		return delivery
	}
	body, err := json.Marshal(event)
	if err != nil {
		delivery.Error = fmt.Sprintf("编码 Webhook 事件: %v", err)
		delivery.Finished = finished()
		return delivery
	}
	secret, err := resolveSecret(config.SecretRef)
	if err != nil {
		delivery.Error = err.Error()
		delivery.Finished = finished()
		return delivery
	}
	attempts := c.MaxAttempts
	if attempts <= 0 {
		attempts = defaultWebhookAttempts
	}
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}

	for attempt := 1; attempt <= attempts; attempt++ {
		delivery.Attempts = attempt
		status, retry, sendErr := c.sendOnce(ctx, client, config, body, secret)
		delivery.StatusCode = status
		if sendErr == nil {
			delivery.Delivered = true
			delivery.Error = ""
			delivery.Finished = finished()
			return delivery
		}
		delivery.Error = sendErr.Error()
		if !retry || attempt == attempts || ctx.Err() != nil {
			break
		}
		if err := c.sleep(ctx, time.Duration(1<<(attempt-1))*250*time.Millisecond); err != nil {
			delivery.Error = err.Error()
			break
		}
	}
	delivery.Finished = finished()
	return delivery
}

func (c WebhookClient) sendOnce(ctx context.Context, client *http.Client, config WebhookConfig, body, secret []byte) (int, bool, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, config.URL, bytes.NewReader(body))
	if err != nil {
		return 0, false, fmt.Errorf("创建 Webhook 请求: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "svntui-automation/1")
	for name, value := range config.Headers {
		request.Header.Set(name, value)
	}
	if len(secret) > 0 {
		timestamp := nowUTC(c.Now).Format(time.RFC3339)
		mac := hmac.New(sha256.New, secret)
		_, _ = mac.Write([]byte(timestamp))
		_, _ = mac.Write([]byte("."))
		_, _ = mac.Write(body)
		request.Header.Set("X-SVNTUI-Timestamp", timestamp)
		request.Header.Set("X-SVNTUI-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}

	response, err := client.Do(request)
	if err != nil {
		return 0, true, fmt.Errorf("发送 Webhook: %w", err)
	}
	defer response.Body.Close()
	_, readErr := io.ReadAll(io.LimitReader(response.Body, maxWebhookResponse))
	if readErr != nil {
		return response.StatusCode, true, fmt.Errorf("读取 Webhook 响应: %w", readErr)
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return response.StatusCode, false, nil
	}
	retry := response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500
	return response.StatusCode, retry, fmt.Errorf("Webhook 返回 HTTP %d", response.StatusCode)
}

func (c WebhookClient) sleep(ctx context.Context, delay time.Duration) error {
	if c.Sleep != nil {
		return c.Sleep(ctx, delay)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-timer.C:
		return nil
	}
}

func resolveSecret(reference string) ([]byte, error) {
	if reference == "" {
		return nil, nil
	}
	if name, ok := strings.CutPrefix(reference, "env:"); ok {
		if name == "" {
			return nil, errors.New("Webhook 密钥环境变量名不能为空")
		}
		value, exists := os.LookupEnv(name)
		if !exists {
			return nil, fmt.Errorf("未设置 Webhook 密钥环境变量 %s", name)
		}
		return []byte(value), nil
	}
	path, _ := strings.CutPrefix(reference, "file:")
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("检查 Webhook 密钥: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("Webhook 密钥必须是普通文件")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("打开 Webhook 密钥: %w", err)
	}
	defer file.Close()

	// 校验实际打开的文件，再从同一句柄读取密钥。
	info, err = file.Stat()
	if err != nil {
		return nil, fmt.Errorf("检查 Webhook 密钥: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("Webhook 密钥必须是普通文件")
	}
	if err := checkSecretFilePermissions(file, info); err != nil {
		return nil, err
	}
	secret, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("读取 Webhook 密钥: %w", err)
	}
	return bytes.TrimSpace(secret), nil
}
