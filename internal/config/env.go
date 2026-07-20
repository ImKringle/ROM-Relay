package config

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// secrets resolves sensitive values for the loader. Values already present in
// the process environment win, so a container started with `--env-file .env`
// (which Docker injects into the real environment) needs no on-disk .env file;
// a native run reads the optional .env beside config.ini as a convenience.
type secrets struct {
	file map[string]string
}

// loadSecrets reads an optional .env file from dir. A missing file is not an
// error; secrets then come entirely from the process environment.
func loadSecrets(dir string) (*secrets, error) {
	s := &secrets{file: map[string]string{}}
	f, err := os.Open(filepath.Join(dir, ".env"))
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("read .env: %w", err)
	}
	defer f.Close()
	parsed, err := parseEnvFile(f)
	if err != nil {
		return nil, err
	}
	s.file = parsed
	return s, nil
}

// get returns the value for key, preferring the process environment over the
// .env file.
func (s *secrets) get(key string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return s.file[key]
}

// parseEnvFile reads KEY=VALUE lines with `#` comments and optional surrounding
// quotes, tolerating a leading `export `.
func parseEnvFile(r io.Reader) (map[string]string, error) {
	out := map[string]string{}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		text = strings.TrimPrefix(text, "export ")
		key, value, found := strings.Cut(text, "=")
		if !found {
			return nil, fmt.Errorf(".env line %d: expected KEY=VALUE, got %q", line, text)
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, fmt.Errorf(".env line %d: empty key", line)
		}
		out[key] = unquote(strings.TrimSpace(value))
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
