package torrent

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Managed is a qBittorrent that ROM Relay started itself, with a private
// profile, bound to 127.0.0.1 on a free port, behind a random per-launch
// password. The operator's own qBittorrent (if any) is never touched.
type Managed struct {
	Client *Client
	URL    string
	cmd    *exec.Cmd
	exited chan struct{}
}

// FindBinary resolves the qBittorrent executable: the configured path, else
// qbittorrent-nox on PATH, else (Windows, for local testing) the desktop
// install. "" means none was found.
func FindBinary(configured string) string {
	if configured != "" {
		return configured
	}
	if p, err := exec.LookPath("qbittorrent-nox"); err == nil {
		return p
	}
	if runtime.GOOS == "windows" {
		p := filepath.Join(os.Getenv("ProgramFiles"), "qBittorrent", "qbittorrent.exe")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// Launch starts qBittorrent and waits until its Web API answers.
func Launch(ctx context.Context, binary, profileDir string) (*Managed, error) {
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	pass := randomHex(24)
	if err := writeProfile(profileDir, port, "romrelay", pass); err != nil {
		return nil, fmt.Errorf("write qBittorrent profile: %w", err)
	}
	args := []string{"--profile=" + profileDir, "--webui-port=" + strconv.Itoa(port)}
	// qbittorrent-nox 5.x refuses to start without accepting the legal
	// notice on the command line; 4.x rejects the unknown flag, and reads the
	// acceptance from the profile instead.
	if major := binaryMajor(binary); major >= 5 && runtime.GOOS != "windows" {
		args = append(args, "--confirm-legal-notice")
	}
	logFile, err := os.Create(filepath.Join(profileDir, "qbittorrent.log"))
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(binary, args...)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return nil, fmt.Errorf("start %s: %w", binary, err)
	}
	m := &Managed{URL: fmt.Sprintf("http://127.0.0.1:%d", port), cmd: cmd, exited: make(chan struct{})}
	m.Client = NewClient(m.URL, "romrelay", pass)
	go func() { _ = cmd.Wait(); logFile.Close(); close(m.exited) }()

	deadline := time.After(45 * time.Second)
	for {
		probe, cancel := context.WithTimeout(ctx, 3*time.Second)
		_, err := m.Client.request(probe, "GET", "/api/v2/app/version", "", nil)
		cancel()
		if err == nil {
			return m, nil
		}
		select {
		case <-m.exited:
			return nil, fmt.Errorf("qBittorrent exited during startup; see %s", filepath.Join(profileDir, "qbittorrent.log"))
		case <-deadline:
			m.Stop()
			return nil, fmt.Errorf("qBittorrent did not answer within 45s: %w", err)
		case <-ctx.Done():
			m.Stop()
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// Stop asks qBittorrent to shut down cleanly (so it saves resume data), and
// kills it if it has not exited after 15s.
func (m *Managed) Stop() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = m.Client.form(ctx, "/api/v2/app/shutdown", nil)
	cancel()
	select {
	case <-m.exited:
	case <-time.After(15 * time.Second):
		_ = m.cmd.Process.Kill()
		<-m.exited
	}
}

// writeProfile writes the settings ROM Relay depends on. It is rewritten on
// every launch, so the port and password are always fresh.
func writeProfile(profileDir string, port int, user, pass string) error {
	dir := filepath.Join(profileDir, "qBittorrent", "config")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	hash, err := passwordHash(pass)
	if err != nil {
		return err
	}
	name := "qBittorrent.conf"
	if runtime.GOOS == "windows" {
		name = "qBittorrent.ini"
	}
	// Web UI settings live under [Preferences] (as WebUI\Key) up to
	// qBittorrent 5.2 and under [WebUI] from 5.3; writing both keeps any
	// version reading them.
	webui := []string{
		"Enabled=true",
		"Address=127.0.0.1",
		"Port=" + strconv.Itoa(port),
		"Username=" + user,
		`Password_PBKDF2="@ByteArray(` + hash + `)"`,
		"LocalHostAuth=true",
		"UseUPnP=false",
	}
	var b strings.Builder
	b.WriteString("[LegalNotice]\nAccepted=true\n\n[Preferences]\n")
	for _, kv := range webui {
		b.WriteString(`WebUI\` + kv + "\n")
	}
	b.WriteString(`General\StartMinimized=true` + "\n")
	b.WriteString(`Downloads\SavePath=` + filepath.ToSlash(filepath.Join(profileDir, "downloads")) + "\n\n[WebUI]\n")
	for _, kv := range webui {
		b.WriteString(kv + "\n")
	}
	conf := b.String()
	return os.WriteFile(filepath.Join(dir, name), []byte(conf), 0o600)
}

// passwordHash produces qBittorrent's stored Web UI password format:
// base64(salt):base64(PBKDF2-HMAC-SHA512, 100000 rounds, 64 bytes).
func passwordHash(pass string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha512.New, pass, salt, 100000, 64)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(salt) + ":" + base64.StdEncoding.EncodeToString(key), nil
}

var versionRe = regexp.MustCompile(`v(\d+)\.`)

// binaryMajor runs `<binary> --version` and returns the major version, or 0
// if it cannot tell (the desktop build on Windows opens no console for it).
func binaryMajor(binary string) int {
	if runtime.GOOS == "windows" {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, binary, "--version").Output()
	if err != nil {
		return 0
	}
	m := versionRe.FindSubmatch(out)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(string(m[1]))
	return n
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, errors.New("no free local port for qBittorrent: " + err.Error())
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
