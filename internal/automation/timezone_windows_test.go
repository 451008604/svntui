//go:build windows

package automation

import (
	"testing"
	"time"
)

func TestTimeZoneWorksWithoutGoInstallation(t *testing.T) {
	// 模拟独立安装，没有 Go SDK 或系统 IANA 数据文件。
	t.Setenv("GOROOT", t.TempDir())
	t.Setenv("ZONEINFO", t.TempDir())
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("embedded time zone unavailable: %v", err)
	}
	_, offset := time.Date(2026, 10, 18, 12, 0, 0, 0, location).Zone()
	if offset != 8*60*60 {
		t.Fatalf("unexpected Shanghai offset: %d", offset)
	}
}
