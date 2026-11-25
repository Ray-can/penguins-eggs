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
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

var compressionFactors = map[string]uint64{
	"xz":   31,
	"zstd": 35,
	"gzip": 37,
	"lzo":  52,
	"lzma": 52,
	"lz4":  52,
}

type SpaceReport struct {
	RootUsedKiB     uint64
	ExcludedKiB     uint64
	CompressedKiB   uint64
	FreeSnapshotKiB uint64
	FreeWorkKiB     uint64
	SnapshotDir     string
	WorkDir         string
	SamePartition   bool
}

func (r SpaceReport) NeededKiB() uint64 {
	if r.SamePartition {
		return r.CompressedKiB * 2
	}
	return r.CompressedKiB
}

func (r SpaceReport) String() string {
	const mib = 1024.0
	const gib = 1024.0 * 1024.0
	lines := []string{
		fmt.Sprintf("  Root used:        %.1f MiB", float64(r.RootUsedKiB)/mib),
		fmt.Sprintf("  Excluded:         %.1f MiB", float64(r.ExcludedKiB)/mib),
		fmt.Sprintf("  Estimated ISO:    %.1f MiB", float64(r.CompressedKiB)/mib),
		fmt.Sprintf("  Free on %s: %.2f GiB", r.SnapshotDir, float64(r.FreeSnapshotKiB)/gib),
	}
	if r.SamePartition {
		lines = append(lines, "  Work dir on same partition: needs ~2x ISO size")
	} else {
		lines = append(lines, fmt.Sprintf("  Free on %s: %.2f GiB", r.WorkDir, float64(r.FreeWorkKiB)/gib))
	}
	return strings.Join(lines, "\n")
}

func CheckDiskSpace(workDir string, snapshotDir string, compression string, excludeListPath string) (*SpaceReport, error) {
	factor, ok := compressionFactors[compression]
	if !ok {
		factor = 35 // default zstd
	}

	rootUsed, err := usedSpaceKiB("/")
	if err != nil {
		return nil, fmt.Errorf("cannot determine root usage: %w", err)
	}

	excludedSize := estimateExcludedKiB(excludeListPath)
	if excludedSize > rootUsed {
		excludedSize = rootUsed
	}

	compressedSize := (rootUsed - excludedSize) * factor / 100

	freeSnapshot, err := freeSpaceKiB(snapshotDir)
	if err != nil {
		return nil, fmt.Errorf("cannot determine free space on %s: %w", snapshotDir, err)
	}

	freeWork, err := freeSpaceKiB(workDir)
	if err != nil {
		freeWork = freeSnapshot
	}

	samePartition := onSameDevice(workDir, snapshotDir)

	return &SpaceReport{
		RootUsedKiB:     rootUsed,
		ExcludedKiB:     excludedSize,
		CompressedKiB:   compressedSize,
		FreeSnapshotKiB: freeSnapshot,
		FreeWorkKiB:     freeWork,
		SnapshotDir:     snapshotDir,
		WorkDir:         workDir,
		SamePartition:   samePartition,
	}, nil
}

func usedSpaceKiB(path string) (uint64, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, err
	}
	blockSize := uint64(stat.Bsize)
	totalBytes := stat.Blocks * blockSize
	freeBytes := stat.Bfree * blockSize
	return (totalBytes - freeBytes) / 1024, nil
}

func freeSpaceKiB(path string) (uint64, error) {
	if err := os.MkdirAll(path, 0755); err != nil {
		return 0, err
	}
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, err
	}
	return (stat.Bavail * uint64(stat.Bsize)) / 1024, nil
}

func onSameDevice(path1, path2 string) bool {
	var stat1, stat2 unix.Statfs_t
	if unix.Statfs(path1, &stat1) != nil || unix.Statfs(path2, &stat2) != nil {
		return true // conservative assumption
	}
	return stat1.Fsid == stat2.Fsid
}

func estimateExcludedKiB(excludeListPath string) uint64 {
	return estimateExcludedKiBFrom("/", excludeListPath)
}

