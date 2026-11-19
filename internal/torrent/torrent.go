// Package torrent reads .torrent files and drives a qBittorrent instance over
// its Web API to fetch individual files out of them, leech-only: every torrent
// it adds has a zero share ratio and is removed from qBittorrent (files
// kept) the moment its selected files finish, so the relay never seeds.
package torrent

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// File is one entry in a torrent's file list. Index is qBittorrent's file id.
type File struct {
	Index int    `json:"index"`
	Path  string `json:"path"`
	Size  int64  `json:"size"`
}

// Meta is what ROM Relay needs from a .torrent: its v1 info-hash (for the
// qBittorrent API), display name and file list.
type Meta struct {
	Hash  string
	Name  string
	Files []File
}

// Parse decodes a v1 (or hybrid) .torrent. v2-only torrents have no v1 hash
// or "files" list and are rejected.
func Parse(b []byte) (Meta, error) {
	d := decoder{b: b}
	top, err := d.value()
	if err != nil {
		return Meta{}, err
	}
	root, ok := top.(map[string]any)
	if !ok || d.infoEnd == 0 {
		return Meta{}, errors.New("not a .torrent file")
	}
	info, _ := root["info"].(map[string]any)
	name, _ := info["name"].(string)
	if name == "" {
		return Meta{}, errors.New("torrent has no name")
	}
	sum := sha1.Sum(b[d.infoStart:d.infoEnd])
	m := Meta{Hash: hex.EncodeToString(sum[:]), Name: name}
	if length, ok := info["length"].(int64); ok {
		m.Files = []File{{Index: 0, Path: name, Size: length}}
		return m, nil
	}
	list, ok := info["files"].([]any)
	if !ok {
		return Meta{}, errors.New("torrent has no v1 file list (v2-only torrents are not supported)")
	}
	for i, item := range list {
		f, _ := item.(map[string]any)
		parts, _ := f["path"].([]any)
		size, _ := f["length"].(int64)
		segs := []string{name}
		for _, p := range parts {
			if s, ok := p.(string); ok {
				segs = append(segs, s)
			}
		}
		// Padding files (BEP 47) keep their slot in qBittorrent's file ids but
		// are not worth listing.
		if attr, _ := f["attr"].(string); !strings.Contains(attr, "p") {
			m.Files = append(m.Files, File{Index: i, Path: strings.Join(segs, "/"), Size: size})
		}
	}
	return m, nil
}

// decoder is a minimal bencode reader. It records the byte span of the
// top-level "info" dict, whose SHA-1 is the torrent's info-hash.
type decoder struct {
	b                  []byte
	i                  int
	depth              int
	infoStart, infoEnd int
}

func (d *decoder) value() (any, error) {
	if d.i >= len(d.b) {
		return nil, errors.New("truncated torrent")
	}
	if d.depth > 64 {
		return nil, errors.New("torrent nests too deeply")
	}
	switch c := d.b[d.i]; {
	case c == 'i':
		end := d.index('e', d.i+1)
		if end < 0 {
			return nil, errors.New("bad integer")
		}
		n, err := strconv.ParseInt(string(d.b[d.i+1:end]), 10, 64)
		d.i = end + 1
		return n, err
	case c >= '0' && c <= '9':
		colon := d.index(':', d.i)
		if colon < 0 {
			return nil, errors.New("bad string")
		}
		n, err := strconv.Atoi(string(d.b[d.i:colon]))
		if err != nil || n < 0 || colon+1+n > len(d.b) {
			return nil, errors.New("bad string length")
		}
		d.i = colon + 1 + n
		return string(d.b[colon+1 : d.i]), nil
	case c == 'l':
		d.i++
		d.depth++
		var out []any
		for d.i < len(d.b) && d.b[d.i] != 'e' {
			v, err := d.value()
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		d.i++
		d.depth--
		return out, nil
	case c == 'd':
		d.i++
		d.depth++
		out := map[string]any{}
		for d.i < len(d.b) && d.b[d.i] != 'e' {
			k, err := d.value()
			key, ok := k.(string)
			if err != nil || !ok {
				return nil, errors.New("bad dictionary key")
			}
			start := d.i
			v, err := d.value()
			if err != nil {
				return nil, err
			}
			if key == "info" && d.depth == 1 {
				d.infoStart, d.infoEnd = start, d.i
			}
			out[key] = v
		}
		d.i++
		d.depth--
		return out, nil
	default:
		return nil, fmt.Errorf("unexpected byte %q at %d", c, d.i)
	}
}

func (d *decoder) index(c byte, from int) int {
	for j := from; j < len(d.b); j++ {
		if d.b[j] == c {
			return j
		}
	}
	return -1
}
