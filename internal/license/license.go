package license

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	EnvKey   = "THIN_LICENSE_KEY"
	filename = "license.json"
)

// Record is the local, intentionally simple license record. The key is an
// opaque commercial license code issued outside the binary, for example by a
// billing provider. Validation is deliberately not performed in the request
// path.
type Record struct {
	Key         string    `json:"key"`
	ActivatedAt time.Time `json:"activated_at"`
}

type Status struct {
	Licensed    bool
	Source      string
	KeyDisplay  string
	ActivatedAt time.Time
	Path        string
}

func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "thin", filename), nil
}

func Current() Status {
	if key := strings.TrimSpace(os.Getenv(EnvKey)); key != "" {
		return Status{
			Licensed:   true,
			Source:     "environment",
			KeyDisplay: displayKey(key),
		}
	}

	path, err := DefaultPath()
	if err != nil {
		return Status{}
	}
	st := Status{Path: path}

	rec, err := Load(path)
	if err != nil {
		return st
	}
	st.Licensed = true
	st.Source = "file"
	st.KeyDisplay = displayKey(rec.Key)
	st.ActivatedAt = rec.ActivatedAt
	return st
}

func Load(path string) (Record, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Record{}, err
	}
	var rec Record
	if err := json.Unmarshal(b, &rec); err != nil {
		return Record{}, err
	}
	if strings.TrimSpace(rec.Key) == "" {
		return Record{}, errors.New("empty license key")
	}
	return rec, nil
}

func Activate(key string) (Status, error) {
	key = strings.TrimSpace(key)
	if err := validateKey(key); err != nil {
		return Status{}, err
	}
	path, err := DefaultPath()
	if err != nil {
		return Status{}, err
	}
	rec := Record{Key: key, ActivatedAt: time.Now().UTC()}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return Status{}, err
	}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return Status{}, err
	}
	b = append(b, '\n')
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return Status{}, err
	}
	return Status{
		Licensed:    true,
		Source:      "file",
		KeyDisplay:  displayKey(key),
		ActivatedAt: rec.ActivatedAt,
		Path:        path,
	}, nil
}

func Remove() (string, error) {
	path, err := DefaultPath()
	if err != nil {
		return "", err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return path, err
	}
	return path, nil
}

func validateKey(key string) error {
	if key == "" {
		return errors.New("license key is empty")
	}
	if len(key) < 8 {
		return fmt.Errorf("license key is too short")
	}
	return nil
}

func displayKey(key string) string {
	key = strings.TrimSpace(key)
	if len(key) <= 12 {
		return maskShort(key)
	}
	prefix := key[:4]
	suffix := key[len(key)-4:]
	sum := sha256.Sum256([]byte(key))
	return fmt.Sprintf("%s…%s sha256:%s", prefix, suffix, hex.EncodeToString(sum[:])[:12])
}

func maskShort(key string) string {
	if key == "" {
		return ""
	}
	if len(key) <= 4 {
		return strings.Repeat("*", len(key))
	}
	return key[:2] + strings.Repeat("*", len(key)-4) + key[len(key)-2:]
}