// estimateExcludedKiBFrom expands the mksquashfs -ef patterns against root
// and sums the size of the matched files. The mksquashfs pattern dialect is
// supported: shell wildcards (*, ?, []), extglob !(a|b) and doublestar **.
func estimateExcludedKiBFrom(root string, excludeListPath string) uint64 {
	if excludeListPath == "" {
		return 0
	}
	data, err := os.ReadFile(excludeListPath)
	if err != nil {
		return 0
	}

	var matches []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		matches = append(matches, expandExcludePattern(root, line)...)
	}

	// Only the filesystem that holds root contributes to the root usage we
	// estimate. Skipping other devices also avoids pseudo-filesystems such as
	// /proc and /sys, whose files can report absurd apparent sizes.
	var rootStat unix.Stat_t
	if err := unix.Stat(root, &rootStat); err != nil {
		return 0
	}

	// Patterns may overlap (e.g. "root/*" and "root/.bashrc"): keep only the
	// outermost matches and size every subtree once. Sorting by length makes
	// ancestors come before their descendants.
	sort.Slice(matches, func(i, j int) bool { return len(matches[i]) < len(matches[j]) })
	var roots []string
	for _, match := range matches {
		covered := false
		for _, r := range roots {
			if match == r || strings.HasPrefix(match, r+string(os.PathSeparator)) {
				covered = true
				break
			}
		}
		if !covered {
			roots = append(roots, match)
		}
	}

	var totalKiB uint64
	for _, r := range roots {
		totalKiB += pathSizeKiBOnDevice(r, rootStat.Dev)
	}
	return totalKiB
}

// expandExcludePattern resolves a relative mksquashfs exclude pattern into the
// concrete paths it matches below root. Non-existing paths are returned as-is
// and later sized as zero, mirroring mksquashfs' "cannot stat, ignoring".
func expandExcludePattern(root string, pattern string) []string {
	segments := strings.Split(strings.Trim(pattern, "/"), "/")
	var matches []string

	var walk func(dir string, index int)
	walk = func(dir string, index int) {
		if index == len(segments) {
			matches = append(matches, dir)
			return
		}
		segment := segments[index]
		switch {
		case segment == "" || segment == ".":
			walk(dir, index+1)
		case segment == "**":
			// Zero or more directory levels.
			walk(dir, index+1)
			entries, err := os.ReadDir(dir)
			if err != nil {
				return
			}
			for _, entry := range entries {
				if entry.IsDir() {
					walk(filepath.Join(dir, entry.Name()), index)
				}
			}
		case !strings.ContainsAny(segment, "*?[") && !strings.Contains(segment, "!("):
			walk(filepath.Join(dir, segment), index+1)
		default:
			entries, err := os.ReadDir(dir)
			if err != nil {
				return
			}
			for _, entry := range entries {
				if matchExcludeSegment(segment, entry.Name()) {
					walk(filepath.Join(dir, entry.Name()), index+1)
				}
			}
		}
	}

	walk(filepath.Clean(root), 0)
	return matches
}

// matchExcludeSegment applies the mksquashfs wildcard dialect to one path
// component. Whole-segment extglob alternatives such as !(themes|unicode.pf2)
// are supported in addition to filepath.Match's *, ? and [] classes.
func matchExcludeSegment(pattern, name string) bool {
	if strings.HasPrefix(pattern, "!(") && strings.HasSuffix(pattern, ")") {
		for _, alternative := range strings.Split(pattern[2:len(pattern)-1], "|") {
			if ok, err := filepath.Match(alternative, name); err == nil && ok {
				return false
			}
		}
		return true
	}
	ok, err := filepath.Match(pattern, name)
	return err == nil && ok
}

// pathSizeKiB recursively sums the regular files below root without following
// symlinks, using the filesystem the path itself lives on.
func pathSizeKiB(root string) uint64 {
	var stat unix.Stat_t
	if err := unix.Stat(root, &stat); err != nil {
		return 0
	}
	return pathSizeKiBOnDevice(root, stat.Dev)
}

// pathSizeKiBOnDevice sums the regular files below root that live on dev,
// without following symlinks. It stops at other filesystems (e.g. /proc, /sys,
// tmpfs or a USB disk mounted under /media) because those are not part of the
// root filesystem usage we estimate.
func pathSizeKiBOnDevice(root string, dev uint64) uint64 {
	var totalBytes int64
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return nil
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return nil
		}
		if stat.Dev != dev {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !info.IsDir() {
			totalBytes += info.Size()
		}
		return nil
	})
	return uint64(totalBytes) / 1024
}
