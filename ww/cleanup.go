package ww

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"monks.co/pkg/jj"
)

// cleanUntracked removes files and directories from the workspace that are
// not tracked by jj (i.e. gitignored). Without this, on release these files
// persist into the next acquire, where they may show up as untracked or
// added if the destination change's gitignore differs.
//
// The .jj and .git directories at the workspace root are always preserved.
func cleanUntracked(client *jj.Client, wsPath string) error {
	if err := client.Snapshot(wsPath); err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}

	files, err := client.FileList(wsPath)
	if err != nil {
		return fmt.Errorf("list tracked files: %w", err)
	}

	tracked := make(map[string]bool, len(files))
	trackedDirs := map[string]bool{}
	for _, f := range files {
		f = filepath.Clean(f)
		tracked[f] = true
		for dir := filepath.Dir(f); dir != "." && dir != string(filepath.Separator); dir = filepath.Dir(dir) {
			trackedDirs[dir] = true
		}
	}

	return filepath.WalkDir(wsPath, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == wsPath {
			return nil
		}
		rel, err := filepath.Rel(wsPath, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			if rel == ".jj" || rel == ".git" {
				return filepath.SkipDir
			}
			if !trackedDirs[rel] {
				if err := os.RemoveAll(path); err != nil {
					return fmt.Errorf("remove %s: %w", rel, err)
				}
				return filepath.SkipDir
			}
			return nil
		}
		if !tracked[rel] {
			if err := os.Remove(path); err != nil {
				return fmt.Errorf("remove %s: %w", rel, err)
			}
		}
		return nil
	})
}
