package operation

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"svntui/internal/svn"
	"svntui/internal/wclock"
)

// Record 保存脱敏后的 SVN 操作审计信息。
type Record struct {
	ID         string        `json:"id"`
	StartedAt  time.Time     `json:"started_at"`
	Duration   time.Duration `json:"duration"`
	Directory  string        `json:"directory,omitempty"`
	Arguments  []string      `json:"arguments"`
	ExitCode   int           `json:"exit_code"`
	ErrorKind  svn.ErrorKind `json:"error_kind,omitempty"`
	ErrorBrief string        `json:"error_brief,omitempty"`
}

type Recorder struct {
	Path       string
	MaxBytes   int64
	MaxBackups int
}

func (r Recorder) Observe(command svn.Command, result svn.Result, runErr error) {
	record := Record{
		ID:        randomID(),
		StartedAt: result.Started,
		Duration:  result.Duration,
		Directory: command.Dir,
		Arguments: redact(command.Args),
		ExitCode:  result.ExitCode,
	}
	var commandErr *svn.CommandError
	if errors.As(runErr, &commandErr) {
		record.ErrorKind = commandErr.Kind
		record.ErrorBrief = firstLine(commandErr.Error())
	}
	_ = r.Append(record)
}

const defaultLogBytes int64 = 10 << 20
const defaultLogBackups = 5
const maxRecordBytes = 1 << 20

func (r Recorder) limits() (int64, int) {
	size, count := r.MaxBytes, r.MaxBackups
	if size <= 0 {
		size = defaultLogBytes
	}
	if count <= 0 {
		count = defaultLogBackups
	}
	return size, count
}

func (r Recorder) lock() (*wclock.Lock, error) {
	deadline := time.Now().Add(time.Second)
	for {
		lock, err := wclock.Acquire(filepath.Join(filepath.Dir(r.Path), "log-locks"), r.Path, "操作日志")
		if err == nil {
			return lock, nil
		}
		if !errors.Is(err, wclock.ErrBusy) || time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (r Recorder) Append(record Record) error {
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("编码操作日志: %w", err)
	}
	if len(data) > maxRecordBytes {
		return errors.New("操作记录超过 1 MiB，已拒绝写入")
	}
	data = append(data, '\n')
	lock, err := r.lock()
	if err != nil {
		return err
	}
	defer lock.Release()
	size, backups := r.limits()
	// 追加和轮转共用进程锁，Windows 上也不会重命名正在读取的文件。
	if info, err := os.Stat(r.Path); err == nil && info.Size() > 0 && info.Size()+int64(len(data)) > size {
		oldest := fmt.Sprintf("%s.%d", r.Path, backups)
		if err := os.Remove(oldest); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		for i := backups - 1; i >= 1; i-- {
			if err := os.Rename(fmt.Sprintf("%s.%d", r.Path, i), fmt.Sprintf("%s.%d", r.Path, i+1)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		if err := os.Rename(r.Path, r.Path+".1"); err != nil {
			return err
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file, err := os.OpenFile(r.Path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	return errors.Join(writeErr, file.Close())
}

func (r Recorder) Last(limit int) ([]Record, error) {
	if limit <= 0 {
		limit = 1000
	}
	if limit > 10000 {
		return nil, errors.New("一次最多读取 10000 条操作记录")
	}
	lock, err := r.lock()
	if err != nil {
		return nil, err
	}
	defer lock.Release()
	_, backups := r.limits()
	var records []Record
	// 从最近文件开始倒序读取，只保留所需记录和至多一个固定大小数据块。
	for index := 0; index <= backups && len(records) < limit; index++ {
		path := r.Path
		if index > 0 {
			path = fmt.Sprintf("%s.%d", r.Path, index)
		}
		file, err := os.Open(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		part, readErr := readTail(file, limit-len(records))
		closeErr := file.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			return nil, err
		}
		records = append(records, part...)
	}
	return records, nil
}

func readTail(file *os.File, limit int) ([]Record, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	offset := info.Size()
	pending := []byte{}
	var result []Record
	discardLong := false
	consume := func(line []byte) {
		if len(line) == 0 || len(line) > maxRecordBytes {
			return
		}
		var record Record
		if json.Unmarshal(line, &record) == nil {
			result = append(result, record)
		}
	}
	for offset > 0 && len(result) < limit {
		count := min(offset, 64<<10)
		offset -= count
		block := make([]byte, count)
		if _, err := file.ReadAt(block, offset); err != nil {
			return nil, err
		}
		data := append(block, pending...)
		end := len(data)
		for i := len(data) - 1; i >= 0 && len(result) < limit; i-- {
			if data[i] != '\n' {
				continue
			}
			if !discardLong {
				consume(data[i+1 : end])
			}
			discardLong = false
			end = i
		}
		pending = append(pending[:0], data[:end]...)
		if len(pending) > maxRecordBytes {
			pending = nil
			discardLong = true
		}
	}
	if offset == 0 && len(result) < limit && !discardLong {
		consume(pending)
	}
	return result, nil
}

func redact(args []string) []string {
	redacted := slices.Clone(args)
	for i := 0; i < len(redacted); i++ {
		argument := strings.ToLower(redacted[i])
		if argument == "--password" || argument == "--config-option" {
			if i+1 < len(redacted) {
				redacted[i+1] = "[REDACTED]"
				i++
			}
			continue
		}
		if argument == "propset" && i+2 < len(redacted) && sensitiveName(redacted[i+1]) {
			redacted[i+2] = "[REDACTED]"
		}
		if sanitized, ok := redactURL(redacted[i]); ok {
			redacted[i] = sanitized
		}
	}
	return redacted
}

func sensitiveName(value string) bool {
	value = strings.ToLower(value)
	return containsAny(value, "password", "passwd", "token", "secret", "credential", "authorization")
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

func redactURL(value string) (string, bool) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.User == nil {
		return value, false
	}
	parsed.User = url.User("REDACTED")
	return parsed.String(), true
}

func firstLine(value string) string {
	line, _, _ := strings.Cut(value, "\n")
	if len(line) > 300 {
		return line[:300]
	}
	return line
}

func randomID() string {
	data := make([]byte, 12)
	if _, err := rand.Read(data); err != nil {
		return fmt.Sprintf("op-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(data)
}
