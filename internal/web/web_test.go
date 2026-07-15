package web

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func cia(t *testing.T, dir, name string, size int64) string {
	t.Helper()
	if size < 0x2040 {
		size = 0x2040
	}
	p := filepath.Join(dir, name)
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	h := make([]byte, 0x2040)
	binary.LittleEndian.PutUint32(h, 0x2020)
	binary.LittleEndian.PutUint64(h[24:], uint64(size-0x2040))
	if _, err = f.Write(h); err != nil {
		t.Fatal(err)
	}
	if err = f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return p
}

func TestHTTPFilesRangesAndQR(t *testing.T) {
	d := t.TempDir()
	p := cia(t, d, "sweet corn #1.cia", 0x3000)
	f := File{Name: "sweet corn #1.cia", Path: p, ID: "abc", Size: 0x3000, URL: "http://lan:8080/files/sweet%20corn%20%231.cia"}
	s, err := New([]File{f}, "http://lan:8080", "")
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	checks := []struct {
		method, path, rng string
		status            int
		length            string
	}{{"GET", "/files/sweet%20corn%20%231.cia", "", 200, "12288"}, {"HEAD", "/files/sweet%20corn%20%231.cia", "", 200, "12288"}, {"GET", "/d/abc", "", 200, "12288"}, {"HEAD", "/d/abc", "", 200, "12288"}, {"GET", "/files/sweet%20corn%20%231.cia", "bytes=10-19", 206, "10"}, {"GET", "/files/sweet%20corn%20%231.cia", "bytes=-12", 206, "12"}, {"GET", "/files/sweet%20corn%20%231.cia", "bytes=999999-", 416, ""}, {"GET", "/files/..%2Fsecret.cia", "", 404, ""}, {"GET", "/d/missing", "", 404, ""}, {"GET", "/qr/abc.png", "", 200, ""}, {"GET", "/qr/all.png", "", 200, ""}, {"GET", "/healthz", "", 200, ""}}
	for _, c := range checks {
		req, _ := http.NewRequest(c.method, ts.URL+c.path, nil)
		if c.rng != "" {
			req.Header.Set("Range", c.rng)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != c.status {
			t.Errorf("%s %s: status %d body %s", c.method, c.path, res.StatusCode, body)
		}
		if c.length != "" && res.Header.Get("Content-Length") != c.length {
			t.Errorf("length=%q want %q", res.Header.Get("Content-Length"), c.length)
		}
		if (strings.Contains(c.path, "/files/") || strings.Contains(c.path, "/d/")) && res.StatusCode < 300 && res.Header.Get("Content-Encoding") != "identity" {
			t.Error("identity encoding missing")
		}
	}
}
func TestConcurrentDownloads(t *testing.T) {
	d := t.TempDir()
	p := cia(t, d, "a.cia", 0x4000)
	s, _ := New([]File{{Name: "a.cia", Path: p, ID: "1", Size: 0x4000, URL: "http://x/files/a.cia"}}, "http://x", "")
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := http.Get(ts.URL + "/files/a.cia")
			if err != nil {
				t.Error(err)
				return
			}
			n, _ := io.Copy(io.Discard, res.Body)
			res.Body.Close()
			if n != 0x4000 {
				t.Errorf("got %d", n)
			}
		}()
	}
	wg.Wait()
	res, err := http.Get(ts.URL + "/files/a.cia")
	if err != nil {
		t.Fatal(err)
	}
	one := make([]byte, 1)
	res.Body.Read(one)
	res.Body.Close()
	if health, err := http.Get(ts.URL + "/healthz"); err != nil || health.StatusCode != 200 {
		t.Fatalf("server unhealthy after disconnect: %v", err)
	} else {
		health.Body.Close()
	}
}
func TestDiscoverAndVirtualFourGiB(t *testing.T) {
	d := t.TempDir()
	cia(t, d, "huge.cia", 1<<32)
	os.WriteFile(filepath.Join(d, "bad.cia"), []byte("bad"), 0600)
	files, err := Discover(d, "http://x", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || !files[0].Large {
		t.Fatalf("unexpected files: %+v", files)
	}
}

func TestRemoteTokenProtectsEveryRoute(t *testing.T) {
	d := t.TempDir()
	cia(t, d, "remote.cia", 0x3000)
	const token = "a-very-long-private-token"
	files, err := Discover(d, "http://203.0.113.4:28080", token)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(files[0].URL, "token=") {
		t.Fatal("download URL lacks token")
	}
	s, _ := New(files, "http://203.0.113.4:28080", token)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	for _, path := range []string{"/", "/healthz", "/files/remote.cia", "/d/" + files[0].ID, "/qr/" + files[0].ID + ".png", "/qr/all.png", "/queue"} {
		res, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s status=%d", path, res.StatusCode)
		}
	}
	res, err := http.Get(ts.URL + "/?token=" + token)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(body), "token=") || !strings.Contains(string(body), "/qr/all.png") {
		t.Fatalf("authorized page failed: %d", res.StatusCode)
	}
	if got, want := s.qrURL(files[0]), "http://203.0.113.4:28080/d/"+files[0].ID+"?token="+token; got != want {
		t.Fatalf("QR URL=%q want %q", got, want)
	}
}

