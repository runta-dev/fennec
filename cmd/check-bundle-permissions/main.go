package main

import (
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	volume := flag.Bool("volume", false, "Check a mounted distribution volume")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: check-bundle-permissions <app>")
		os.Exit(2)
	}
	if err := checkTree(flag.Arg(0), !*volume); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func check(root string) error {
	return checkTree(root, true)
}
func checkTree(root string, bundle bool) error {
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() || bundle && !strings.EqualFold(filepath.Ext(root), ".app") {
		return fmt.Errorf("application directory required")
	}
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		mode := info.Mode().Perm()
		if mode&0022 != 0 {
			return fmt.Errorf("application content is writable by another user: %s", path)
		}
		required := os.FileMode(0444)
		relative, _ := filepath.Rel(root, path)
		if entry.IsDir() || mode&0111 != 0 || strings.HasPrefix(relative, "Contents/MacOS/") {
			required = 0555
		}
		if mode&required != required {
			return fmt.Errorf("application content is not readable/executable by another user: %s", path)
		}
		return nil
	})
}
