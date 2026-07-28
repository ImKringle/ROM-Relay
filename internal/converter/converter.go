package converter

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
)

const mediaUnit = 0x200

//go:embed assets/certchain_retail.bin
var certchain []byte

//go:embed assets/ticket_tmd.bin
var ticketTMD []byte

type Progress func(part string, done, total int64)

type Converter struct{ originalKey []byte }

func New(boot9 string) (*Converter, error) {
	c := &Converter{}
	if boot9 == "" {
		return c, nil
	}
	b, err := os.ReadFile(boot9)
	if err != nil {
		return nil, fmt.Errorf("read boot9: %w", err)
	}
	h := sha256.Sum256(b)
	full, _ := hex.DecodeString("2f88744feed717856386400a44bba4b9ca62e76a32c715d4f309c399bf28166f")
	prot, _ := hex.DecodeString("7331f7edece3dd33f2ab4bd0b3a5d607229fd19212c10b734cedcaf78c1a7b98")
	if !equal(h[:], full) && !equal(h[:], prot) {
		return nil, fmt.Errorf("boot9 SHA-256 %x is not the recognized retail full/protected dump", h)
	}
	off := 0x59d0
	if len(b) == 0x10000 {
		off += 0x8000
	}
	if off+16 > len(b) {
		return nil, errors.New("boot9 is too short")
	}
	c.originalKey = append([]byte(nil), b[off:off+16]...)
	return c, nil
}

type partition struct{ off, size int64 }

