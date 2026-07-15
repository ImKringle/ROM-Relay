package patch

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
)

// ApplyProgress reports output bytes produced by the native patch engines.
type ApplyProgress func(done, total int64)

func applyNative(ctx context.Context, source, patch []byte, format string, progress ApplyProgress) (ApplyResult, bool, error) {
	switch format {
	case "ips":
		out, err := IPS(source, patch)
		if progress != nil && err == nil {
			progress(int64(len(out)), int64(len(out)))
		}
		return ApplyResult{Output: out, Format: format}, true, err
	case "ups":
		out, expected, actual, match, err := applyUPS(ctx, source, patch, progress)
		return ApplyResult{Output: out, Format: format, HasSourceChecksum: true, SourceChecksumMatch: match, ExpectedChecksum: expected, ActualChecksum: actual}, true, err
	case "bps":
		out, expected, actual, match, err := applyBPS(ctx, source, patch, progress)
		return ApplyResult{Output: out, Format: format, HasSourceChecksum: true, SourceChecksumMatch: match, ExpectedChecksum: expected, ActualChecksum: actual}, true, err
	default:
		return ApplyResult{}, false, nil
	}
}

func patchVLV(data []byte, pos *int, limit int) (uint64, error) {
	var value, shift uint64 = 0, 1
	for {
		if *pos >= limit || shift > 1<<56 {
			return 0, errors.New("truncated or overflowing patch variable integer")
		}
		x := data[*pos]
		(*pos)++
		value += uint64(x&0x7f) * shift
		if x&0x80 != 0 {
			return value, nil
		}
		shift <<= 7
		value += shift
	}
}

func checksumDetails(source []byte, expected uint32) (string, string, bool) {
	actual := crc32.ChecksumIEEE(source)
	return fmt.Sprintf("CRC32 %08x", expected), fmt.Sprintf("%08x", actual), actual == expected
}

func verifyPatchCRC(patch []byte) error {
	if len(patch) < 4 {
		return errors.New("patch is truncated")
	}
	want := binary.LittleEndian.Uint32(patch[len(patch)-4:])
	if got := crc32.ChecksumIEEE(patch[:len(patch)-4]); got != want {
		return fmt.Errorf("patch checksum mismatch: got %08x, want %08x", got, want)
	}
	return nil
}

func applyUPS(ctx context.Context, source, patch []byte, progress ApplyProgress) ([]byte, string, string, bool, error) {
	if len(patch) < 16 || !bytes.Equal(patch[:4], []byte("UPS1")) {
		return nil, "", "", false, errors.New("invalid UPS patch")
	}
	if err := verifyPatchCRC(patch); err != nil {
		return nil, "", "", false, err
	}
	pos, end := 4, len(patch)-12
	inputSize, err := patchVLV(patch, &pos, end)
	if err != nil {
		return nil, "", "", false, err
	}
	outputSize, err := patchVLV(patch, &pos, end)
	if err != nil || outputSize > uint64(^uint(0)>>1) {
		return nil, "", "", false, errors.New("invalid UPS output size")
	}
	expected := binary.LittleEndian.Uint32(patch[len(patch)-12:])
	expectedText, actual, match := checksumDetails(source, expected)
	// Preserve RomPatcher.js compatibility for a larger user-supplied source.
	if uint64(len(source)) > inputSize && outputSize < uint64(len(source)) {
		outputSize = uint64(len(source))
		inputSize = uint64(len(source))
	}
	out := make([]byte, int(outputSize))
	copy(out, source[:min(len(source), int(min(inputSize, outputSize)))])
	var offset uint64
	for pos < end {
		if err := ctx.Err(); err != nil {
			return nil, expectedText, actual, match, err
		}
		relative, e := patchVLV(patch, &pos, end)
		if e != nil || relative > ^uint64(0)-offset {
			return nil, expectedText, actual, match, errors.New("invalid UPS record offset")
		}
		offset += relative
		for {
			if pos >= end {
				return nil, expectedText, actual, match, errors.New("truncated UPS record")
			}
			x := patch[pos]
			pos++
			if x == 0 {
				break
			}
			if offset >= uint64(len(out)) {
				return nil, expectedText, actual, match, errors.New("UPS record exceeds output size")
			}
			var original byte
			if offset < uint64(len(source)) {
				original = source[offset]
			}
			out[offset] = original ^ x
			offset++
		}
		offset++
		if progress != nil {
			progress(int64(min(offset, uint64(len(out)))), int64(len(out)))
		}
	}
	if want := binary.LittleEndian.Uint32(patch[len(patch)-8:]); crc32.ChecksumIEEE(out) != want {
		return nil, expectedText, actual, match, errors.New("UPS target checksum mismatch")
	}
	if progress != nil {
		progress(int64(len(out)), int64(len(out)))
	}
	return out, expectedText, actual, match, nil
}

