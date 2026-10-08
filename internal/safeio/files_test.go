package safeio

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExclusivePublicationAndSymlinkRejection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "owner.json")
	if err := WriteExclusive(path, []byte(`{"Version":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := WriteExclusive(path, []byte("overwrite")); err == nil {
		t.Fatal("overwrote developer file")
	}
	data, err := Read(path)
	if err != nil || string(data) != `{"Version":1}` {
		t.Fatalf("%q %v", data, err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(link); err == nil {
		t.Fatal("followed ownership symlink")
	}
	if err := WriteExclusive(link, []byte("overwrite")); err == nil {
		t.Fatal("replaced symlink")
	}
}
func TestStrictOwnershipJSON(t *testing.T) {
	dir := t.TempDir()
	for i, data := range []string{`{"Version":1,"Unknown":true}`, `{"Version":1} {"Version":2}`, `invalid`} {
		path := filepath.Join(dir, string(rune('a'+i)))
		if err := WriteExclusive(path, []byte(data)); err != nil {
			t.Fatal(err)
		}
		var value struct{ Version int }
		if err := ReadJSON(path, &value); err == nil {
			t.Fatalf("accepted %q", data)
		}
	}
}
