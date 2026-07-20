package ftpxfer_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"romrelay/internal/ftpxfer"
)

// TestListAndSendAgainstFakeFTPd is the one runnable check for this package:
// it drives ftpxfer.List and ftpxfer.Send against a minimal from-scratch FTP
// server (USER/PASS/FEAT/TYPE/CWD/PASV/LIST/STOR/QUIT, the exact subset
// ftpxfer's connect/List/Send code path uses) backed by a real temp
// directory, so directory listing and file upload are proven end to end
// rather than just type-checked.
func TestListAndSendAgainstFakeFTPd(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "games"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "games", "existing.nds"), []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	addr, stop := startFakeFTPd(t, root, "user", "pass")
	defer stop()

	entries, err := ftpxfer.List(context.Background(), addr, "user", "pass", "/games")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "existing.nds" || entries[0].Dir {
		t.Fatalf("unexpected entries: %+v", entries)
	}

	local := filepath.Join(t.TempDir(), "new.nds")
	if err := os.WriteFile(local, []byte("payload"), 0644); err != nil {
		t.Fatal(err)
	}
	results, err := ftpxfer.Send(context.Background(), addr, "user", "pass", "/games", []ftpxfer.SendFile{{Name: "new.nds", Path: local}})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(results) != 1 || results[0].Error != "" {
		t.Fatalf("unexpected send result: %+v", results)
	}
	got, err := os.ReadFile(filepath.Join(root, "games", "new.nds"))
	if err != nil || string(got) != "payload" {
		t.Fatalf("uploaded file missing or wrong content: %q, err=%v", got, err)
	}
}

func startFakeFTPd(t *testing.T, root, user, pass string) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
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
			go serveFakeFTPd(conn, root, user, pass)
		}
	}()
	return ln.Addr().String(), func() { ln.Close(); <-done }
}

func serveFakeFTPd(conn net.Conn, root, user, pass string) {
	defer conn.Close()
	fmt.Fprint(conn, "220 fake ftpd ready\r\n")
	cwd := root
	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		verb, arg, _ := strings.Cut(line, " ")
		switch strings.ToUpper(verb) {
		case "USER":
			fmt.Fprint(conn, "331 send password\r\n")
		case "PASS":
			if arg == pass {
				fmt.Fprint(conn, "230 logged in\r\n")
			} else {
				fmt.Fprint(conn, "530 login incorrect\r\n")
			}
		case "FEAT":
			fmt.Fprint(conn, "502 not implemented\r\n")
		case "TYPE":
			fmt.Fprint(conn, "200 type set\r\n")
		case "CWD":
			dir := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(arg, "/")))
			if st, e := os.Stat(dir); e != nil || !st.IsDir() {
				fmt.Fprint(conn, "550 no such directory\r\n")
				continue
			}
			cwd = dir
			fmt.Fprint(conn, "250 directory changed\r\n")
		case "PASV":
			dl, e := net.Listen("tcp4", "127.0.0.1:0")
			if e != nil {
				fmt.Fprint(conn, "425 cannot open data connection\r\n")
				continue
			}
			_, portStr, _ := net.SplitHostPort(dl.Addr().String())
			var port int
			fmt.Sscanf(portStr, "%d", &port)
			fmt.Fprintf(conn, "227 Entering Passive Mode (127,0,0,1,%d,%d)\r\n", port/256, port%256)
			data, e := dl.Accept()
			dl.Close()
			if e != nil {
				continue
			}
			defer data.Close()
			handleFakeData(conn, data, scanner, cwd)
			return
		case "QUIT":
			fmt.Fprint(conn, "221 bye\r\n")
			return
		default:
			fmt.Fprint(conn, "502 command not implemented\r\n")
		}
	}
}

// handleFakeData takes over once a PASV data connection is accepted, since
// the real ftpd protocol only ever needs one LIST or one STOR per data
// connection (ftpxfer never pipelines transfers), that keeps this fake
// server's control loop linear instead of needing a full state machine.
func handleFakeData(ctrl, data net.Conn, scanner *bufio.Scanner, cwd string) {
	if !scanner.Scan() {
		return
	}
	line := strings.TrimRight(scanner.Text(), "\r")
	verb, arg, _ := strings.Cut(line, " ")
	switch strings.ToUpper(verb) {
	case "LIST":
		fmt.Fprint(ctrl, "150 opening data connection\r\n")
		entries, _ := os.ReadDir(cwd)
		for _, e := range entries {
			info, _ := e.Info()
			kind := byte('-')
			if e.IsDir() {
				kind = 'd'
			}
			fmt.Fprintf(data, "%crwxr-xr-x 1 owner group %d %s %s\r\n", kind, info.Size(), time.Now().Format("Jan 02 2006"), e.Name())
		}
		data.Close()
		fmt.Fprint(ctrl, "226 transfer complete\r\n")
	case "STOR":
		fmt.Fprint(ctrl, "150 opening data connection\r\n")
		out, err := os.Create(filepath.Join(cwd, arg))
		if err != nil {
			fmt.Fprint(ctrl, "550 cannot create file\r\n")
			return
		}
		io.Copy(out, data)
		out.Close()
		fmt.Fprint(ctrl, "226 transfer complete\r\n")
	default:
		fmt.Fprint(ctrl, "502 command not implemented\r\n")
	}
}