func TestQueueAll(t *testing.T) {
	d := t.TempDir()
	pZ := cia(t, d, "z.cia", 0x3000)
	pA := cia(t, d, "A.cia", 0x3000)
	s, err := New([]File{
		{Name: "z.cia", Path: pZ, ID: "zed", Size: 0x3000, URL: "http://lan/files/z.cia"},
		{Name: "A.cia", Path: pA, ID: "aye", Size: 0x3000, URL: "http://lan/files/A.cia"},
	}, "http://192.168.1.10:8080", "")
	if err != nil {
		t.Fatal(err)
	}
	var gotAddr string
	var gotPayload []byte
	s.queue = func(addr string, payload []byte) error {
		gotAddr = addr
		gotPayload = append([]byte(nil), payload...)
		return nil
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	qrRes, err := http.Get(ts.URL + "/qr/all.png")
	if err != nil {
		t.Fatal(err)
	}
	qrRes.Body.Close()
	if qrRes.StatusCode != http.StatusOK || qrRes.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("queue QR status=%d type=%q", qrRes.StatusCode, qrRes.Header.Get("Content-Type"))
	}
	client := *http.DefaultClient
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	res, err := client.PostForm(ts.URL+"/queue", url.Values{"target": {"192.168.1.77"}})
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("status=%d", res.StatusCode)
	}
	if gotAddr != "192.168.1.77:5000" {
		t.Fatalf("address=%q", gotAddr)
	}
	want := "http://192.168.1.10:8080/d/aye\nhttp://192.168.1.10:8080/d/zed\n"
	if string(gotPayload) != want {
		t.Fatalf("payload=%q want %q", gotPayload, want)
	}
	if location := res.Header.Get("Location"); location != "/?queued=2" {
		t.Fatalf("location=%q", location)
	}

	res, err = http.PostForm(ts.URL+"/queue", url.Values{"target": {"203.0.113.8"}})
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("public target status=%d", res.StatusCode)
	}
}

func TestSendFBIQueueProtocol(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	want := []byte("http://192.168.1.10:8080/d/a\nhttp://192.168.1.10:8080/d/b\n")
	received := make(chan []byte, 1)
	errs := make(chan error, 1)
	allowAck := make(chan struct{})
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			errs <- err
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		var size uint32
		if err := binary.Read(conn, binary.BigEndian, &size); err != nil {
			errs <- err
			return
		}
		payload := make([]byte, size)
		if _, err := io.ReadFull(conn, payload); err != nil {
			errs <- err
			return
		}
		received <- payload
		<-allowAck
		if _, err := conn.Write([]byte{0}); err != nil {
			errs <- err
			return
		}
	}()
	sent := make(chan error, 1)
	go func() { sent <- sendFBIQueue(listener.Addr().String(), want) }()
	select {
	case err := <-errs:
		t.Fatal(err)
	case got := <-received:
		if !bytes.Equal(got, want) {
			t.Fatalf("payload=%q want %q", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for queue payload")
	}
	select {
	case err := <-sent:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("sender waited for FBI's completion acknowledgement")
	}
	close(allowAck)
}
func TestAdvertiseIP(t *testing.T) {
	ip, err := AdvertiseIP("192.168.1.5", "0.0.0.0")
	if err != nil || ip != "192.168.1.5" {
		t.Fatalf("%q %v", ip, err)
	}
	ip, err = AdvertiseIP("", "127.0.0.1")
	if err != nil || ip != "127.0.0.1" {
		t.Fatalf("%q %v", ip, err)
	}
}