func applyBPS(ctx context.Context, source, patch []byte, progress ApplyProgress) ([]byte, string, string, bool, error) {
	if len(patch) < 16 || !bytes.Equal(patch[:4], []byte("BPS1")) {
		return nil, "", "", false, errors.New("invalid BPS patch")
	}
	if err := verifyPatchCRC(patch); err != nil {
		return nil, "", "", false, err
	}
	pos, end := 4, len(patch)-12
	sourceSize, err := patchVLV(patch, &pos, end)
	if err != nil {
		return nil, "", "", false, err
	}
	targetSize, err := patchVLV(patch, &pos, end)
	if err != nil || targetSize > uint64(^uint(0)>>1) {
		return nil, "", "", false, errors.New("invalid BPS target size")
	}
	metadataSize, err := patchVLV(patch, &pos, end)
	if err != nil || metadataSize > uint64(end-pos) {
		return nil, "", "", false, errors.New("invalid BPS metadata size")
	}
	pos += int(metadataSize)
	expected := binary.LittleEndian.Uint32(patch[len(patch)-12:])
	expectedText, actual, match := checksumDetails(source, expected)
	if sourceSize != uint64(len(source)) {
		match = false
	}
	out := make([]byte, int(targetSize))
	var outputOffset, sourceRelative, targetRelative int64
	for pos < end {
		if err := ctx.Err(); err != nil {
			return nil, expectedText, actual, match, err
		}
		encoded, e := patchVLV(patch, &pos, end)
		if e != nil {
			return nil, expectedText, actual, match, e
		}
		length := int64(encoded>>2) + 1
		if length <= 0 || outputOffset > int64(len(out))-length {
			return nil, expectedText, actual, match, errors.New("BPS action exceeds output size")
		}
		switch encoded & 3 {
		case 0: // SourceRead uses the current output position.
			if outputOffset > int64(len(source))-length {
				return nil, expectedText, actual, match, errors.New("BPS source-read exceeds source size")
			}
			copy(out[outputOffset:outputOffset+length], source[outputOffset:outputOffset+length])
		case 1: // TargetRead stores literal bytes in the patch.
			if length > int64(end-pos) {
				return nil, expectedText, actual, match, errors.New("truncated BPS target-read")
			}
			copy(out[outputOffset:outputOffset+length], patch[pos:pos+int(length)])
			pos += int(length)
		case 2, 3:
			relative, e := patchVLV(patch, &pos, end)
			if e != nil || relative > uint64(^uint64(0)>>1) {
				return nil, expectedText, actual, match, errors.New("invalid BPS relative offset")
			}
			delta := int64(relative >> 1)
			if relative&1 != 0 {
				delta = -delta
			}
			if encoded&3 == 2 {
				sourceRelative += delta
				if sourceRelative < 0 || sourceRelative > int64(len(source))-length {
					return nil, expectedText, actual, match, errors.New("BPS source-copy exceeds source size")
				}
				copy(out[outputOffset:outputOffset+length], source[sourceRelative:sourceRelative+length])
				sourceRelative += length
			} else {
				targetRelative += delta
				if targetRelative < 0 || targetRelative >= outputOffset {
					return nil, expectedText, actual, match, errors.New("BPS target-copy has invalid offset")
				}
				for i := int64(0); i < length; i++ {
					src := targetRelative + i
					if src < 0 || src >= outputOffset+i {
						return nil, expectedText, actual, match, errors.New("BPS target-copy exceeds produced output")
					}
					out[outputOffset+i] = out[src]
				}
				targetRelative += length
			}
		}
		outputOffset += length
		if progress != nil {
			progress(outputOffset, int64(len(out)))
		}
	}
	if outputOffset != int64(len(out)) {
		return nil, expectedText, actual, match, errors.New("BPS output is incomplete")
	}
	if want := binary.LittleEndian.Uint32(patch[len(patch)-8:]); crc32.ChecksumIEEE(out) != want {
		return nil, expectedText, actual, match, errors.New("BPS target checksum mismatch")
	}
	return out, expectedText, actual, match, nil
}
