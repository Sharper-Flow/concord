package store

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
)

type nativeIsolationCacheEntry struct {
	Path   string
	HWCaps uint64
}

// glibc 2.39 sysdeps/generic/dl-cache.h defines the 48-byte new-format
// header and 24-byte entries. Other formats/architectures refuse. The cache
// is a host-launcher input only and never a workload mount.
func nativeIsolationLoaderCache(raw []byte) (map[string][]nativeIsolationCacheEntry, error) {
	if len(raw) < 48 || len(raw) > 8*1024*1024 || !bytes.HasPrefix(raw, []byte("glibc-ld.so.cache1.1")) || (raw[28]&3 != 0 && raw[28]&3 != 2) {
		return nil, fmt.Errorf("unsupported host loader cache")
	}
	n := uint64(binary.LittleEndian.Uint32(raw[20:24]))
	size := uint64(binary.LittleEndian.Uint32(raw[24:28]))
	start := uint64(48) + 24*n
	end := start + size
	if n > 131072 || end > uint64(len(raw)) {
		return nil, fmt.Errorf("host loader cache exceeds bounds")
	}
	stringAt := func(offset uint32) (string, error) {
		if uint64(offset) < start || uint64(offset) >= end {
			return "", fmt.Errorf("invalid loader cache string offset")
		}
		value := raw[uint64(offset):end]
		nul := bytes.IndexByte(value, 0)
		if nul < 0 || nul > 4096 {
			return "", fmt.Errorf("invalid loader cache string")
		}
		return string(value[:nul]), nil
	}
	entries := map[string][]nativeIsolationCacheEntry{}
	for i := uint64(0); i < n; i++ {
		row := raw[48+i*24 : 48+(i+1)*24]
		// FLAG_X8664_LIB64 | FLAG_ELF_LIBC6. Foreign ABI entries cannot
		// resolve an amd64 runtime and do not join its closure.
		if binary.LittleEndian.Uint32(row[:4]) != 0x303 {
			continue
		}
		name, err := stringAt(binary.LittleEndian.Uint32(row[4:8]))
		if err != nil {
			return nil, err
		}
		path, err := stringAt(binary.LittleEndian.Uint32(row[8:12]))
		if err != nil {
			return nil, err
		}
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return nil, fmt.Errorf("nonabsolute cached library")
		}
		entries[name] = append(entries[name], nativeIsolationCacheEntry{Path: path, HWCaps: binary.LittleEndian.Uint64(row[16:24])})
	}
	return entries, nil
}

func nativeIsolationHostLoaderCache() (map[string][]nativeIsolationCacheEntry, error) {
	raw, err := os.ReadFile("/etc/ld.so.cache")
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return nativeIsolationLoaderCache(raw)
}

func nativeIsolationCacheAgrees(entries []nativeIsolationCacheEntry, selected string) error {
	resolved, err := filepath.EvalSymlinks(selected)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		other, err := filepath.EvalSymlinks(entry.Path)
		if err != nil {
			return err
		}
		if entry.HWCaps != 0 || other != resolved {
			return fmt.Errorf("host loader cache selects an unqualified alternate for %s", selected)
		}
	}
	return nil
}
