// SPDX-FileCopyrightText: 2025 Tássio Dias <https://github.com/tassiods/zapcast>
// SPDX-License-Identifier: Apache-2.0

package server

// Localisation plumbing. Per-locale tables live in i18n_{en,pt,es}.go.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"golang.org/x/text/language"
)

type langKey string

const (
	// Accept-Language values we recognise.
	langEnUS langKey = "en-US"
	langPtBR langKey = "pt-BR"
	langEsES langKey = "es-ES"
)

// translations is populated at init() by the per-locale files. Keys are
// stable once shipped so the frontend's t() helper doesn't need to track
// versioning.
var translations = map[langKey]map[string]string{}

// getLang extracts the preferred language from the request, defaulting to
// en-US when the header is missing, malformed, or asks for a locale we
// don't ship.
func getLang(r *http.Request) langKey {
	accept := r.Header.Get("Accept-Language")
	if accept == "" {
		return langEnUS
	}
	tags, _, err := language.ParseAcceptLanguage(accept)
	if err != nil || len(tags) == 0 {
		return langEnUS
	}
	for _, tag := range tags {
		base, _ := tag.Base()
		switch base.String() {
		case "pt":
			return langPtBR
		case "es":
			return langEsES
		}
	}
	return langEnUS
}

// T translates a key for the given request. Args are formatted with
// fmt.Sprintf when present.
func T(r *http.Request, key string, args ...any) string {
	return TForLang(getLang(r), key, args...)
}

// TForLang translates a key for an explicit locale, falling back to en-US
// for missing keys and finally to the raw key when the dictionary doesn't
// know it at all.
func TForLang(lang langKey, key string, args ...any) string {
	m, ok := translations[lang]
	if !ok {
		m = translations[langEnUS]
	}
	s, ok := m[key]
	if !ok {
		s = translations[langEnUS][key]
	}
	if s == "" {
		return key
	}
	if len(args) > 0 {
		return fmt.Sprintf(s, args...)
	}
	return s
}

// jsonErrorLocalized sends a localised structured error response. `key`
// becomes the machine-readable code; the translation against the request's
// Accept-Language becomes the display message.
func jsonErrorLocalized(w http.ResponseWriter, r *http.Request, status int, key string, args ...any) {
	jsonError(w, status, key, T(r, key, args...))
}

// translationCache is the per-locale snapshot served by /api/translations:
// pre-serialised body + a content-hash ETag. The dictionary is immutable
// for the lifetime of the binary, so we build the cache lazily on first
// access and reuse it forever.
type translationCache struct {
	body []byte
	etag string
}

var (
	translationsCacheMu sync.RWMutex
	translationsCache   = map[langKey]*translationCache{}
)

// translationsForLang returns the cached snapshot for `lang`, computing it
// once. Unknown locales fall back to en-US.
func translationsForLang(lang langKey) *translationCache {
	if _, ok := translations[lang]; !ok {
		lang = langEnUS
	}

	translationsCacheMu.RLock()
	if c, ok := translationsCache[lang]; ok {
		translationsCacheMu.RUnlock()
		return c
	}
	translationsCacheMu.RUnlock()

	body, _ := json.Marshal(translations[lang])
	sum := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(sum[:8]) + `"`

	c := &translationCache{body: body, etag: etag}
	translationsCacheMu.Lock()
	translationsCache[lang] = c
	translationsCacheMu.Unlock()
	return c
}
