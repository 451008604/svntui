package automation

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

const (
	secretFileObject        = 1
	secretOwnerSecurityInfo = 0x00000001
	secretDACLInfo          = 0x00000004
	secretACLSizeInfo       = 2
	secretAllowACE          = 0
	secretDenyACE           = 1
	secretInheritOnlyACE    = 0x08
)

var (
	secretAdvapi32          = syscall.NewLazyDLL("advapi32.dll")
	secretGetSecurityInfo   = secretAdvapi32.NewProc("GetSecurityInfo")
	secretGetACLInformation = secretAdvapi32.NewProc("GetAclInformation")
	secretGetACE            = secretAdvapi32.NewProc("GetAce")
)

type secretACLSizeInformation struct {
	ACECount   uint32
	BytesInUse uint32
	BytesFree  uint32
}

type secretACEHeader struct {
	Type  uint8
	Flags uint8
	Size  uint16
}

type secretAllowedACE struct {
	Header   secretACEHeader
	Mask     uint32
	SIDStart uint32
}

func checkSecretFilePermissions(file *os.File, _ os.FileInfo) error {
	userSID, err := secretCurrentUserSID()
	if err != nil {
		return fmt.Errorf("检查 Webhook 密钥当前用户: %w", err)
	}
	trusted := func(sid string) bool {
		return sid == userSID || sid == "S-1-5-18" || sid == "S-1-5-32-544"
	}

	// 从文件句柄读取所有者和 DACL，避免路径替换导致校验错文件。
	var owner *syscall.SID
	var dacl, descriptor unsafe.Pointer
	status, _, _ := secretGetSecurityInfo.Call(
		file.Fd(), secretFileObject, secretOwnerSecurityInfo|secretDACLInfo,
		uintptr(unsafe.Pointer(&owner)), 0, uintptr(unsafe.Pointer(&dacl)), 0,
		uintptr(unsafe.Pointer(&descriptor)),
	)
	if status != 0 {
		return fmt.Errorf("读取 Webhook 密钥 DACL: %w", syscall.Errno(status))
	}
	defer syscall.LocalFree(syscall.Handle(uintptr(descriptor)))
	if dacl == nil {
		return errors.New("Webhook 密钥文件不能使用允许任何人访问的 NULL DACL")
	}
	if owner == nil {
		return errors.New("Webhook 密钥文件缺少所有者")
	}
	ownerSID, err := owner.String()
	if err != nil {
		return fmt.Errorf("读取 Webhook 密钥所有者: %w", err)
	}
	if !trusted(ownerSID) {
		return errors.New("Webhook 密钥文件所有者必须是当前用户、SYSTEM 或 Administrators")
	}

	var info secretACLSizeInformation
	ok, _, callErr := secretGetACLInformation.Call(uintptr(dacl), uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info), secretACLSizeInfo)
	if ok == 0 {
		return fmt.Errorf("读取 Webhook 密钥 DACL 条目: %w", callErr)
	}
	// 只接受受信任主体的有效授权；复杂或未知 ACE 按失败处理。
	for i := range info.ACECount {
		var entry unsafe.Pointer
		ok, _, callErr := secretGetACE.Call(uintptr(dacl), uintptr(i), uintptr(unsafe.Pointer(&entry)))
		if ok == 0 {
			return fmt.Errorf("读取 Webhook 密钥 DACL 条目: %w", callErr)
		}
		header := (*secretACEHeader)(entry)
		if header.Flags&secretInheritOnlyACE != 0 || header.Type == secretDenyACE {
			continue
		}
		if header.Type != secretAllowACE {
			return fmt.Errorf("Webhook 密钥 DACL 包含不支持的授权类型 %d", header.Type)
		}
		if header.Size < 16 {
			return errors.New("Webhook 密钥 DACL 包含无效授权条目")
		}
		ace := (*secretAllowedACE)(entry)
		if ace.Mask == 0 {
			continue
		}
		sid, err := (*syscall.SID)(unsafe.Pointer(&ace.SIDStart)).String()
		if err != nil {
			return fmt.Errorf("读取 Webhook 密钥 DACL 用户: %w", err)
		}
		if !trusted(sid) {
			return errors.New("Webhook 密钥文件 DACL 只能允许当前用户、SYSTEM 或 Administrators 访问")
		}
	}
	return nil
}

func secretCurrentUserSID() (string, error) {
	token, err := syscall.OpenCurrentProcessToken()
	if err != nil {
		return "", err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return "", err
	}
	return user.User.Sid.String()
}
