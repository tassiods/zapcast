// SPDX-FileCopyrightText: 2025 Tássio Dias <https://github.com/tassiods/zapcast>
// SPDX-License-Identifier: Apache-2.0

package server

// Go fuzz targets for the externally-facing parsers/validators. Each test
// seeds the corpus with the case set the existing unit tests already
// cover, so `go test` runs them deterministically and `go test -fuzz`
// goes deeper when explicitly requested.

import (
	"context"
	"strings"
	"testing"
)

// FuzzValidatePhone makes sure validatePhone never panics on arbitrary
// input — including unicode, control chars, very long strings, etc.
func FuzzValidatePhone(f *testing.F) {
	for _, seed := range []string{
		"+5511999998888", "(55) 11 99999-8888", "+1 (555) 123-4567",
		"", "abc", "1234567", "1234567890123456", "05511999998888",
		"\x00\x01\x02", "🤖", strings.Repeat("9", 1024),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in string) {
		// Either branch is acceptable — what matters is that the function
		// returns without panicking and that the cleaned phone (on
		// success) really is all digits within bounds.
		cleaned, err := validatePhone(in)
		if err != nil {
			return
		}
		if l := len(cleaned); l < 7 || l > 15 {
			t.Fatalf("validatePhone accepted phone of length %d: %q", l, cleaned)
		}
		if cleaned[0] == '0' {
			t.Fatalf("validatePhone accepted leading-zero phone: %q", cleaned)
		}
		for _, c := range cleaned {
			if c < '0' || c > '9' {
				t.Fatalf("validatePhone returned non-digit %q in %q", c, cleaned)
			}
		}
	})
}

// FuzzRenderTemplate exercises renderTemplate against arbitrary bodies +
// variable maps. The renderer must always finish in finite time and never
// loop, even when a variable's value contains another {placeholder}.
func FuzzRenderTemplate(f *testing.F) {
	f.Add("Hi {name}", "Alice", "5511999998888")
	f.Add("no markers", "Bob", "1")
	f.Add("{nome} ({telefone}) — {x}", "Carol", "2")
	f.Add("{nome}{nome}{nome}", "Dan", "3")
	f.Add("{nested {recursion}}", "Eve", "4")
	f.Fuzz(func(t *testing.T, tpl, name, phone string) {
		out := renderTemplate(tpl, Contact{Name: name, Phone: phone}, map[string]string{
			"x": "{y}",
			"y": "{x}",
		})
		// Functional guarantee: output length is bounded by template size +
		// a small constant times the largest substitution. If a recursive
		// substitution ever leaked, this would blow up to thousands of
		// chars.
		if len(out) > len(tpl)+1024 {
			t.Fatalf("renderTemplate output grew without bound (in=%d, out=%d)", len(tpl), len(out))
		}
	})
}

// FuzzParseCSVImport pokes the CSV reader with arbitrary bytes (including
// UTF-16, BOMs and ragged columns). It must always return; whether the
// rows slice is empty or full doesn't matter to the fuzzer.
func FuzzParseCSVImport(f *testing.F) {
	f.Add([]byte("Name,Phone,Group\nAlice,5511999998888,Friends\n"))
	f.Add([]byte("nome,telefone\nBob,5511888887777\n"))
	f.Add([]byte("\xff\xfeN\x00a\x00m\x00e\x00")) // UTF-16 LE noise
	f.Add([]byte(""))
	f.Add([]byte("\n\n\n"))
	f.Fuzz(func(t *testing.T, in []byte) {
		_, _ = parseCSVImport(strings.NewReader(string(in)))
	})
}

// FuzzAssessAuthTokenStrength makes sure the heuristic never panics on
// arbitrary tokens — including ones with multi-byte runes.
func FuzzAssessAuthTokenStrength(f *testing.F) {
	for _, seed := range []string{"", "password", "abcdef", "abcdefghij1234567890klmnopqrstuv", "🔥"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in string) {
		_ = assessAuthTokenStrength(in)
	})
}

// _ keeps go vet from flagging the unused context import when this file
// later grows a fuzz target that needs ctx.
var _ = context.Background
