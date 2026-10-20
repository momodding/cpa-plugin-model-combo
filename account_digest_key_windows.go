//go:build windows

package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func readSecureAccountDigestKey(path string) ([]byte, error) {
	attributes, err := windows.GetFileAttributes(windows.StringToUTF16Ptr(path))
	if err != nil {
		return nil, fmt.Errorf("account digest key: stat: %w", err)
	}
	if attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return nil, errors.New("account digest key: file must not be a reparse point")
	}
	if attributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		return nil, errors.New("account digest key: file must be regular")
	}
	handle, err := windows.CreateFile(windows.StringToUTF16Ptr(path), windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, fmt.Errorf("account digest key: open: %w", err)
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		windows.CloseHandle(handle)
		return nil, fmt.Errorf("account digest key: stat open handle: %w", err)
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		windows.CloseHandle(handle)
		return nil, errors.New("account digest key: file must not be a reparse point")
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		windows.CloseHandle(handle)
		return nil, errors.New("account digest key: file must be regular")
	}
	if err := validateAccountDigestKeyHandle(handle); err != nil {
		windows.CloseHandle(handle)
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		windows.CloseHandle(handle)
		return nil, errors.New("account digest key: open validated handle")
	}
	defer file.Close()
	contents, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("account digest key: read: %w", err)
	}
	return contents, nil
}

func createAccountDigestKey(path string, contents []byte) error {
	sid, err := currentAccountDigestKeySID()
	if err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString("O:" + sid.String() + "G:" + sid.String() + "D:P(A;;GA;;;" + sid.String() + ")")
	if err != nil {
		return fmt.Errorf("account digest key: create security descriptor: %w", err)
	}
	attributes := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	handle, err := windows.CreateFile(windows.StringToUTF16Ptr(path), windows.GENERIC_WRITE, 0, attributes, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		if errors.Is(err, windows.ERROR_FILE_EXISTS) {
			return fs.ErrExist
		}
		return fmt.Errorf("account digest key: create: %w", err)
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		windows.CloseHandle(handle)
		return errors.New("account digest key: create handle")
	}
	defer file.Close()
	if _, err := file.Write(contents); err != nil {
		return fmt.Errorf("account digest key: write: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("account digest key: sync: %w", err)
	}
	return nil
}

func currentAccountDigestKeySID() (*windows.SID, error) {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return nil, fmt.Errorf("account digest key: current user: %w", err)
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("account digest key: current user: %w", err)
	}
	return user.User.Sid, nil
}

func validateAccountDigestKeyHandle(handle windows.Handle) error {
	currentSID, err := currentAccountDigestKeySID()
	if err != nil {
		return err
	}
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("account digest key: read security descriptor: %w", err)
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !owner.Equals(currentSID) {
		return errors.New("account digest key: file security must grant only current user full control")
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil || dacl.AceCount != 1 {
		return errors.New("account digest key: file security must grant only current user full control")
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return errors.New("account digest key: file security must use protected DACL")
	}
	ace := (*windows.ACCESS_ALLOWED_ACE)(unsafe.Add(unsafe.Pointer(dacl), 8))
	if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags&windows.INHERITED_ACE != 0 || ace.Mask != windows.GENERIC_ALL || !(*windows.SID)(unsafe.Pointer(&ace.SidStart)).Equals(owner) {
		return errors.New("account digest key: file security must grant only current user full control")
	}
	return nil
}
