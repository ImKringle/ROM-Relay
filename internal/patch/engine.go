package patch

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/dop251/goja"
)

//go:embed engine/*.js
var engineFiles embed.FS

var engineOrder = []string{"BinFile.js", "HashCalculator.js", "RomPatcher.format.ips.js", "RomPatcher.format.aps_n64.js", "RomPatcher.format.aps_gba.js", "RomPatcher.format.ups.js", "RomPatcher.format.bps.js", "RomPatcher.format.rup.js", "RomPatcher.format.ppf.js", "RomPatcher.format.bdf.js", "RomPatcher.format.pmsr.js", "RomPatcher.format.vcdiff.js", "RomPatcher.js"}

type ApplyResult struct {
	Output                                 []byte
	Format                                 string
	HasSourceChecksum, SourceChecksumMatch bool
	ExpectedChecksum, ActualChecksum       string
}
type PatchInspection struct{ Format, ValidationType, ValidationValue string }

func newRuntime() (*goja.Runtime, error) {
	rt := goja.New()
	rt.Set("console", map[string]any{"log": func(...any) {}})
	for _, name := range engineOrder {
		code, err := engineFiles.ReadFile("engine/" + name)
		if err != nil {
			return nil, err
		}
		if _, err = rt.RunString(string(code)); err != nil {
			return nil, fmt.Errorf("load embedded %s: %w", name, err)
		}
	}
	return rt, nil
}

func InspectEmbedded(ctx context.Context, patch []byte, patchName string) (PatchInspection, error) {
	rt, err := newRuntime()
	if err != nil {
		return PatchInspection{}, err
	}
	fnValue, err := rt.RunString(`(function(patchBuffer,patchName){var pf=new BinFile(new Uint8Array(patchBuffer));pf.fileName=patchName;var p=RomPatcher.parsePatchFile(pf);if(!p)throw new Error('Invalid or unsupported patch file');var i=typeof p.getValidationInfo==='function'?p.getValidationInfo():null;return {type:i&&i.type?String(i.type):'',value:i&&i.value!==undefined?String(i.value):i&&i.checksum!==undefined?String(i.checksum):'',raw:i?JSON.stringify(i):''};})`)
	if err != nil {
		return PatchInspection{}, err
	}
	fn, ok := goja.AssertFunction(fnValue)
	if !ok {
		return PatchInspection{}, errors.New("embedded patch inspector is not callable")
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			rt.Interrupt(ctx.Err())
		case <-done:
		}
	}()
	v, err := fn(goja.Undefined(), rt.ToValue(rt.NewArrayBuffer(patch)), rt.ToValue(patchName))
	if err != nil {
		if ctx.Err() != nil {
			return PatchInspection{}, ctx.Err()
		}
		return PatchInspection{}, fmt.Errorf("inspect %s: %w", filepath.Ext(patchName), err)
	}
	o := v.ToObject(rt)
	value := o.Get("value").String()
	if value == "" {
		value = o.Get("raw").String()
	}
	return PatchInspection{Format: strings.TrimPrefix(strings.ToLower(filepath.Ext(patchName)), "."), ValidationType: strings.ToUpper(o.Get("type").String()), ValidationValue: strings.ToLower(value)}, nil
}

// ApplyEmbedded runs the pinned RomPatcher.js engine in a fresh pure-Go VM.
// Only byte buffers and names enter the VM; require, filesystem, process and
// network objects are never installed.
func ApplyEmbedded(ctx context.Context, source, patch []byte, sourceName, patchName string) (ApplyResult, error) {
	return ApplyEmbeddedProgress(ctx, source, patch, sourceName, patchName, nil)
}

// ApplyEmbeddedProgress uses native engines for the common IPS, UPS, and BPS
// formats and retains the isolated JavaScript fallback for the remaining
// supported formats.
func ApplyEmbeddedProgress(ctx context.Context, source, patch []byte, sourceName, patchName string, progress ApplyProgress) (ApplyResult, error) {
	format := strings.TrimPrefix(strings.ToLower(filepath.Ext(patchName)), ".")
	if result, handled, err := applyNative(ctx, source, patch, format, progress); handled {
		return result, err
	}
	rt, err := newRuntime()
	if err != nil {
		return ApplyResult{}, err
	}
	fnValue, err := rt.RunString(`(function(sourceBuffer,patchBuffer,sourceName,patchName){var rom=new BinFile(new Uint8Array(sourceBuffer));rom.fileName=sourceName;var pf=new BinFile(new Uint8Array(patchBuffer));pf.fileName=patchName;var p=RomPatcher.parsePatchFile(pf);if(!p)throw new Error('Invalid or unsupported patch file');var info=typeof p.getValidationInfo==='function'?p.getValidationInfo():null;var valid=info?RomPatcher.validateRom(rom,p):true;var actual='';if(info&&info.type==='CRC32')actual=rom.hashCRC32().toString(16).padStart(8,'0');else if(info&&info.type==='MD5')actual=rom.hashMD5();else if(info)actual=String(RomPatcher.getRomAdditionalChecksum(rom)||'unavailable');var out=RomPatcher.applyPatch(rom,p,{requireValidation:false,removeHeader:false,addHeader:false,fixChecksum:false,outputSuffix:false});return {output:out._u8array.buffer,hasChecksum:!!info,valid:valid,expected:info?JSON.stringify(info):'',actual:actual};})`)
	if err != nil {
		return ApplyResult{}, err
	}
	fn, ok := goja.AssertFunction(fnValue)
	if !ok {
		return ApplyResult{}, errors.New("embedded patch entry point is not callable")
	}
	interrupted := make(chan struct{})
	defer close(interrupted)
	go func() {
		select {
		case <-ctx.Done():
			rt.Interrupt(ctx.Err())
		case <-interrupted:
		}
	}()
	value, err := fn(goja.Undefined(), rt.ToValue(rt.NewArrayBuffer(source)), rt.ToValue(rt.NewArrayBuffer(patch)), rt.ToValue(sourceName), rt.ToValue(patchName))
	if err != nil {
		if ctx.Err() != nil {
			return ApplyResult{}, ctx.Err()
		}
		return ApplyResult{}, fmt.Errorf("apply %s: %w", filepath.Ext(patchName), err)
	}
	obj := value.ToObject(rt)
	buffer, ok := obj.Get("output").Export().(goja.ArrayBuffer)
	if !ok {
		return ApplyResult{}, errors.New("embedded engine returned an invalid output buffer")
	}
	result := ApplyResult{Output: append([]byte(nil), buffer.Bytes()...), Format: format, HasSourceChecksum: obj.Get("hasChecksum").ToBoolean(), SourceChecksumMatch: obj.Get("valid").ToBoolean(), ExpectedChecksum: obj.Get("expected").String(), ActualChecksum: obj.Get("actual").String()}
	if progress != nil {
		progress(int64(len(result.Output)), int64(len(result.Output)))
	}
	return result, nil
}
