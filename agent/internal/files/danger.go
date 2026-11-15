package files

import (
	"fmt"
	"path/filepath"
	"strings"
)

// DangerousPaths lists folders no allowed folder may overlap: where keys, startup programs, the system and the programs
// that other things run live. getenv is os.Getenv in production; a variable that is not set adds nothing.
func DangerousPaths(getenv func(string) string) []string {
	var out []string
	add := func(parts ...string) {
		if parts[0] != "" {
			out = append(out, filepath.Join(parts...))
		}
	}
	for _, name := range []string{".ssh", ".aws", ".azure", ".kube", ".gnupg", ".docker", ".config", "AppData"} {
		add(getenv("USERPROFILE"), name)
	}
	add(getenv("ProgramData"), "ssh")
	add(getenv("ProgramData"), "Microsoft", "Windows", "Start Menu")
	for _, name := range []string{"APPDATA", "LOCALAPPDATA", "SystemRoot", "ProgramFiles", "ProgramFiles(x86)"} {
		add(getenv(name))
	}
	for _, dir := range splitPathList(getenv("PATH")) {
		add(dir)
	}
	return out
}

// splitPathList splits PATH the way the host does (; on Windows, : elsewhere).
func splitPathList(v string) []string {
	if v == "" {
		return nil
	}
	sep := ":"
	if onWindows {
		sep = ";"
	}
	return strings.Split(v, sep)
}

// CheckEnvironment refuses to go on when, on Windows, a variable that names a forbidden folder is missing: without it
// that folder would silently stop being protected.
func CheckEnvironment(getenv func(string) string, windows bool) error {
	if !windows {
		return nil
	}
	for _, name := range []string{"USERPROFILE", "SystemRoot", "ProgramFiles"} {
		if getenv(name) == "" {
			return fmt.Errorf("files: the environment variable %s is not set, so the folders it names could not be protected", name)
		}
	}
	return nil
}
