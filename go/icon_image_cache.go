package main

import "sync"

var (
	iconImageCacheMu sync.RWMutex
	iconImageCache   = make(map[string][]byte) // icon_hash hex -> JPEG bytes
)

func getIconImageFromCache(iconHash string) ([]byte, bool) {
	iconImageCacheMu.RLock()
	b, ok := iconImageCache[iconHash]
	iconImageCacheMu.RUnlock()
	if !ok || len(b) == 0 {
		return nil, false
	}
	return b, true
}

func putIconImageCache(iconHash string, image []byte) {
	if iconHash == "" || len(image) == 0 {
		return
	}
	dup := make([]byte, len(image))
	copy(dup, image)
	iconImageCacheMu.Lock()
	iconImageCache[iconHash] = dup
	iconImageCacheMu.Unlock()
}

func clearIconImageCache() {
	iconImageCacheMu.Lock()
	iconImageCache = make(map[string][]byte)
	iconImageCacheMu.Unlock()
}
