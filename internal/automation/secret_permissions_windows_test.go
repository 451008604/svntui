package automation

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

func TestResolveSecretWindowsDACL(t *testing.T) {
	userSID, err := secretCurrentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		aces string
		deny bool
	}{
		{name: "current_user_only"},
		{name: "system_and_administrators", aces: "(A;;FA;;;SY)(A;;FA;;;BA)"},
		{name: "everyone_read", aces: "(A;;FR;;;WD)", deny: true},
		{name: "authenticated_users_read", aces: "(A;;FR;;;AU)", deny: true},
		{name: "everyone_write", aces: "(A;;FW;;;WD)", deny: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "webhook.secret")
			if err := os.WriteFile(path, []byte("  secret\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			// 仅替换本用例文件的 DACL，并关闭继承以隔离宿主权限。
			setSecretTestDACL(t, path, fmt.Sprintf("D:P(A;;FA;;;%s)%s", userSID, test.aces))
			secret, err := resolveSecret("file:" + path)
			if test.deny {
				if err == nil || !strings.Contains(err.Error(), "DACL") || len(secret) != 0 {
					t.Fatalf("insecure DACL accepted: secret=%q err=%v", secret, err)
				}
				return
			}
			if err != nil || string(secret) != "secret" {
				t.Fatalf("secure DACL rejected: secret=%q err=%v", secret, err)
			}
		})
	}
}

func TestResolveSecretWindowsNullDACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "webhook.secret")
	if err := os.WriteFile(path, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	setSecretTestDACL(t, path, "D:NO_ACCESS_CONTROL")
	secret, err := resolveSecret("file:" + path)
	if err == nil || !strings.Contains(err.Error(), "NULL DACL") || len(secret) != 0 {
		t.Fatalf("NULL DACL accepted: secret=%q err=%v", secret, err)
	}
}

func setSecretTestDACL(t *testing.T, path, sddl string) {
	t.Helper()
	convert := secretAdvapi32.NewProc("ConvertStringSecurityDescriptorToSecurityDescriptorW")
	getDACL := secretAdvapi32.NewProc("GetSecurityDescriptorDacl")
	setSecurityInfo := secretAdvapi32.NewProc("SetNamedSecurityInfoW")
	text, err := syscall.UTF16PtrFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	var descriptor unsafe.Pointer
	ok, _, callErr := convert.Call(uintptr(unsafe.Pointer(text)), 1, uintptr(unsafe.Pointer(&descriptor)), 0)
	if ok == 0 {
		t.Fatalf("create test DACL: %v", callErr)
	}
	defer syscall.LocalFree(syscall.Handle(uintptr(descriptor)))
	var present, defaulted uint32
	var dacl unsafe.Pointer
	ok, _, callErr = getDACL.Call(uintptr(descriptor), uintptr(unsafe.Pointer(&present)), uintptr(unsafe.Pointer(&dacl)), uintptr(unsafe.Pointer(&defaulted)))
	if ok == 0 || present == 0 {
		t.Fatalf("read test DACL: present=%d err=%v", present, callErr)
	}
	filename, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	const protectedDACLInfo = 0x80000000
	status, _, _ := setSecurityInfo.Call(uintptr(unsafe.Pointer(filename)), secretFileObject, secretDACLInfo|protectedDACLInfo, 0, 0, uintptr(dacl), 0)
	if status != 0 {
		t.Fatalf("set test DACL: %v", syscall.Errno(status))
	}
}