// Convert repackages a raw .3ds/.cci cart into a CIA. It reports (via the
// second return value) whether the source content was NCCH-encrypted: the
// body payload is copied through unchanged either way, so a decrypted-format
// source (e.g. a GodMode9 "[Decrypted]" dump) yields a decrypted CIA, and an
// encrypted source yields an encrypted CIA. Callers route the output folder
// off this rather than assuming one state.
func (c *Converter) Convert(ctx context.Context, src, dst string, progress Progress) (encrypted bool, err error) {
	in, err := os.Open(src)
	if err != nil {
		return false, err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return false, err
	}
	read := func(off int64, n int) ([]byte, error) { b := make([]byte, n); _, e := in.ReadAt(b, off); return b, e }
	ncsd, err := read(0x100, 0x40)
	if err != nil || string(ncsd[:4]) != "NCSD" {
		return false, errors.New("missing NCSD magic (not a CCI)")
	}
	titleLE := ncsd[8:16]
	titleID := reverse(titleLE)
	parts := make([]partition, 3)
	for i := range parts {
		p := 0x20 + i*8
		parts[i] = partition{int64(binary.LittleEndian.Uint32(ncsd[p:p+4])) * mediaUnit, int64(binary.LittleEndian.Uint32(ncsd[p+4:p+8])) * mediaUnit}
	}
	game := parts[0]
	if game.off < 0x200 || game.size < 0x600 || game.off+game.size > st.Size() {
		return false, errors.New("invalid game partition bounds")
	}
	for _, p := range parts[1:] {
		if p.off != 0 && (p.size == 0 || p.off+p.size > st.Size()) {
			return false, errors.New("invalid auxiliary partition bounds")
		}
	}
	header, err := read(game.off, 0x200)
	if err != nil || string(header[0x100:0x104]) != "NCCH" {
		return false, errors.New("missing NCCH magic (not a retail CCI)")
	}
	flags := header[0x18f]
	encrypted = flags&4 == 0
	zero := flags&1 != 0
	var key []byte
	if encrypted {
		if zero {
			key = make([]byte, 16)
		} else {
			if len(c.originalKey) != 16 {
				return encrypted, errors.New("Original-NCCH encryption requires a validated boot9.bin or boot9_prot.bin")
			}
			key = normalKey(c.originalKey, header[:16])
		}
	}
	ext, err := read(game.off+0x200, 0x400)
	if err != nil {
		return encrypted, fmt.Errorf("read ExtHeader: %w", err)
	}
	if encrypted {
		cryptCTR(key, counter(titleID, 1), ext)
	}
	want, err := read(game.off+0x160, 32)
	if err != nil {
		return encrypted, err
	}
	got := sha256.Sum256(ext)
	if !equal(want, got[:]) {
		return encrypted, errors.New("invalid ExtHeader hash (dump is corrupt or encryption metadata is wrong)")
	}
	ext[0x0d] |= 2
	patchedHash := sha256.Sum256(ext)
	deps := append([]byte(nil), ext[0x40:0x1c0]...)
	save := append([]byte(nil), ext[0x1c0:0x1c4]...)
	if encrypted {
		cryptCTR(key, counter(titleID, 1), ext)
	}
	copy(header[0x160:0x180], patchedHash[:])
	exefsOff := int64(binary.LittleEndian.Uint32(header[0x1a0:0x1a4])) * mediaUnit
	eh, err := read(game.off+exefsOff, 0x40)
	if err != nil {
		return encrypted, errors.New("cannot read ExeFS header")
	}
	if encrypted {
		cryptCTR(key, counter(titleID, 2), eh)
	}
	var icon []byte
	for i := 0; i < 4; i++ {
		h := eh[i*16 : (i+1)*16]
		name := string(h[:8])
		for len(name) > 0 && name[len(name)-1] == 0 {
			name = name[:len(name)-1]
		}
		if name == "icon" {
			off := int64(binary.LittleEndian.Uint32(h[8:12]))
			icon, err = read(game.off+exefsOff+0x200+off, 0x36c0)
			if err != nil {
				return encrypted, errors.New("truncated ExeFS icon")
			}
			if encrypted {
				ctr := addCounter(counter(titleID, 2), uint64(off/16+0x20))
				cryptCTR(key, ctr, icon)
			}
			break
		}
	}
	if icon == nil {
		return encrypted, errors.New("icon not found in ExeFS")
	}

	contentParts := []int{0}
	count := 1
	tmdSize := 0xb34
	index := byte(0x80)
	if parts[1].off != 0 {
		contentParts = append(contentParts, 1)
		count++
		tmdSize += 0x30
		index |= 0x40
	}
	if parts[2].off != 0 {
		contentParts = append(contentParts, 2)
		count++
		tmdSize += 0x30
		index |= 0x20
	}
	records := make([]byte, count*0x30)
	total := int64(0)
	for slot, i := range contentParts {
		p := parts[i]
		binary.BigEndian.PutUint32(records[slot*0x30:], uint32(i))
		binary.BigEndian.PutUint16(records[slot*0x30+4:], uint16(i))
		binary.BigEndian.PutUint64(records[slot*0x30+8:], uint64(p.size))
		total += p.size
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return encrypted, err
	}
	ok := false
	defer func() {
		out.Close()
		if !ok {
			os.Remove(dst)
		}
	}()
	base := make([]byte, 0x2040)
	binary.LittleEndian.PutUint32(base, 0x2020)
	binary.LittleEndian.PutUint32(base[8:], 0xa00)
	binary.LittleEndian.PutUint32(base[12:], 0x350)
	binary.LittleEndian.PutUint32(base[16:], uint32(tmdSize))
	binary.LittleEndian.PutUint32(base[20:], 0x3ac0)
	binary.LittleEndian.PutUint64(base[24:], uint64(total))
	base[0x20] = index
	if _, err = out.Write(base); err != nil {
		return encrypted, err
	}
	if _, err = out.Write(certchain); err != nil {
		return encrypted, err
	}
	if _, err = out.Write(ticketTMD); err != nil {
		return encrypted, err
	}
	if _, err = out.Write(make([]byte, 0x96c)); err != nil {
		return encrypted, err
	}
	if _, err = out.Write(records); err != nil {
		return encrypted, err
	}
	if _, err = out.Write(make([]byte, 12+(count-1)*16)); err != nil {
		return encrypted, err
	}
	writeAt := func(off int64, b []byte) error { _, e := out.WriteAt(b, off); return e }
	writeAt(0x2f9f, []byte{byte(count)})
	writeAt(0x2c1c, titleID)
	writeAt(0x2f4c, titleID)
	writeAt(0x2f5a, save)
	if _, err = out.Seek(0, io.SeekEnd); err != nil {
		return encrypted, err
	}
	for slot, i := range contentParts {
		p := parts[i]
		h := sha256.New()
		var r io.Reader = io.NewSectionReader(in, p.off, p.size)
		if i == 0 {
			if _, err = out.Write(header); err != nil {
				return encrypted, err
			}
			h.Write(header)
			if _, err = out.Write(ext); err != nil {
				return encrypted, err
			}
			h.Write(ext)
			r = io.NewSectionReader(in, p.off+0x600, p.size-0x600)
		}
		done := int64(0)
		buf := make([]byte, 1024*1024)
		for {
			if err = ctx.Err(); err != nil {
				return encrypted, err
			}
			n, e := r.Read(buf)
			if n > 0 {
				if _, err = out.Write(buf[:n]); err != nil {
					return encrypted, err
				}
				h.Write(buf[:n])
				done += int64(n)
				if progress != nil {
					progress(partName(i), done, p.size)
				}
			}
			if e == io.EOF {
				break
			}
			if e != nil {
				return encrypted, e
			}
		}
		sum := h.Sum(nil)
		copy(records[slot*0x30+0x10:], sum)
		writeAt(0x38d4+int64(slot*0x30), sum)
	}
	rh := sha256.Sum256(records)
	writeAt(0x2fc7, append([]byte{byte(count)}, rh[:]...))
	info := make([]byte, 0x900)
	info[3] = byte(count)
	copy(info[4:], rh[:])
	ih := sha256.Sum256(info)
	writeAt(0x2fa4, ih[:])
	if _, err = out.Seek(0, io.SeekEnd); err != nil {
		return encrypted, err
	}
	meta := append(deps, make([]byte, 0x180)...)
	v := make([]byte, 4)
	binary.LittleEndian.PutUint32(v, 2)
	meta = append(meta, v...)
	meta = append(meta, make([]byte, 0xfc)...)
	meta = append(meta, icon...)
	if _, err = out.Write(meta); err != nil {
		return encrypted, err
	}
	if err = out.Sync(); err != nil {
		return encrypted, err
	}
	if err = out.Close(); err != nil {
		return encrypted, err
	}
	if err = Validate(dst); err != nil {
		return encrypted, err
	}
	ok = true
	return encrypted, nil
}

