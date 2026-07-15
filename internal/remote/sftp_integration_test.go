package remote_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"romrelay/internal/catalog"
	remotelib "romrelay/internal/remote"
	webui "romrelay/internal/web"
)

func TestSFTPSearchDownloadHostKeyAndExternalCatalog(t *testing.T) {
	remoteRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(remoteRoot, "gba", "nested"), 0755); err != nil {
		t.Fatal(err)
	}
	rom := make([]byte, 0x200)
	copy(rom[0xa0:], []byte("REMOTE TEST"))
	if err := os.WriteFile(filepath.Join(remoteRoot, "gba", "nested", "demo.gba"), rom, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remoteRoot, "gba", "nested", "ignore.txt"), []byte("no"), 0600); err != nil {
		t.Fatal(err)
	}
	addr, fingerprint, closeServer := startSFTPServer(t, remoteRoot)
	defer closeServer()
	d := t.TempDir()
	library := filepath.Join(d, "platforms")
	external := filepath.Join(library, "external", "antisocial-expert")
	store, err := catalog.Open(filepath.Join(d, "catalog.db"), library)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.AddExternalArtifactRoot(external)
	host, portText, _ := net.SplitHostPort(addr)
	port, _ := net.LookupPort("tcp", portText)
	opts := remotelib.Options{Host: host, Port: port, Username: "reader", Password: "secret", Root: filepath.ToSlash(remoteRoot), HostKeySHA256: fingerprint, MaxResults: 10, MaxFileBytes: 1 << 20, PlatformDirectories: map[string]string{"gba": "gba"}, LibraryRoot: library, ExternalRoot: external, Retention: 24 * time.Hour}
	bad := opts
	bad.HostKeySHA256 = "SHA256:wrong"
	if _, err = remotelib.New(store, bad).Search(context.Background(), "gba", "demo"); err == nil {
		t.Fatal("incorrect host key accepted")
	}
	m := remotelib.New(store, opts)
	results, err := m.Search(context.Background(), "gba", "demo")
	if err != nil || len(results) != 1 || results[0].Name != "demo.gba" {
		t.Fatalf("results=%+v err=%v", results, err)
	}
	job, err := m.Create(context.Background(), results[0].ID, false, "", "")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for job.Status == "queued" || job.Status == "running" {
		if time.Now().After(deadline) {
			t.Fatal("remote job timeout")
		}
		time.Sleep(10 * time.Millisecond)
		job, err = m.Get(job.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	if job.Status != "completed" || job.ArtifactID == "" || job.BytesDownloaded != int64(len(rom)) {
		t.Fatalf("job=%+v", job)
	}
	a, err := store.Artifact(context.Background(), job.ArtifactID)
	if err != nil {
		t.Fatal(err)
	}
	if !a.External || a.ExpiresAt == nil || a.Platform != "gba" {
		t.Fatalf("artifact=%+v", a)
	}
	got, err := os.ReadFile(a.Path)
	if err != nil || string(got) != string(rom) {
		t.Fatal("downloaded bytes differ")
	}
	ui := webui.NewCatalog(store, "http://relay.invalid", "", "default", "admin-token-with-24-characters!", filepath.Join(d, "patches"), 24*time.Hour)
	ts := httptest.NewServer(ui.Handler())
	defer ts.Close()
	res, err := http.Get(ts.URL + "/d/" + a.ID)
	if err != nil {
		t.Fatal(err)
	}
	served, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil || res.StatusCode != 200 || string(served) != string(rom) {
		t.Fatal("SFTP artifact was not served byte-for-byte")
	}
	res, err = http.Get(ts.URL + "/qr/" + a.ID + ".png")
	if err != nil {
		t.Fatal(err)
	}
	sig := make([]byte, 8)
	_, _ = io.ReadFull(res.Body, sig)
	res.Body.Close()
	if string(sig) != "\x89PNG\r\n\x1a\n" {
		t.Fatal("SFTP artifact QR was not a PNG")
	}
}

func startSFTPServer(t *testing.T, root string) (string, string, func()) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{PasswordCallback: func(c ssh.ConnMetadata, p []byte) (*ssh.Permissions, error) {
		if c.User() == "reader" && string(p) == "secret" {
			return nil, nil
		}
		return nil, os.ErrPermission
	}}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, e := ln.Accept()
			if e != nil {
				return
			}
			go func(c net.Conn) {
				server, chs, reqs, e := ssh.NewServerConn(c, cfg)
				if e != nil {
					c.Close()
					return
				}
				defer server.Close()
				go ssh.DiscardRequests(reqs)
				for ch := range chs {
					if ch.ChannelType() != "session" {
						_ = ch.Reject(ssh.UnknownChannelType, "session only")
						continue
					}
					channel, requests, e := ch.Accept()
					if e != nil {
						continue
					}
					go func() {
						defer channel.Close()
						for req := range requests {
							var payload struct{ Name string }
							_ = ssh.Unmarshal(req.Payload, &payload)
							if req.Type != "subsystem" || payload.Name != "sftp" {
								_ = req.Reply(false, nil)
								continue
							}
							_ = req.Reply(true, nil)
							srv, e := sftp.NewServer(channel)
							if e == nil {
								_ = srv.Serve()
								_ = srv.Close()
							}
							return
						}
					}()
				}
			}(conn)
		}
	}()
	return ln.Addr().String(), ssh.FingerprintSHA256(signer.PublicKey()), func() { _ = ln.Close(); <-done }
}
