//go:build windows

package gateway

import (
	"fmt"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// writeMask holds the access bits that let a principal change a file or add to a directory.
const writeMask = windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA | windows.FILE_WRITE_EA | windows.FILE_WRITE_ATTRIBUTES |
	windows.DELETE | windows.WRITE_DAC | windows.WRITE_OWNER | windows.GENERIC_WRITE | windows.GENERIC_ALL | 0x40

// checkNotWritableByOthers fails when Everyone, Authenticated Users or Users can write the file or its folder.
func checkNotWritableByOthers(path string) error {
	broad := map[string]windows.WELL_KNOWN_SID_TYPE{
		"Everyone":            windows.WinWorldSid,
		"Authenticated Users": windows.WinAuthenticatedUserSid,
		"Users":               windows.WinBuiltinUsersSid,
	}
	sids := map[string]*windows.SID{}
	for name, kind := range broad {
		sid, err := windows.CreateWellKnownSid(kind)
		if err != nil {
			return fmt.Errorf("cannot read the permissions of %s: %w", filepath.Base(path), err)
		}
		sids[name] = sid
	}
	for _, target := range []string{path, filepath.Dir(path)} {
		sd, err := windows.GetNamedSecurityInfo(target, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			return fmt.Errorf("cannot read the permissions of %s: %w", filepath.Base(target), err)
		}
		dacl, _, err := sd.DACL()
		if err != nil {
			return fmt.Errorf("cannot read the permissions of %s: %w", filepath.Base(target), err)
		}
		if dacl == nil {
			return fmt.Errorf("%s has no access list, so everyone can change it", target)
		}
		for i := uint32(0); i < uint32(dacl.AceCount); i++ {
			var ace *windows.ACCESS_ALLOWED_ACE
			if err := windows.GetAce(dacl, i, &ace); err != nil {
				return fmt.Errorf("cannot read the permissions of %s: %w", filepath.Base(target), err)
			}
			if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Mask&writeMask == 0 {
				continue
			}
			sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
			for name, broadSid := range sids {
				if windows.EqualSid(sid, broadSid) {
					return fmt.Errorf("%s can be changed by %s; remove that write access (icacls <path> /remove:g *%s)", target, name, broadSid.String())
				}
			}
		}
	}
	return nil
}