func Validate(path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	h := make([]byte, 0x20)
	if _, e = io.ReadFull(f, h); e != nil {
		return e
	}
	if binary.LittleEndian.Uint32(h) != 0x2020 {
		return errors.New("invalid CIA header")
	}
	st, e := f.Stat()
	if e != nil {
		return e
	}
	align := func(n uint64) uint64 { return (n + 63) &^ 63 }
	expected := align(0x2020) + align(uint64(binary.LittleEndian.Uint32(h[8:]))) + align(uint64(binary.LittleEndian.Uint32(h[12:]))) + align(uint64(binary.LittleEndian.Uint32(h[16:]))) + binary.LittleEndian.Uint64(h[24:]) + uint64(binary.LittleEndian.Uint32(h[20:]))
	if uint64(st.Size()) != expected {
		return fmt.Errorf("CIA length mismatch: got %d want %d", st.Size(), expected)
	}
	return nil
}

func partName(i int) string {
	if i == 0 {
		return "Game CXI"
	}
	if i == 1 {
		return "Manual CFA"
	}
	return "Download Play CFA"
}
func reverse(b []byte) []byte {
	r := append([]byte(nil), b...)
	for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
		r[i], r[j] = r[j], r[i]
	}
	return r
}
func equal(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var x byte
	for i := range a {
		x |= a[i] ^ b[i]
	}
	return x == 0
}
func counter(title []byte, section byte) []byte {
	b := make([]byte, 16)
	copy(b, title)
	b[8] = section
	return b
}
func addCounter(b []byte, n uint64) []byte {
	x := new(big.Int).SetBytes(b)
	x.Add(x, new(big.Int).SetUint64(n))
	r := x.FillBytes(make([]byte, 16))
	return r
}
func cryptCTR(key, ctr, b []byte) {
	block, _ := aes.NewCipher(key)
	cipher.NewCTR(block, ctr).XORKeyStream(b, b)
}
func normalKey(x, y []byte) []byte {
	xi := new(big.Int).SetBytes(x)
	yi := new(big.Int).SetBytes(y)
	mask := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 128), big.NewInt(1))
	rol := func(v *big.Int, n uint) *big.Int {
		a := new(big.Int).Lsh(v, n)
		b := new(big.Int).Rsh(v, 128-n)
		a.Or(a, b)
		return a.And(a, mask)
	}
	v := rol(xi, 2)
	v.Xor(v, yi)
	c, _ := new(big.Int).SetString("1FF9E9AAC5FE0408024591DC5D52768A", 16)
	v.Add(v, c)
	v.And(v, mask)
	v = rol(v, 87)
	return v.FillBytes(make([]byte, 16))
}
