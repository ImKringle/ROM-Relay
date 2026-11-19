package torrent

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"testing"
)

// TestGrabAgainstLiveQBittorrent drives a real qBittorrent Web API, e.g.:
//
//	docker run -d -p 127.0.0.1:18080:8080 -e QBT_LEGAL_NOTICE=confirm qbittorrentofficial/qbittorrent-nox:5.3.0-2
//	QBT_TEST_URL=http://127.0.0.1:18080 QBT_TEST_USER=... QBT_TEST_PASS=... go test ./internal/torrent -run Live
//
// It checks the parts that only a real server can: the add/filePrio/start
// call shapes, that just the chosen file is enabled, and the no-seed limits.
func TestGrabAgainstLiveQBittorrent(t *testing.T) {
	base := os.Getenv("QBT_TEST_URL")
	if base == "" {
		t.Skip("QBT_TEST_URL not set")
	}
	c := NewClient(base, os.Getenv("QBT_TEST_USER"), os.Getenv("QBT_TEST_PASS"))
	ctx := context.Background()
	info := "d5:filesl" +
		"d6:lengthi1024e4:pathl3:GBA14:Test Quest.gbaee" +
		"d6:lengthi2048e4:pathl3:Zip12:Test Set.zipee" +
		"e4:name7:TestSet12:piece lengthi16384e6:pieces20:" + strings.Repeat("\x00", 20) + "e"
	data := []byte("d8:announce22:http://127.0.0.1:1/ann4:info" + info + "e")
	m, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	defer c.form(ctx, "/api/v2/torrents/delete", url.Values{"hashes": {m.Hash}, "deleteFiles": {"true"}})

	if err := c.Grab(ctx, data, m, 1, "/tmp/romrelay-test"); err != nil {
		t.Fatalf("grab: %v", err)
	}
	raw, err := c.request(ctx, "GET", "/api/v2/torrents/files?hash="+m.Hash, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	var files []struct {
		Index    int `json:"index"`
		Priority int `json:"priority"`
	}
	if err := json.Unmarshal([]byte(raw), &files); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if want := map[int]int{0: 0, 1: 1}[f.Index]; f.Priority != want {
			t.Errorf("file %d priority = %d, want %d (only the grabbed file downloads)", f.Index, f.Priority, want)
		}
	}
	raw, err = c.request(ctx, "GET", "/api/v2/torrents/info?hashes="+m.Hash, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	var props []struct {
		Category string  `json:"category"`
		RatioLim float64 `json:"ratio_limit"`
		SavePath string  `json:"save_path"`
	}
	if err := json.Unmarshal([]byte(raw), &props); err != nil || len(props) != 1 {
		t.Fatalf("info: %v %s", err, raw)
	}
	p := props[0]
	if p.Category != Category || p.RatioLim != 0 || !strings.HasPrefix(p.SavePath, "/tmp/romrelay-test") {
		t.Errorf("leech settings not applied: %+v", p)
	}
	_, active, err := c.Sweep(ctx)
	if err != nil || len(active) == 0 {
		t.Fatalf("sweep must see the unfinished grab as active: %v %v", active, err)
	}
	// A second grab of another file from the same torrent switches it on.
	if err := c.Grab(ctx, data, m, 0, "/tmp/romrelay-test"); err != nil {
		t.Fatalf("second grab: %v", err)
	}
}
