// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package util

import (
	"strings"
	"unicode"
)

// TitleCase replaces underscore with blank, certain keywords with caps and converts all
// words to title case (first letter of each word capitalized) on the given string.
func TitleCase(s string) string {
	replacements := [][2]string{
		{"_", " "},
		{"hdd", "HDD"},
		{"iscsi", "ISCSI"},
		{"nfs", "NFS"},
		{"vmdk", "VMDK"},
		{"kvm", "KVM"},
		{"ssd", "SSD"},
	}
	for _, r := range replacements {
		s = strings.ReplaceAll(s, r[0], r[1])
	}

	words := strings.Fields(s)
	for i, word := range words {
		if word != "" {
			runes := []rune(word)
			runes[0] = unicode.ToUpper(runes[0])
			words[i] = string(runes)
		}
	}
	return strings.Join(words, " ")
}
