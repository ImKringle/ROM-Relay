package torrent

import (
	"crypto/sha1"
	"encoding/hex"
	"testing"
)

func TestParseMultiFileWithPadding(t *testing.T) {
	info := "d5:filesl" +
		"d6:lengthi10e4:pathl3:gba11:Emerald.gbaee" +
		"d4:attr1:p6:lengthi6e4:pathl4:.pad1:6ee" +
		"d6:lengthi20e4:pathl3:nds9:Black.ndsee" +
		"e4:name3:Sete"
	raw := "d8:announce3:url4:info" + info + "e"
	m, err := Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha1.Sum([]byte(info))
	if m.Hash != hex.EncodeToString(sum[:]) || m.Name != "Set" {
		t.Fatalf("hash/name wrong: %+v", m)
	}
	want := []File{{0, "Set/gba/Emerald.gba", 10}, {2, "Set/nds/Black.nds", 20}}
	if len(m.Files) != 2 || m.Files[0] != want[0] || m.Files[1] != want[1] {
		t.Fatalf("files = %+v, want %+v (padding skipped, ids kept)", m.Files, want)
	}
	if _, err := Parse([]byte("not a torrent")); err == nil {
		t.Fatal("garbage must not parse")
	}
}
