package config

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// iniFile holds a parsed INI document. Section and key lookups are
// case-insensitive; presence is tracked so callers can distinguish an
// explicitly configured value from an absent one (needed for options whose
// default is true).
type iniFile struct {
	// sections maps section name -> key -> value. The top-level (section-less)
	// keys live under the empty-string section.
	sections map[string]map[string]string
}

// parseINI reads a minimal INI dialect: `key = value` pairs grouped under
// `[section]` headers, with `#` or `;` line comments. Values are trimmed;
// surrounding single or double quotes are stripped so paths with trailing
// spaces can be expressed. Inline comments are not supported because ROM Relay
// values (URLs, tokens) may legitimately contain `#` and `;`.
func parseINI(r io.Reader) (*iniFile, error) {
	f := &iniFile{sections: map[string]map[string]string{"": {}}}
	section := ""
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") || strings.HasPrefix(text, ";") {
			continue
		}
		if strings.HasPrefix(text, "[") {
			if !strings.HasSuffix(text, "]") {
				return nil, fmt.Errorf("config.ini line %d: malformed section header %q", line, text)
			}
			section = strings.ToLower(strings.TrimSpace(text[1 : len(text)-1]))
			if section == "" {
				return nil, fmt.Errorf("config.ini line %d: empty section header", line)
			}
			if f.sections[section] == nil {
				f.sections[section] = map[string]string{}
			}
			continue
		}
		key, value, found := strings.Cut(text, "=")
		if !found {
			return nil, fmt.Errorf("config.ini line %d: expected key=value, got %q", line, text)
		}
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" {
			return nil, fmt.Errorf("config.ini line %d: empty key", line)
		}
		f.sections[section][key] = unquote(strings.TrimSpace(value))
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return f, nil
}

func unquote(v string) string {
	if len(v) >= 2 {
		if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
			return v[1 : len(v)-1]
		}
	}
	return v
}

// section returns the key/value map for a section (empty section is top-level).
// The returned map is never nil.
func (f *iniFile) section(name string) map[string]string {
	if s := f.sections[strings.ToLower(name)]; s != nil {
		return s
	}
	return map[string]string{}
}

// has reports whether a key was explicitly present in a section.
func (f *iniFile) has(section, key string) bool {
	s := f.sections[strings.ToLower(section)]
	if s == nil {
		return false
	}
	_, ok := s[strings.ToLower(key)]
	return ok
}
