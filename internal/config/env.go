// Package config loads the dashboard's credentials and the list of AWS
// resources it should watch.
package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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

// DefaultEnvFile is the name of the file credentials are read from.
const DefaultEnvFile = ".env"

// envFileCandidates lists where a .env is looked for, in order. It is given the
// directories rather than finding them so the ordering can be tested without a
// real executable and a real home.
//
// The working directory is deliberately absent. A shipped binary is started by
// double-clicking it, or from whatever shell happened to be open, so cwd says
// nothing about where the operator put their key — a .env sitting next to the
// binary was invisible from one directory up, which is what "the dashboard
// cannot read .env" turned out to mean. Where the file lives is a property of
// the install, not of how the process was launched.
//
// In development the credentials arrive through the process environment
// instead: mise exports the repo's own .env (mise.toml, [env] _.file) and
// LoadCredentials falls back to it.
func envFileCandidates(exeDir, homeDir string) []string {
	var out []string
	for _, dir := range []string{exeDir, homeDir} {
		if dir == "" {
			continue
		}
		p := filepath.Join(dir, DefaultEnvFile)
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

// ResolveEnvFile picks the .env to read.
//
// A path named on the command line is used as given and must exist: a typo
// there should say so rather than fall through to an empty environment, which
// is how the old silent-miss behaviour looked from the outside. Otherwise the
// first existing candidate wins; when none exists the caller gets the list back,
// because "no credentials" is only actionable together with the places checked.
//
// A missing file is not an error in the unnamed case — the process environment
// may already carry the values.
func ResolveEnvFile(named string) (path string, tried []string, err error) {
	if named != "" {
		if _, err := os.Stat(named); err != nil {
			return "", nil, err
		}
		return named, []string{named}, nil
	}

	var exeDir, homeDir string
	if exe, err := os.Executable(); err == nil {
		exeDir = filepath.Dir(exe)
	}
	if dir, err := Dir(); err == nil {
		homeDir = dir
	}

	tried = envFileCandidates(exeDir, homeDir)
	for _, p := range tried {
		if _, err := os.Stat(p); err == nil {
			return p, tried, nil
		}
	}
	return "", tried, nil
}

// LoadEnvFile parses a .env file into a map. A missing file is not an error:
// the process environment may already carry the values.
func LoadEnvFile(path string) (map[string]string, error) {
	// An empty path is what ResolveEnvFile reports when no candidate existed.
	// It means "there is no file", same as a missing one.
	if path == "" {
		return map[string]string{}, nil
	}
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
