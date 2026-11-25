/*
 * penguins-eggs - Copyright (C) 2017-2026 Piero Proietti
 * Author: Piero Proietti <piero.proietti@gmail.com>
 * https://penguins-eggs.net
 *
 * This file is part of penguins-eggs.
 * Released under the GNU GPL v3 license.
 */

package utils

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ReadSquashfsFile returns the contents of innerPath inside the squashfs image.
func ReadSquashfsFile(source, innerPath string) ([]byte, error) {
	return ReadSquashfsFileContext(context.Background(), source, innerPath)
}

// ReadSquashfsFileContext returns the contents of innerPath inside the squashfs
// image, honouring ctx for both extraction attempts.
//
// unsquashfs only gained -cat in squashfs-tools 4.5: Debian 11 and Ubuntu 20.04
// still ship 4.4, where -cat is rejected as a syntax error. Fall back to
// extracting just the requested file into a temporary directory, which every
// 4.x release supports.
func ReadSquashfsFileContext(ctx context.Context, source, innerPath string) ([]byte, error) {
	cat := exec.CommandContext(ctx, "unsquashfs", "-cat", source, innerPath)
	if out, err := cat.Output(); err == nil {
		return out, nil
	}

	tmpDir, err := os.MkdirTemp("", "eggs-squashfs-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)

	// unsquashfs refuses an existing destination directory, so point -d at a
	// child that does not exist yet.
	dest := filepath.Join(tmpDir, "root")
	extract := exec.CommandContext(ctx, "unsquashfs", "-n", "-d", dest, source, innerPath)
	if out, err := extract.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("unsquashfs extract %s from %s: %w: %s",
			innerPath, source, err, strings.TrimSpace(string(out)))
	}
	return os.ReadFile(filepath.Join(dest, innerPath))
}
