package converter

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func fixture(t *testing.T, zero, manual, dlp, icon bool) string {
	t.Helper()
	gameOff, gameSize := 0x4000, 0x5000
	end := gameOff + gameSize
	if manual {
		end += 0x400
	}
	if dlp {
		end += 0x400
	}
	b := make([]byte, end)
	copy(b[0x100:], "NCSD")
	copy(b[0x108:], []byte{8, 7, 6, 5, 4, 3, 2, 1})
	binary.LittleEndian.PutUint32(b[0x120:], uint32(gameOff/mediaUnit))
	binary.LittleEndian.PutUint32(b[0x124:], uint32(gameSize/mediaUnit))
	next := gameOff + gameSize
	if manual {
		binary.LittleEndian.PutUint32(b[0x128:], uint32(next/mediaUnit))
		binary.LittleEndian.PutUint32(b[0x12c:], 2)
		for i := 0; i < 0x400; i++ {
			b[next+i] = byte(i)
		}
		next += 0x400
	}
	if dlp {
		binary.LittleEndian.PutUint32(b[0x130:], uint32(next/mediaUnit))
		binary.LittleEndian.PutUint32(b[0x134:], 2)
		for i := 0; i < 0x400; i++ {
			b[next+i] = byte(i * 3)
		}
	}
	h := b[gameOff : gameOff+0x200]
	copy(h[0x100:], "NCCH")
	binary.LittleEndian.PutUint32(h[0x1a0:], 4)
	h[0x18f] = 4
	ext := b[gameOff+0x200 : gameOff+0x600]
	for i := range ext {
		ext[i] = byte(i * 7)
	}
	sum := sha256.Sum256(ext)
	copy(h[0x160:], sum[:])
	eh := b[gameOff+0x800 : gameOff+0x840]
	if icon {
		copy(eh, "icon")
		iconBytes := b[gameOff+0xa00 : gameOff+0xa00+0x36c0]
		for i := range iconBytes {
			iconBytes[i] = byte(i * 11)
		}
	}
	if zero {
		h[0x18f] = 1
		title := []byte{1, 2, 3, 4, 5, 6, 7, 8}
		cryptCTR(make([]byte, 16), counter(title, 1), ext)
		cryptCTR(make([]byte, 16), counter(title, 2), eh)
		if icon {
			cryptCTR(make([]byte, 16), addCounter(counter(title, 2), 0x20), b[gameOff+0xa00:gameOff+0xa00+0x36c0])
		}
	}
	p := filepath.Join(t.TempDir(), "fixture.3ds")
	if err := os.WriteFile(p, b, 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSyntheticGoldenDeterministic(t *testing.T) {
	src := fixture(t, false, true, true, true)
	c, _ := New("")
	var hashes [2][32]byte
	for i := range 2 {
		dst := filepath.Join(t.TempDir(), "out.cia")
		if _, err := c.Convert(context.Background(), src, dst, nil); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(dst)
		hashes[i] = sha256.Sum256(b)
		if err := Validate(dst); err != nil {
			t.Fatal(err)
		}
	}
	if hashes[0] != hashes[1] {
		t.Fatal("output is not deterministic")
	}
	const golden = "0e2dbff4488c7a0ae52761bf9bddc0e7c3a1c008101bdc630fa84846aa316901"
	if hex.EncodeToString(hashes[0][:]) != golden {
		t.Fatalf("golden changed: %x", hashes[0])
	}
}

func TestNormalKeyVector(t *testing.T) {
	x := make([]byte, 16)
	y := make([]byte, 16)
	for i := range x {
		x[i] = byte(i)
		y[i] = byte(i + 16)
	}
	if got := hex.EncodeToString(normalKey(x, y)); got != "09c4bdce56980781e4e4ff89099d4162" {
		t.Fatalf("key=%s", got)
	}
}
func TestZeroKeyEncrypted(t *testing.T) {
	src := fixture(t, true, false, false, true)
	dst := filepath.Join(t.TempDir(), "out.cia")
	c, _ := New("")
	if _, err := c.Convert(context.Background(), src, dst, nil); err != nil {
		t.Fatal(err)
	}
}

func TestOriginalKeyEncrypted(t *testing.T) {
	p := fixture(t, false, false, false, true)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	const gameOff = 0x4000
	x := make([]byte, 16)
	for i := range x {
		x[i] = byte(i + 32)
		b[gameOff+i] = byte(i + 64)
	}
	key := normalKey(x, b[gameOff:gameOff+16])
	b[gameOff+0x18f] = 0
	title := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	cryptCTR(key, counter(title, 1), b[gameOff+0x200:gameOff+0x600])
	cryptCTR(key, counter(title, 2), b[gameOff+0x800:gameOff+0x840])
	cryptCTR(key, addCounter(counter(title, 2), 0x20), b[gameOff+0xa00:gameOff+0xa00+0x36c0])
	if err = os.WriteFile(p, b, 0600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "original.cia")
	if _, err = (&Converter{originalKey: x}).Convert(context.Background(), p, dst, nil); err != nil {
		t.Fatal(err)
	}
}

func TestRejectsInvalidBoot9(t *testing.T) {
	p := filepath.Join(t.TempDir(), "boot9.bin")
	os.WriteFile(p, make([]byte, 0x10000), 0600)
	if _, err := New(p); err == nil {
		t.Fatal("invalid boot9 accepted")
	}
}
func TestFailuresAndCollision(t *testing.T) {
	c, _ := New("")
	tests := []struct {
		name string
		mut  func(string)
	}{{"bad magic", func(p string) { f, _ := os.OpenFile(p, os.O_WRONLY, 0); f.WriteAt([]byte("NOPE"), 0x100); f.Close() }}, {"bad hash", func(p string) { f, _ := os.OpenFile(p, os.O_WRONLY, 0); f.WriteAt([]byte{9}, 0x4160); f.Close() }}, {"missing icon", func(p string) { f, _ := os.OpenFile(p, os.O_WRONLY, 0); f.WriteAt(make([]byte, 8), 0x4800); f.Close() }}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := fixture(t, false, false, false, true)
			tt.mut(p)
			if _, err := c.Convert(context.Background(), p, filepath.Join(t.TempDir(), "o.cia"), nil); err == nil {
				t.Fatal("wanted error")
			}
		})
	}
	p := fixture(t, false, false, false, true)
	dst := filepath.Join(t.TempDir(), "exists.cia")
	os.WriteFile(dst, []byte("mine"), 0600)
	if _, err := c.Convert(context.Background(), p, dst, nil); err == nil {
		t.Fatal("collision accepted")
	}
	b, _ := os.ReadFile(dst)
	if string(b) != "mine" {
		t.Fatal("existing file changed")
	}
}
func TestCancellationRemovesPartial(t *testing.T) {
	p := fixture(t, false, false, false, true)
	dst := filepath.Join(t.TempDir(), "partial.cia")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c, _ := New("")
	_, err := c.Convert(ctx, p, dst, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(dst); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("partial output remains")
	}
}
