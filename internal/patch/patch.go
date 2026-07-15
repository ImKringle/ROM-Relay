// Package patch contains bounded, filesystem-independent patch primitives.
// The orchestrator always applies these to a private copy, never to a source ROM.
package patch

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

var reserved = map[string]bool{"CON": true, "PRN": true, "AUX": true, "NUL": true, "COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true, "COM6": true, "COM7": true, "COM8": true, "COM9": true, "LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true, "LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true}

func ValidateOutputName(name, baseExtension string) (string, error) {
	if name == "" || name != filepath.Base(name) || strings.ContainsAny(name, "/\\") || strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
		return "", errors.New("output filename must be a safe basename")
	}
	stem := strings.ToUpper(strings.TrimSuffix(name, filepath.Ext(name)))
	if reserved[stem] {
		return "", errors.New("output filename is reserved on Windows")
	}
	if !strings.EqualFold(filepath.Ext(name), baseExtension) {
		return "", fmt.Errorf("output extension must be %s", baseExtension)
	}
	return name, nil
}

// IPS applies the copyright-free IPS record format in memory. Limits must be
// checked by the caller before buffers are allocated.
func IPS(source, data []byte) ([]byte, error) {
	if len(data) < 8 || !bytes.Equal(data[:5], []byte("PATCH")) {
		return nil, errors.New("invalid IPS header")
	}
	out := append([]byte(nil), source...)
	p := 5
	for {
		if p+3 > len(data) {
			return nil, errors.New("truncated IPS record")
		}
		if bytes.Equal(data[p:p+3], []byte("EOF")) {
			p += 3
			break
		}
		off := int(data[p])<<16 | int(data[p+1])<<8 | int(data[p+2])
		p += 3
		if p+2 > len(data) {
			return nil, errors.New("truncated IPS size")
		}
		size := int(binary.BigEndian.Uint16(data[p : p+2]))
		p += 2
		var chunk []byte
		if size == 0 {
			if p+3 > len(data) {
				return nil, errors.New("truncated IPS RLE")
			}
			count := int(binary.BigEndian.Uint16(data[p : p+2]))
			value := data[p+2]
			p += 3
			chunk = bytes.Repeat([]byte{value}, count)
		} else {
			if p+size > len(data) {
				return nil, errors.New("truncated IPS data")
			}
			chunk = data[p : p+size]
			p += size
		}
		end := off + len(chunk)
		if end < off {
			return nil, errors.New("IPS offset overflow")
		}
		if end > len(out) {
			out = append(out, make([]byte, end-len(out))...)
		}
		copy(out[off:end], chunk)
	}
	if len(data)-p == 3 {
		n := int(data[p])<<16 | int(data[p+1])<<8 | int(data[p+2])
		if n < len(out) {
			out = out[:n]
		} else if n > len(out) {
			out = append(out, make([]byte, n-len(out))...)
		}
	} else if p != len(data) {
		return nil, errors.New("invalid IPS trailer")
	}
	return out, nil
}
