// Package config loads the dashboard's credentials and the list of AWS
// resources it should watch.
package config

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// Credentials is an AWS access key as supplied through the .env file.
//
// The key is read from disk into memory and never written back out. The
// dashboard has no login screen that persists anything: rotating the key means
// editing .env, which is already in .gitignore.
type Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	Region          string
}

// Redacted renders the credentials for display and logging. The secret is never
// echoed, and only the tail of the key id is shown — enough to tell two keys
// apart, not enough to be worth copying out of a screenshot.
func (c Credentials) Redacted() string {
	id := c.AccessKeyID
	if len(id) > 4 {
		id = "…" + id[len(id)-4:]
	}
	return fmt.Sprintf("AccessKeyId=%s Region=%s SessionToken=%v", id, c.Region, c.SessionToken != "")
}

// Validate reports whether the credentials are complete enough to attempt a
// call. It deliberately does not check the key against AWS; that is what the
// identity endpoint is for.
func (c Credentials) Validate() error {
	var missing []string
	if c.AccessKeyID == "" {
		missing = append(missing, "AWS_ACCESS_KEY_ID")
	}
	if c.SecretAccessKey == "" {
		missing = append(missing, "AWS_SECRET_ACCESS_KEY")
	}
	if c.Region == "" {
		missing = append(missing, "AWS_REGION")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing %s; add them to your .env file", strings.Join(missing, ", "))
	}
	return nil
}

// DefaultEnvFile is the file consulted when none is named on the command line.
const DefaultEnvFile = ".env"

// LoadEnvFile parses a .env file into a map. A missing file is not an error:
// the process environment may already carry the values.
func LoadEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	defer f.Close()

	out := map[string]string{}
	sc := bufio.NewScanner(f)
	line := 0
	for sc.Scan() {
		line++
		k, v, ok := parseEnvLine(sc.Text())
		if !ok {
			continue
		}
		if k == "" {
			return nil, fmt.Errorf("%s:%d: assignment with no name", path, line)
		}
		out[k] = v
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// parseEnvLine reads one KEY=VALUE assignment, tolerating the shell-flavoured
// spellings that accumulate in these files: a leading `export`, surrounding
// quotes, inline comments after an unquoted value.
func parseEnvLine(raw string) (key, value string, ok bool) {
	s := strings.TrimSpace(raw)
	if s == "" || strings.HasPrefix(s, "#") {
		return "", "", false
	}
	s = strings.TrimPrefix(s, "export ")

	eq := strings.IndexByte(s, '=')
	if eq < 0 {
		return "", "", false
	}
	key = strings.TrimSpace(s[:eq])
	value = strings.TrimSpace(s[eq+1:])

	switch {
	case len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"':
		value = strings.NewReplacer(`\n`, "\n", `\"`, `"`, `\\`, `\`).Replace(value[1 : len(value)-1])
	case len(value) >= 2 && value[0] == '\'' && value[len(value)-1] == '\'':
		value = value[1 : len(value)-1]
	default:
		// An unquoted value ends at an unescaped comment marker.
		if i := strings.Index(value, " #"); i >= 0 {
			value = strings.TrimSpace(value[:i])
		}
	}
	return key, value, true
}

// LoadCredentials reads credentials from the .env file, falling back to the
// process environment for any value the file does not set. Values already
// present in the environment win only when the file is silent, so a .env stays
// the authoritative place to look.
func LoadCredentials(envPath string) (Credentials, error) {
	vals, err := LoadEnvFile(envPath)
	if err != nil {
		return Credentials{}, fmt.Errorf("read %s: %w", envPath, err)
	}
	get := func(key string) string {
		if v, ok := vals[key]; ok && v != "" {
			return v
		}
		return os.Getenv(key)
	}

	c := Credentials{
		AccessKeyID:     get("AWS_ACCESS_KEY_ID"),
		SecretAccessKey: get("AWS_SECRET_ACCESS_KEY"),
		SessionToken:    get("AWS_SESSION_TOKEN"),
		Region:          get("AWS_REGION"),
	}
	if c.Region == "" {
		c.Region = get("AWS_DEFAULT_REGION")
	}
	return c, nil
}
