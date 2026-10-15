package store

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Locked lists group videos behind a shared long secret. A video with an
// empty AccessList stays public; otherwise only holders of the list key
// (plus editors/administrators, for management) may view it. The plaintext
// key is shown once at creation/rotation — only its SHA-256 is persisted.
type AccessList struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Prefix    string `json:"prefix"`
	KeyHash   string `json:"key_hash,omitempty"`
	CreatedAt string `json:"created_at"`
}

var validAccessListID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ValidAccessListID keeps list identifiers safe for metadata and filenames.
func ValidAccessListID(id string) bool { return validAccessListID.MatchString(id) }

// ValidAccessListName bounds the operator-chosen display name.
func ValidAccessListName(name string) bool {
	name = strings.TrimSpace(name)
	return name != "" && len(name) <= 100
}

// listKeyTokenPrefix marks locked-list capability tokens. The body is 32
// base64url characters (24 random bytes); the leading 12 double as the
// public Prefix, mirroring API keys.
const listKeyTokenPrefix = "lt-"

var validListKeyBody = regexp.MustCompile(`^[A-Za-z0-9_-]{32}$`)

// GenerateAccessListID mints an unguessable list identifier.
func GenerateAccessListID() (string, error) {
	var entropy [12]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(entropy[:]), nil
}

// GenerateAccessKey mints a plaintext list key. It returns the raw token
// (shown once), its public prefix and the SHA-256 hash to persist.
func GenerateAccessKey() (raw, prefix, hash string, err error) {
	var entropy [24]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", "", "", err
	}
	body := base64.RawURLEncoding.EncodeToString(entropy[:])
	raw = listKeyTokenPrefix + body
	return raw, body[:12], HashAccessKey(raw), nil
}

// HashAccessKey digests a plaintext list key for storage.
func HashAccessKey(raw string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(raw)))
	return hex.EncodeToString(sum[:])
}

// SplitAccessKey extracts the public prefix from a presented token.
func SplitAccessKey(raw string) (string, bool) {
	body, ok := strings.CutPrefix(strings.TrimSpace(raw), listKeyTokenPrefix)
	if !ok || !validListKeyBody.MatchString(body) {
		return "", false
	}
	return body[:12], true
}

// AccessListStore persists locked lists. Both backends agree: missing
// records return os.ErrNotExist, invalid names return os.ErrInvalid.
type AccessListStore interface {
	GetAccessList(id string) (AccessList, error)
	GetAccessListByPrefix(prefix string) (AccessList, error)
	ListAccessLists() ([]AccessList, error)
	PutAccessList(l AccessList) error
	DeleteAccessList(id string) error
}

// FS access lists live under Root/.auth/access_lists, alongside the other
// private records that media serving never exposes.
func (s *FS) accessListsDir() string { return filepath.Join(s.authDir(), "access_lists") }

func (s *FS) accessListPath(id string) string {
	return filepath.Join(s.accessListsDir(), id+".json")
}

func (s *FS) GetAccessList(id string) (AccessList, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var l AccessList
	if !ValidAccessListID(id) {
		return l, os.ErrInvalid
	}
	b, err := os.ReadFile(s.accessListPath(id))
	if err != nil {
		return l, err
	}
	if err := json.Unmarshal(b, &l); err != nil {
		return l, err
	}
	if l.ID != id {
		return AccessList{}, os.ErrNotExist
	}
	return l, nil
}

func (s *FS) GetAccessListByPrefix(prefix string) (AccessList, error) {
	lists, err := s.ListAccessLists()
	if err != nil {
		return AccessList{}, err
	}
	for _, l := range lists {
		if l.Prefix == prefix {
			return l, nil
		}
	}
	return AccessList{}, os.ErrNotExist
}

func (s *FS) ListAccessLists() ([]AccessList, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entries, err := os.ReadDir(s.accessListsDir())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []AccessList
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".json")
		if !ValidAccessListID(name) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.accessListsDir(), e.Name()))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		var l AccessList
		if err := json.Unmarshal(b, &l); err != nil {
			return nil, err
		}
		if l.ID != name {
			continue
		}
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *FS) PutAccessList(l AccessList) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !ValidAccessListID(l.ID) || !ValidAccessListName(l.Name) ||
		!ValidKeyPrefix(l.Prefix) || !validTokenHash(l.KeyHash) {
		return os.ErrInvalid
	}
	l.Name = strings.TrimSpace(l.Name)
	if l.CreatedAt == "" {
		l.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	return s.writeJSONFile(s.accessListPath(l.ID), l)
}

func (s *FS) DeleteAccessList(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !ValidAccessListID(id) {
		return os.ErrInvalid
	}
	err := os.Remove(s.accessListPath(id))
	if os.IsNotExist(err) {
		return os.ErrNotExist
	}
	return err
}
