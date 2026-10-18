package automation

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"
	// 独立安装的 Windows 程序也能加载 IANA 时区。
	_ "time/tzdata"
)

const (
	defaultInterval = 15 * time.Minute
	stateVersion    = 1
)

var (
	ErrTaskNotFound    = errors.New("找不到自动更新任务")
	ErrWebhookNotFound = errors.New("找不到 Webhook")
)

// Duration 以易读字符串持久化 time.Duration。
type Duration time.Duration

func (d Duration) Duration() time.Duration { return time.Duration(d) }

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

func (d *Duration) UnmarshalJSON(data []byte) error {
	if d == nil {
		return errors.New("无法将时长解析到空接收者")
	}
	if bytes.Equal(data, []byte("null")) {
		*d = 0
		return nil
	}
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		value, err := time.ParseDuration(text)
		if err != nil {
			return fmt.Errorf("解析时长 %q: %w", text, err)
		}
		*d = Duration(value)
		return nil
	}
	var nanos int64
	if err := json.Unmarshal(data, &nanos); err != nil {
		return fmt.Errorf("解码时长: %w", err)
	}
	*d = Duration(nanos)
	return nil
}

type TaskStatus string

const (
	StatusDisabled  TaskStatus = "disabled"
	StatusScheduled TaskStatus = "scheduled"
	StatusRunning   TaskStatus = "running"
	StatusDeferred  TaskStatus = "deferred"
	StatusSkipped   TaskStatus = "skipped"
	StatusSucceeded TaskStatus = "succeeded"
	StatusFailed    TaskStatus = "failed"
	StatusPaused    TaskStatus = "paused"
)

type RunOutcome string

const (
	OutcomeChanged  RunOutcome = "changed"
	OutcomeNoChange RunOutcome = "no_change"
)

type Trigger string

const (
	TriggerScheduled Trigger = "scheduled"
	TriggerManual    Trigger = "manual"
)

type EventType string

const (
	EventUpdateSucceeded EventType = "update.succeeded"
	EventUpdateNoChange  EventType = "update.no_change"
	EventUpdateSkipped   EventType = "update.skipped"
	EventUpdateFailed    EventType = "update.failed"
	EventUpdatePaused    EventType = "update.paused"
	EventUpdateResumed   EventType = "update.resumed"
)

// ActiveWindow 使用本地时间的 HH:MM 表达允许执行的时段，跨午夜时段受开始日约束。
type ActiveWindow struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

func (w ActiveWindow) Validate() error {
	if _, err := parseClock(w.Start); err != nil {
		return fmt.Errorf("无效的活跃时段开始时间: %w", err)
	}
	if _, err := parseClock(w.End); err != nil {
		return fmt.Errorf("无效的活跃时段结束时间: %w", err)
	}
	return nil
}

// Schedule 限定自动任务可运行的星期和时段；空值表示始终允许。
type Schedule struct {
	Weekdays []time.Weekday `json:"weekdays,omitempty"`
	Windows  []ActiveWindow `json:"windows,omitempty"`
}

