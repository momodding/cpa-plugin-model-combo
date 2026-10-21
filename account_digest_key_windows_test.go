//go:build windows

package main

import (
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestAccountDigestKeyCreatedACLDiagnostic(t *testing.T) {
	dataPath := filepath.Join(t.TempDir(), "usage.db")
	if _, err := loadOrCreateAccountDigestKey(dataPath); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(filepath.Dir(dataPath), accountDigestKeyFile)

	h, err := windows.CreateFile(
		windows.StringToUTF16Ptr(path),
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)

	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	control, _, err := sd.Control()
	if err != nil {
		t.Fatal(err)
	}
	currentSID, err := currentAccountDigestKeySID()
	if err != nil {
		t.Fatal(err)
	}

	aceCount := uint32(0)
	if dacl != nil {
		aceCount = dacl.AceCount
	}
	ownerMatches := owner != nil && owner.Equals(currentSID)
	daclProtected := control&windows.SE_DACL_PROTECTED != 0
	t.Logf("owner_match_current_sid=%t dacl_protected=%t ace_count=%d", ownerMatches, daclProtected, aceCount)
	if !ownerMatches {
		t.Fatal("owner SID does not equal current account SID")
	}
	if !daclProtected {
		t.Fatal("DACL is not protected")
	}
	if aceCount != 1 {
		t.Fatalf("expected exactly one ACE, got %d", aceCount)
	}

	ace := (*windows.ACCESS_ALLOWED_ACE)(unsafe.Add(unsafe.Pointer(dacl), 8))
	fileAllAccess := accountDigestKeyFileAllAccess
	aceSID := (*windows.SID)(unsafe.Add(unsafe.Pointer(ace), unsafe.Offsetof(ace.SidStart)))
	t.Logf("ace_type=%d ace_flags=%d access_mask=0x%08x ace_sid_matches_owner=%t mask_equals_generic_all=%t mask_equals_file_all_access=%t exact_file_all_access=0x%08x", ace.Header.AceType, ace.Header.AceFlags, ace.Mask, aceSID.Equals(owner), ace.Mask == windows.GENERIC_ALL, ace.Mask == fileAllAccess, fileAllAccess)
	if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
		t.Fatalf("expected ACCESS_ALLOWED_ACE_TYPE, got %d", ace.Header.AceType)
	}
	if ace.Header.AceFlags != 0 {
		t.Fatalf("expected AceFlags == 0, got 0x%02x", ace.Header.AceFlags)
	}
	if ace.Mask != windows.GENERIC_ALL && ace.Mask != fileAllAccess {
		t.Fatalf("expected mask GENERIC_ALL or FILE_ALL_ACCESS, got 0x%08x", ace.Mask)
	}
	if !aceSID.Equals(owner) {
		t.Fatal("ACE SID does not equal owner SID")
	}
}
