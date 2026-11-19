package torrent

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"testing"
)

// TestGrabSelectsOnlyOneFileOfALargeSet replays the bug where a grab from a
// 7,770-file torrent left every file selected: a fresh managed qBittorrent,
// grabbed from immediately, several times. Run it with no network so nothing
// is ever fetched:
//
//	docker run --rm --network none -v ...:/t --entrypoint /t/torrent.test \
//	  -e QBT_TEST_BINARY=/usr/bin/qbittorrent-nox -e QBT_TEST_TORRENT=/t/big.torrent \
//	  qbittorrentofficial/qbittorrent-nox:5.3.0-2 -test.run LargeSet -test.v
func TestGrabSelectsOnlyOneFileOfALargeSet(t *testing.T) {
	bin, src := os.Getenv("QBT_TEST_BINARY"), os.Getenv("QBT_TEST_TORRENT")
	if bin == "" || src == "" {
		t.Skip("QBT_TEST_BINARY / QBT_TEST_TORRENT not set")
	}
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	m, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	visible := map[int]bool{}
	for _, f := range m.Files {
		visible[f.Index] = true
	}
	ctx := context.Background()
	for round := 0; round < 5; round++ {
		qb, err := Launch(ctx, bin, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		file := m.Files[(round*1543)%len(m.Files)].Index
		if err := qb.Client.Grab(ctx, data, m, file, t.TempDir()); err != nil {
			t.Errorf("round %d: grab: %v", round, err)
		} else if ok, err := qb.Client.selectionIs(ctx, m.Hash, file, true, visible); !ok || err != nil {
			t.Errorf("round %d: more than file %d selected after start (err=%v)", round, file, err)
		}
		// A second grab from the same running torrent adds one file and must
		// leave the rest off.
		second := m.Files[(round*1543+7)%len(m.Files)].Index
		if err := qb.Client.Grab(ctx, data, m, second, t.TempDir()); err != nil {
			t.Errorf("round %d: second grab: %v", round, err)
		}
		on, _ := qb.Client.selectedCount(ctx, m.Hash)
		if on != 2 {
			t.Errorf("round %d: %d files selected after two grabs, want 2", round, on)
		}
		_ = qb.Client.form(ctx, "/api/v2/torrents/delete", url.Values{"hashes": {m.Hash}, "deleteFiles": {"true"}})
		qb.Stop()
	}
}

// selectedCount is how many of the torrent's files qBittorrent will download.
func (c *Client) selectedCount(ctx context.Context, hash string) (int, error) {
	raw, err := c.request(ctx, "GET", "/api/v2/torrents/files?hash="+hash, "", nil)
	if err != nil {
		return 0, err
	}
	var files []struct {
		Priority int `json:"priority"`
	}
	if err := json.Unmarshal([]byte(raw), &files); err != nil {
		return 0, err
	}
	n := 0
	for _, f := range files {
		if f.Priority != 0 {
			n++
		}
	}
	return n, nil
}
