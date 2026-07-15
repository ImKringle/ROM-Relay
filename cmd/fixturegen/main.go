// fixturegen creates a tiny synthetic CCI containing no copyrighted game data.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"romrelay/internal/converter"
)

func main() {
	out := flag.String("output", "synthetic.3ds", "fixture path")
	cia := flag.String("cia", "", "optionally convert to this CIA path")
	flag.Parse()
	if err := generate(*out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if *cia != "" {
		c, _ := converter.New("")
		if err := c.Convert(context.Background(), *out, *cia, nil); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}
func generate(path string) error {
	const gameOff = 0x4000
	const gameSize = 0x5000
	b := make([]byte, gameOff+gameSize+0x800)
	copy(b[0x100:], "NCSD")
	copy(b[0x108:], []byte{8, 7, 6, 5, 4, 3, 2, 1})
	binary.LittleEndian.PutUint32(b[0x120:], gameOff/0x200)
	binary.LittleEndian.PutUint32(b[0x124:], gameSize/0x200)
	binary.LittleEndian.PutUint32(b[0x128:], (gameOff+gameSize)/0x200)
	binary.LittleEndian.PutUint32(b[0x12c:], 2)
	binary.LittleEndian.PutUint32(b[0x130:], (gameOff+gameSize+0x400)/0x200)
	binary.LittleEndian.PutUint32(b[0x134:], 2)
	for i := 0; i < 0x800; i++ {
		b[gameOff+gameSize+i] = byte(i * 3)
	}
	h := b[gameOff : gameOff+0x200]
	copy(h[0x100:], "NCCH")
	h[0x18f] = 4
	binary.LittleEndian.PutUint32(h[0x1a0:], 4)
	ext := b[gameOff+0x200 : gameOff+0x600]
	for i := range ext {
		ext[i] = byte(i * 7)
	}
	sum := sha256.Sum256(ext)
	copy(h[0x160:], sum[:])
	copy(b[gameOff+0x800:], "icon")
	icon := b[gameOff+0xa00 : gameOff+0xa00+0x36c0]
	for i := range icon {
		icon[i] = byte(i * 11)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0600)
}
