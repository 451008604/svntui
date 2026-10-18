package automation

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"svntui/internal/appdirs"
	"svntui/internal/storage"
	"svntui/internal/wclock"
)

const defaultRunLimit = 500

var storeMutexes sync.Map

// Store 通过原子 JSON 替换和跨进程锁保存任务及运行记录。
type Store struct {
	Path     string
	LockDir  string
	RunLimit int
}

func NewStore(dirs appdirs.Dirs) Store {
	return Store{
		Path:     filepath.Join(dirs.State, "automations.json"),
		LockDir:  filepath.Join(dirs.State, "locks"),
		RunLimit: defaultRunLimit,
	}
}

func (s Store) Load() (State, error) {
	state := State{Version: stateVersion}
	if err := storage.LoadJSON(s.Path, &state); err != nil {
		return State{}, err
	}
	if state.Version == 0 {
		state.Version = stateVersion
	}
	if state.Version != stateVersion {
		return State{}, fmt.Errorf("不支持的自动更新状态版本: %d", state.Version)
	}
	return state, nil
}

func (s Store) Save(state State) error {
	mutex := storeMutex(s.Path)
	mutex.Lock()
	defer mutex.Unlock()
	lock, err := s.acquire("保存自动更新状态")
	if err != nil {
		return err
	}
	defer lock.Release()
	return s.saveUnlocked(state)
}

func (s Store) Update(update func(*State) error) error {
	if update == nil {
		return errors.New("自动更新状态更新函数不能为空")
	}
	mutex := storeMutex(s.Path)
	mutex.Lock()
	defer mutex.Unlock()
	lock, err := s.acquire("更新自动更新状态")
	if err != nil {
		return err
	}
	defer lock.Release()

	state, err := s.Load()
	if err != nil {
		return err
	}
	if err := update(&state); err != nil {
		return err
	}
	limit := s.RunLimit
	if limit <= 0 {
		limit = defaultRunLimit
	}
	if len(state.Runs) > limit {
		state.Runs = slices.Clone(state.Runs[len(state.Runs)-limit:])
	}
	return s.saveUnlocked(state)
}

func (s Store) UpsertTask(task Task) (Task, error) {
	if err := task.Normalize(); err != nil {
		return Task{}, err
	}
	err := s.Update(func(state *State) error {
		index := slices.IndexFunc(state.Tasks, func(item Task) bool { return item.ID == task.ID })
		if index < 0 {
			state.Tasks = append(state.Tasks, task)
		} else {
			state.Tasks[index] = task
		}
		return nil
	})
	return task, err
}

// AddTask 添加新任务；重复 ID 会返回错误。
func (s Store) AddTask(task Task) (Task, error) {
	if err := task.Normalize(); err != nil {
		return Task{}, err
	}
	err := s.Update(func(state *State) error {
		if slices.ContainsFunc(state.Tasks, func(item Task) bool { return item.ID == task.ID }) {
			return fmt.Errorf("自动更新任务 %s 已存在", task.ID)
		}
		state.Tasks = append(state.Tasks, task)
		return nil
	})
	return task, err
}

// UpdateTask 更新已有任务；不存在的 ID 不会隐式新增。
func (s Store) UpdateTask(task Task) (Task, error) {
	if err := task.Normalize(); err != nil {
		return Task{}, err
	}
	err := s.Update(func(state *State) error {
		index := slices.IndexFunc(state.Tasks, func(item Task) bool { return item.ID == task.ID })
		if index < 0 {
			return ErrTaskNotFound
		}
		state.Tasks[index] = task
		return nil
	})
	return task, err
}

func (s Store) DeleteTask(id string) error {
	return s.Update(func(state *State) error {
		before := len(state.Tasks)
		state.Tasks = slices.DeleteFunc(state.Tasks, func(task Task) bool { return task.ID == id })
		if len(state.Tasks) == before {
			return ErrTaskNotFound
		}
		return nil
	})
}

func (s Store) Task(id string) (Task, error) {
	state, err := s.Load()
	if err != nil {
		return Task{}, err
	}
	index := slices.IndexFunc(state.Tasks, func(task Task) bool { return task.ID == id })
	if index < 0 {
		return Task{}, ErrTaskNotFound
	}
	return state.Tasks[index], nil
}

