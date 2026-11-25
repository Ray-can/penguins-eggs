/*
 * penguins-eggs - Copyright (C) 2017-2026 Piero Proietti
 * Author: Piero Proietti <piero.proietti@gmail.com>
 * https://penguins-eggs.net
 *
 * This file is part of penguins-eggs.
 * Released under the GNU GPL v3 license.
 */

package planner

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSizedFile(t *testing.T, root, rel string, kib int) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), kib*1024), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestEstimateExcludedKiBFromExpandsPatterns(t *testing.T) {
	root := t.TempDir()

	// cache/* must recurse into sub-directories.
	writeSizedFile(t, root, "cache/a", 10)
	writeSizedFile(t, root, "cache/sub/b", 20)
	writeSizedFile(t, root, "cache/sub/deeper/c", 30)

	// extglob: keep.txt must survive for log/* but be excluded for log/!(keep.txt).
	writeSizedFile(t, root, "log/keep.txt", 5)
	writeSizedFile(t, root, "log/drop/x", 100)
	writeSizedFile(t, root, "log/drop/y/z", 200)

	writeSizedFile(t, root, "plain/file", 7)

	// doublestar, matched with a leading slash as the user list may contain it.
	writeSizedFile(t, root, "etc/openkylin-installer/small", 1)
	writeSizedFile(t, root, "etc/openkylin-installer/a/big", 64)

	listPath := filepath.Join(t.TempDir(), "excludes.list")
	content := strings.Join([]string{
		"cache/*",
		"cache/sub", // nested duplicate of cache/*
		"log/!(keep.txt)",
		"plain/file",
		"/etc/openkylin-installer/**",
		"missing/*",
		"# a comment",
		"",
	}, "\n")
	if err := os.WriteFile(listPath, []byte(content), 0644); err != nil {
		t.Fatalf("write exclude list: %v", err)
	}

	got := estimateExcludedKiBFrom(root, listPath)
	want := uint64(10 + 20 + 30 + 100 + 200 + 7 + 1 + 64)
	if got != want {
		t.Fatalf("estimateExcludedKiBFrom = %d KiB, want %d KiB", got, want)
	}
}

func TestEstimateExcludedKiBFromHandlesMissingList(t *testing.T) {
	if got := estimateExcludedKiBFrom(t.TempDir(), filepath.Join(t.TempDir(), "nope.list")); got != 0 {
		t.Fatalf("missing list should estimate 0 KiB, got %d", got)
	}
}

func TestPathSizeKiBDoesNotFollowSymlinks(t *testing.T) {
	root := t.TempDir()
	writeSizedFile(t, root, "real/file", 4)
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}

	if got := pathSizeKiB(filepath.Join(root, "link")); got != 0 {
		t.Fatalf("symlinked directory should not be followed, got %d KiB", got)
	}
	if got := pathSizeKiB(filepath.Join(root, "real")); got != 4 {
		t.Fatalf("real directory = %d KiB, want 4 KiB", got)
	}
}
