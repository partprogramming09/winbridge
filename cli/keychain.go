package main

import (
	"errors"
	"strings"
)

// keychainAccount is the account name under which every host key is stored.
const keychainAccount = "winbridge"

// Secrets reads and writes the auth key of a host.
type Secrets interface {
	Get(service string) (string, error)
	Set(service, key string) error
}

// macKeychain stores keys in the macOS Keychain through the `security` tool.
type macKeychain struct{ run Runner }

// Get returns the key stored for the service.
func (k macKeychain) Get(service string) (string, error) {
	out, err := k.run.Run("security", []string{"find-generic-password", "-a", keychainAccount, "-s", service, "-w"}, "")
	if err != nil {
		return "", errors.New("no key in the Keychain for " + service + "; run `winbridge adopt`")
	}
	return strings.TrimSpace(out), nil
}

// Set stores the key. `security` takes it as an argument, so it is visible in the process list for a moment.
func (k macKeychain) Set(service, key string) error {
	_, err := k.run.Run("security", []string{"add-generic-password", "-a", keychainAccount, "-s", service, "-w", key, "-U"}, "")
	return err
}
