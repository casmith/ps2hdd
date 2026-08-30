package asset_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/casmith/ps2hdd/internal/asset"
)

func TestCheckWritableAcceptsAWritableDirAndLeavesNothingBehind(t *testing.T) {
	dir := t.TempDir()
	if err := asset.CheckWritable(dir); err != nil {
		t.Fatalf("CheckWritable: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("the probe left %d file(s) behind: %v", len(entries), entries)
	}
}

func TestCheckWritableExplainsARefusal(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	err := asset.CheckWritable(dir)
	if err == nil {
		t.Fatal("a read-only directory was reported as writable")
	}
	// The message has to say the disk was left alone: a user who sees an
	// artwork failure needs to know it did not half-write something.
	if !strings.Contains(err.Error(), "nothing on the disk was changed") {
		t.Errorf("error does not say the disk is untouched:\n%v", err)
	}
	if !strings.Contains(err.Error(), "Permission was refused") {
		t.Errorf("error does not explain the errno:\n%v", err)
	}
}
