// Package decryptor integrates an operator-supplied Batch CIA 3DS Decryptor
// Redux bundle without redistributing or exposing its unsigned third-party
// executables. Each invocation runs against private copies only.
package decryptor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"romrelay/internal/catalog"
	"romrelay/internal/converter"
)

const scriptName = "Batch CIA 3DS Decryptor Redux.bat"

type Manager struct {
	store      *catalog.Store
	driverRoot string
	library    string
	workRoot   string
	timeout    time.Duration
	one        chan struct{}
}

func New(store *catalog.Store, driverRoot, library, workRoot string, timeout time.Duration) *Manager {
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	return &Manager{store: store, driverRoot: driverRoot, library: library, workRoot: workRoot, timeout: timeout, one: make(chan struct{}, 1)}
}

func (m *Manager) Available() (bool, string) {
	for _, rel := range []string{scriptName, "bin/ctrtool.exe", "bin/decrypt.exe", "bin/makerom.exe", "bin/seeddb.bin"} {
		st, err := os.Stat(filepath.Join(m.driverRoot, filepath.FromSlash(rel)))
		if err != nil || !st.Mode().IsRegular() {
			return false, rel + " is missing"
		}
	}
	return true, ""
}

// Prepare makes an unencrypted sibling of an encrypted catalog CIA. It never
// modifies the source and publishes only after structural CIA validation.
func (m *Manager) Prepare(parent context.Context, artifactID string) (catalog.Artifact, error) {
	available, reason := m.Available()
	if !available {
		return catalog.Artifact{}, fmt.Errorf("3DS decryptor unavailable: %s", reason)
	}
	a, err := m.store.Artifact(parent, artifactID)
	if err != nil {
		return catalog.Artifact{}, err
	}
	if a.Platform != "3ds" || !strings.EqualFold(a.Extension, ".cia") || a.Encryption != "encrypted" {
		return catalog.Artifact{}, errors.New("only encrypted catalog CIA files can be prepared")
	}
	select {
	case m.one <- struct{}{}:
		defer func() { <-m.one }()
	case <-parent.Done():
		return catalog.Artifact{}, parent.Err()
	}
	ctx, cancel := context.WithTimeout(parent, m.timeout)
	defer cancel()
	stage, err := os.MkdirTemp(m.workRoot, "cia-decrypt-")
	if err != nil {
		return catalog.Artifact{}, err
	}
	defer os.RemoveAll(stage)
	if err = copyBundle(ctx, m.driverRoot, stage); err != nil {
		return catalog.Artifact{}, fmt.Errorf("stage decryptor: %w", err)
	}
	stagedSource := filepath.Join(stage, a.Name)
	if err = copyFile(ctx, a.Path, stagedSource, a.Size); err != nil {
		return catalog.Artifact{}, fmt.Errorf("stage source: %w", err)
	}
	cmd := exec.CommandContext(ctx, "cmd.exe", "/d", "/s", "/c", scriptName)
	cmd.Dir = stage
	cmd.Stdin = bytes.NewBufferString("\r\n\r\n\r\n")
	log := &limitedBuffer{limit: 256 << 10}
	cmd.Stdout, cmd.Stderr = log, log
	if err = cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return catalog.Artifact{}, fmt.Errorf("decryptor timed out or was cancelled: %w", ctx.Err())
		}
		return catalog.Artifact{}, fmt.Errorf("decryptor failed: %w (%s)", err, log.summary())
	}
	output, err := findOutput(stage, stagedSource)
	if err != nil {
		return catalog.Artifact{}, fmt.Errorf("decryptor produced no usable CIA: %w (%s)", err, log.summary())
	}
	if err = converter.Validate(output); err != nil {
		return catalog.Artifact{}, fmt.Errorf("decrypted CIA validation failed: %w", err)
	}
	// The directory is the encryption discriminator; retaining the basename also
	// groups encrypted/unencrypted variants as one catalog game.
	name := a.Name
	final := filepath.Join(m.library, "3ds", "cia", "unencrypted", name)
	if err = os.MkdirAll(filepath.Dir(final), 0755); err != nil {
		return catalog.Artifact{}, err
	}
	if err = os.Link(output, final); err != nil {
		if errors.Is(err, os.ErrExist) {
			return catalog.Artifact{}, fmt.Errorf("unencrypted output already exists: %w", os.ErrExist)
		}
		return catalog.Artifact{}, fmt.Errorf("atomic publish: %w", err)
	}
	if err = syncFile(final); err != nil {
		os.Remove(final)
		return catalog.Artifact{}, err
	}
	if err = m.store.Reconcile(ctx); err != nil {
		os.Remove(final)
		return catalog.Artifact{}, err
	}
	return m.store.ArtifactByPath(ctx, final)
}

func copyBundle(ctx context.Context, src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." || strings.HasPrefix(rel, ".git"+string(filepath.Separator)) || rel == ".git" || strings.HasPrefix(rel, "log"+string(filepath.Separator)) || rel == "log" {
			if d.IsDir() && rel != "." {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return errors.New("driver bundle contains a symlink")
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		st, err := d.Info()
		if err != nil || !st.Mode().IsRegular() {
			return errors.New("driver bundle contains a non-regular file")
		}
		return copyFile(ctx, path, target, st.Size())
	})
}

func copyFile(ctx context.Context, src, dst string, expected int64) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		out.Close()
		if !ok {
			os.Remove(dst)
		}
	}()
	written, err := io.Copy(out, &contextReader{ctx: ctx, r: io.LimitReader(in, expected+1)})
	if err != nil {
		return err
	}
	if written != expected {
		return errors.New("file changed while being copied")
	}
	if err = out.Sync(); err != nil {
		return err
	}
	if err = out.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}

func findOutput(root, source string) (string, error) {
	var candidates []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.EqualFold(path, source) {
			return err
		}
		name := strings.ToLower(d.Name())
		if strings.HasSuffix(name, "-decrypted.cia") || (strings.Contains(name, "-decrypted") && strings.HasSuffix(name, ".cia")) {
			candidates = append(candidates, path)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(candidates) != 1 {
		return "", fmt.Errorf("expected one decrypted CIA, found %d", len(candidates))
	}
	return candidates[0], nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

type limitedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if b.Len() < b.limit {
		keep := b.limit - b.Len()
		if keep > len(p) {
			keep = len(p)
		}
		_, _ = b.Buffer.Write(p[:keep])
	}
	return n, nil
}
func (b *limitedBuffer) summary() string {
	s := strings.Join(strings.Fields(b.String()), " ")
	if len(s) > 500 {
		s = s[len(s)-500:]
	}
	return s
}

func syncFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