func (s Schedule) Validate() error {
	for _, day := range s.Weekdays {
		if day < time.Sunday || day > time.Saturday {
			return fmt.Errorf("无效的星期值: %d", day)
		}
	}
	for _, window := range s.Windows {
		if err := window.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func (s Schedule) Allows(at time.Time) bool {
	if len(s.Windows) == 0 {
		return s.allowsDay(at.Weekday())
	}
	minute := at.Hour()*60 + at.Minute()
	for _, window := range s.Windows {
		start, startErr := parseClock(window.Start)
		end, endErr := parseClock(window.End)
		if startErr != nil || endErr != nil {
			continue
		}
		switch {
		case start == end && s.allowsDay(at.Weekday()):
			return true
		case start < end && minute >= start && minute < end && s.allowsDay(at.Weekday()):
			return true
		case start > end && minute >= start && s.allowsDay(at.Weekday()):
			return true
		case start > end && minute < end && s.allowsDay(previousWeekday(at.Weekday())):
			return true
		}
	}
	return false
}

// NextAllowed 返回不早于 at 的下一个可运行分钟。
func (s Schedule) NextAllowed(at time.Time) time.Time {
	if s.Allows(at) {
		return at
	}
	candidate := at.Truncate(time.Minute).Add(time.Minute)
	for range 8 * 24 * 60 {
		if s.Allows(candidate) {
			return candidate
		}
		candidate = candidate.Add(time.Minute)
	}
	return at.Add(7 * 24 * time.Hour)
}

func (s Schedule) allowsDay(day time.Weekday) bool {
	if len(s.Weekdays) == 0 {
		return true
	}
	for _, allowed := range s.Weekdays {
		if allowed == day {
			return true
		}
	}
	return false
}

func previousWeekday(day time.Weekday) time.Weekday {
	return (day + 6) % 7
}

func parseClock(value string) (int, error) {
	parsed, err := time.Parse("15:04", value)
	if err != nil {
		return 0, err
	}
	return parsed.Hour()*60 + parsed.Minute(), nil
}

type NotificationPolicy struct {
	Succeeded bool `json:"succeeded,omitzero"`
	NoChange  bool `json:"no_change,omitzero"`
}

// Task 描述一个绑定到确定仓库身份和分支 URL 的自动更新任务。
type Task struct {
	ID                  string             `json:"id"`
	Name                string             `json:"name"`
	Enabled             bool               `json:"enabled"`
	Status              TaskStatus         `json:"status"`
	WorkingCopy         string             `json:"working_copy"`
	RepositoryUUID      string             `json:"repository_uuid"`
	RootURL             string             `json:"root_url"`
	BranchURL           string             `json:"branch_url"`
	Interval            Duration           `json:"interval"`
	TimeZone            string             `json:"time_zone,omitempty"`
	Schedule            Schedule           `json:"schedule,omitzero"`
	UpdateExternals     bool               `json:"update_externals,omitzero"`
	AllowSwitched       bool               `json:"allow_switched,omitzero"`
	WebhookID           string             `json:"webhook_id,omitempty"`
	Notifications       NotificationPolicy `json:"notifications,omitzero"`
	LastRunAt           time.Time          `json:"last_run_at,omitzero"`
	LastResult          TaskStatus         `json:"last_result,omitempty"`
	LastOutcome         RunOutcome         `json:"last_outcome,omitempty"`
	LastRevision        int64              `json:"last_revision,omitzero"`
	NextRunAt           time.Time          `json:"next_run_at,omitzero"`
	ConsecutiveFailures int                `json:"consecutive_failures,omitzero"`
	PauseReason         string             `json:"pause_reason,omitempty"`
}

func (t *Task) Normalize() error {
	if t == nil {
		return errors.New("自动更新任务不能为空")
	}
	if t.ID == "" {
		t.ID = newID("task")
	}
	if strings.TrimSpace(t.Name) == "" {
		return errors.New("必须填写自动更新任务名称")
	}
	if strings.TrimSpace(t.WorkingCopy) == "" {
		return errors.New("必须填写工作副本路径")
	}
	absolute, err := filepath.Abs(t.WorkingCopy)
	if err != nil {
		return fmt.Errorf("解析工作副本路径: %w", err)
	}
	t.WorkingCopy = filepath.Clean(absolute)
	if t.RepositoryUUID == "" || t.RootURL == "" || t.BranchURL == "" {
		return errors.New("必须填写仓库 UUID、根 URL 和分支 URL")
	}
	if err := validateRepositoryURL(t.RootURL); err != nil {
		return fmt.Errorf("无效的仓库根 URL: %w", err)
	}
	if err := validateRepositoryURL(t.BranchURL); err != nil {
		return fmt.Errorf("无效的分支 URL: %w", err)
	}
	if t.Interval == 0 {
		t.Interval = Duration(defaultInterval)
	}
	if t.Interval.Duration() <= 0 {
		return errors.New("自动更新间隔必须大于零")
	}
	if err := t.Schedule.Validate(); err != nil {
		return err
	}
	if t.TimeZone != "" {
		if _, err := time.LoadLocation(t.TimeZone); err != nil {
			return fmt.Errorf("无效的自动更新时区: %w", err)
		}
	}
	if !t.Enabled {
		t.Status = StatusDisabled
	} else if t.Status == "" || t.Status == StatusDisabled {
		t.Status = StatusScheduled
	}
	return nil
}

func (t Task) location() *time.Location {
	if t.TimeZone == "" {
		return time.Local
	}
	location, err := time.LoadLocation(t.TimeZone)
	if err != nil {
		return time.Local
	}
	return location
}

func validateRepositoryURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil {
		return err
	}
	if parsed.Scheme == "" {
		return errors.New("URL 必须包含协议")
	}
	return nil
}

type Change struct {
	Action string `json:"action"`
	Path   string `json:"path"`
}

type WebhookDelivery struct {
	EventID    string    `json:"event_id"`
	Attempts   int       `json:"attempts"`
	StatusCode int       `json:"status_code,omitzero"`
	Delivered  bool      `json:"delivered"`
	Error      string    `json:"error,omitempty"`
	Finished   time.Time `json:"finished"`
}

// RunRecord 保存一次调度判断或更新的结果。
type RunRecord struct {
	ID             string           `json:"id"`
	TaskID         string           `json:"task_id"`
	Trigger        Trigger          `json:"trigger"`
	Status         TaskStatus       `json:"status"`
	Outcome        RunOutcome       `json:"outcome,omitempty"`
	Reason         string           `json:"reason,omitempty"`
	ErrorKind      string           `json:"error_kind,omitempty"`
	StartedAt      time.Time        `json:"started_at"`
	FinishedAt     time.Time        `json:"finished_at"`
	BeforeRevision int64            `json:"before_revision,omitzero"`
	AfterRevision  int64            `json:"after_revision,omitzero"`
	Changes        []Change         `json:"changes,omitempty"`
	Webhook        *WebhookDelivery `json:"webhook,omitempty"`
}

type WebhookConfig struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers,omitempty"`
	SecretRef string            `json:"secret_ref,omitempty"`
}

func (w *WebhookConfig) Normalize() error {
	if w == nil {
		return errors.New("Webhook 配置不能为空")
	}
	if w.ID == "" {
		w.ID = newID("webhook")
	}
	if strings.TrimSpace(w.Name) == "" {
		return errors.New("必须填写 Webhook 名称")
	}
	parsed, err := url.Parse(w.URL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return errors.New("Webhook URL 必须使用 http 或 https")
	}
	return nil
}

type State struct {
	Version  int             `json:"version"`
	Tasks    []Task          `json:"tasks,omitempty"`
	Webhooks []WebhookConfig `json:"webhooks,omitempty"`
	Runs     []RunRecord     `json:"runs,omitempty"`
}

func newID(prefix string) string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	return prefix + "-" + hex.EncodeToString(value[:])
}