func (s Store) FindTask(id string) (Task, error) { return s.Task(id) }

func (s Store) Tasks() ([]Task, error) {
	state, err := s.Load()
	if err != nil {
		return nil, err
	}
	return slices.Clone(state.Tasks), nil
}

func (s Store) ListTasks() ([]Task, error) { return s.Tasks() }

func (s Store) PauseTask(id, reason string) error {
	return s.Update(func(state *State) error {
		return updateTask(state, id, func(task *Task) {
			task.Status = StatusPaused
			task.PauseReason = reason
			task.NextRunAt = time.Time{}
		})
	})
}

func (s Store) ResumeTask(id string, next time.Time) error {
	return s.Update(func(state *State) error {
		return updateTask(state, id, func(task *Task) {
			task.Enabled = true
			task.Status = StatusScheduled
			task.PauseReason = ""
			task.ConsecutiveFailures = 0
			task.NextRunAt = next
		})
	})
}

func (s Store) UpsertWebhook(webhook WebhookConfig) (WebhookConfig, error) {
	if err := webhook.Normalize(); err != nil {
		return WebhookConfig{}, err
	}
	err := s.Update(func(state *State) error {
		index := slices.IndexFunc(state.Webhooks, func(item WebhookConfig) bool { return item.ID == webhook.ID })
		if index < 0 {
			state.Webhooks = append(state.Webhooks, webhook)
		} else {
			state.Webhooks[index] = webhook
		}
		return nil
	})
	return webhook, err
}

func (s Store) Webhook(id string) (WebhookConfig, error) {
	state, err := s.Load()
	if err != nil {
		return WebhookConfig{}, err
	}
	index := slices.IndexFunc(state.Webhooks, func(item WebhookConfig) bool { return item.ID == id })
	if index < 0 {
		return WebhookConfig{}, ErrWebhookNotFound
	}
	return state.Webhooks[index], nil
}

func (s Store) Runs(taskID string, limit int) ([]RunRecord, error) {
	state, err := s.Load()
	if err != nil {
		return nil, err
	}
	runs := make([]RunRecord, 0, min(len(state.Runs), max(limit, 0)))
	for i := len(state.Runs) - 1; i >= 0; i-- {
		if taskID == "" || state.Runs[i].TaskID == taskID {
			runs = append(runs, state.Runs[i])
			if limit > 0 && len(runs) >= limit {
				break
			}
		}
	}
	return runs, nil
}

func (s Store) saveUnlocked(state State) error {
	state.Version = stateVersion
	return storage.SaveJSON(s.Path, state)
}

func (s Store) lockDir() string {
	if s.LockDir != "" {
		return s.LockDir
	}
	return filepath.Join(filepath.Dir(s.Path), "locks")
}

func (s Store) acquire(operation string) (*wclock.Lock, error) {
	deadline := time.Now().Add(2 * time.Second)
	for {
		lock, err := wclock.Acquire(s.lockDir(), s.Path, operation)
		if err == nil || !errors.Is(err, wclock.ErrBusy) || time.Now().After(deadline) {
			return lock, err
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func storeMutex(path string) *sync.Mutex {
	value, _ := storeMutexes.LoadOrStore(filepath.Clean(path), &sync.Mutex{})
	return value.(*sync.Mutex)
}

func updateTask(state *State, id string, update func(*Task)) error {
	index := slices.IndexFunc(state.Tasks, func(task Task) bool { return task.ID == id })
	if index < 0 {
		return ErrTaskNotFound
	}
	update(&state.Tasks[index])
	return nil
}

func appendRun(state *State, record RunRecord) {
	state.Runs = append(state.Runs, record)
}

func taskWebhook(state State, task Task) (WebhookConfig, bool) {
	if task.WebhookID == "" {
		return WebhookConfig{}, false
	}
	index := slices.IndexFunc(state.Webhooks, func(item WebhookConfig) bool { return item.ID == task.WebhookID })
	if index < 0 {
		return WebhookConfig{}, false
	}
	return state.Webhooks[index], true
}

func nowUTC(now func() time.Time) time.Time {
	if now == nil {
		return time.Now().UTC()
	}
	return now().UTC()
}
