// Package ftpxfer talks plain FTP to a homebrew server (e.g. ftpd) running on
// a 3DS/NDS the user already owns, so ROM Relay can browse its filesystem and
// copy files the user already has in their library onto the console. It
// never searches for or fetches new content, only moves bytes the caller
// already selected.
package ftpxfer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"sort"
	"time"

	"github.com/jlaffaye/ftp"
)

// Entry is one file or directory in a listing.
type Entry struct {
	Name string `json:"name"`
	Dir  bool   `json:"dir"`
	Size int64  `json:"size,omitempty"`
}

// SendFile is one local file to upload, by its already-resolved path.
type SendFile struct {
	Name string
	Path string
}

// SendResult reports the outcome of one file in a Send call.
type SendResult struct {
	Name  string `json:"name"`
	Error string `json:"error,omitempty"`
}

const dialTimeout = 8 * time.Second

// List connects, logs in, and lists dir (an absolute FTP path; "" means the
// server's login directory). Anonymous login is used when user is empty, as
// ftpd and most homebrew FTP servers default to.
func List(ctx context.Context, addr, user, pass, dir string) ([]Entry, error) {
	conn, err := connect(ctx, addr, user, pass)
	if err != nil {
		return nil, err
	}
	defer conn.Quit()
	if dir != "" {
		if err := conn.ChangeDir(dir); err != nil {
			return nil, fmt.Errorf("open %q: %w", dir, err)
		}
	}
	raw, err := conn.List("")
	if err != nil {
		return nil, fmt.Errorf("list %q: %w", dir, err)
	}
	entries := make([]Entry, 0, len(raw))
	for _, e := range raw {
		if e.Name == "." || e.Name == ".." {
			continue
		}
		entries = append(entries, Entry{Name: e.Name, Dir: e.Type == ftp.EntryTypeFolder, Size: int64(e.Size)})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Dir != entries[j].Dir {
			return entries[i].Dir
		}
		return entries[i].Name < entries[j].Name
	})
	return entries, nil
}

// Send connects once and uploads every file in files into dir (an absolute
// FTP path), stopping at the first connection-level failure but recording a
// per-file error for anything that fails mid-transfer so one bad file doesn't
// hide the rest.
func Send(ctx context.Context, addr, user, pass, dir string, files []SendFile) ([]SendResult, error) {
	if len(files) == 0 {
		return nil, errors.New("no files selected")
	}
	conn, err := connect(ctx, addr, user, pass)
	if err != nil {
		return nil, err
	}
	defer conn.Quit()
	if dir != "" {
		if err := conn.ChangeDir(dir); err != nil {
			return nil, fmt.Errorf("open %q: %w", dir, err)
		}
	}
	results := make([]SendResult, 0, len(files))
	for _, f := range files {
		results = append(results, sendOne(conn, f))
	}
	return results, nil
}

func sendOne(conn *ftp.ServerConn, f SendFile) SendResult {
	src, err := os.Open(f.Path)
	if err != nil {
		return SendResult{Name: f.Name, Error: "local file unavailable"}
	}
	defer src.Close()
	if err := conn.Stor(path.Base(f.Name), src); err != nil {
		return SendResult{Name: f.Name, Error: "upload failed"}
	}
	return SendResult{Name: f.Name}
}

func connect(ctx context.Context, addr, user, pass string) (*ftp.ServerConn, error) {
	// EPSV is disabled: it isn't universally implemented across homebrew FTP
	// servers, while PASV is (ftpd included), so PASV is the safer default here.
	conn, err := ftp.Dial(addr, ftp.DialWithContext(ctx), ftp.DialWithTimeout(dialTimeout), ftp.DialWithDisabledEPSV(true))
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", addr, err)
	}
	if user == "" {
		user = "anonymous"
	}
	if err := conn.Login(user, pass); err != nil {
		conn.Quit()
		return nil, fmt.Errorf("login to %s: %w", addr, err)
	}
	return conn, nil
}
