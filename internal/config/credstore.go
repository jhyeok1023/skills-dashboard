package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// CredentialsPath is the file a key saved from the settings page lands in. It
// sits beside config.json rather than inside it: a secret does not belong in a
// file the settings page round-trips wholesale, and keeping it separate is also
// what keeps it out of config.json.bak when a bad config is moved aside.
func CredentialsPath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "credentials.json"), nil
}

// CredentialStore holds the key saved from the settings page.
//
// Until this existed the dashboard read .env and wrote nothing, which meant
// rotating a key was an edit plus a restart. Saving one is now possible, and
// the cost is real: the key is on disk in plain text, mode 0600, in the user's
// home directory. The file is the only copy — nothing is echoed into the log
// beyond the last four characters of the key id (see Credentials.Redacted).
//
// A saved key wins over .env. The settings page is the more recent, more
// explicit statement of which account to use, and the page says which of the
// two is in force so the precedence is never something to guess at.
type CredentialStore struct {
	mu    sync.RWMutex
	creds Credentials
	saved bool
	path  string

	// notice is what loading the file had to overlook, fixed at construction
	// so it needs no lock.
	notice string
}

// LoadCredentialStore reads the saved key at path. A missing file is the
// ordinary case — it means nothing has been saved yet.
//
// An unreadable or unparseable file is not fatal either. The dashboard falls
// back to .env and says so on the settings page; a process that exits over a
// corrupt credentials file serves no page on which to fix it.
func LoadCredentialStore(path string) *CredentialStore {
	s := &CredentialStore{path: path}
	b, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			s.notice = fmt.Sprintf("저장된 자격증명을 읽을 수 없어 무시했습니다 (%v).", err)
		}
		return s
	}
	var creds Credentials
	if err := json.Unmarshal(b, &creds); err != nil {
		s.notice = fmt.Sprintf("저장된 자격증명이 깨져 있어 무시했습니다 (%v).", err)
		return s
	}
	if creds.Validate() != nil {
		// Half a key is worse than none: it would take precedence over a .env
		// that works and fail every call with a puzzle.
		s.notice = "저장된 자격증명이 불완전해 무시했습니다."
		return s
	}
	s.creds, s.saved = creds, true
	return s
}

// Notice is what loading the file had to overlook, empty when there was
// nothing to say.
func (s *CredentialStore) Notice() string { return s.notice }

// Get returns the saved key and whether there is one.
func (s *CredentialStore) Get() (Credentials, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.creds, s.saved
}

// Set validates and persists creds.
//
// Validate only checks that the fields are filled in. Whether the key works is
// decided against STS before this is called — a key that cannot reach AWS is
// never written, so a saved file always means a key that authenticated at least
// once.
func (s *CredentialStore) Set(creds Credentials) error {
	if err := creds.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	s.creds, s.saved = creds, true
	path := s.path
	s.mu.Unlock()

	if path == "" {
		return nil
	}
	b, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(b, '\n'))
}

// Clear forgets the saved key and removes the file, which is what returns the
// dashboard to reading .env. Removing a file that is not there is success.
func (s *CredentialStore) Clear() error {
	s.mu.Lock()
	s.creds, s.saved = Credentials{}, false
	path := s.path
	s.mu.Unlock()

	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
