//go:build windows

package main

import (
	"bytes"
	"testing"
)

func TestEnsureUTF8BOMAddsMissingMarker(t *testing.T) {
	bom := []byte{0xef, 0xbb, 0xbf}
	got := ensureUTF8BOM([]byte("param()"))
	if !bytes.HasPrefix(got, bom) {
		t.Fatal("UTF-8 BOM was not added")
	}
}

func TestEmbeddedSettingsScriptHasOneUTF8BOM(t *testing.T) {
	bom := []byte{0xef, 0xbb, 0xbf}
	got := ensureUTF8BOM([]byte(settingsPowerShell))
	if !bytes.HasPrefix(got, bom) {
		t.Fatal("embedded settings script is missing its UTF-8 BOM")
	}
	if bytes.HasPrefix(got[len(bom):], bom) {
		t.Fatal("embedded settings script contains a duplicate UTF-8 BOM")
	}
}
