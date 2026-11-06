package audit

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"strings"
)

const MaxArchiveBytes = 8 << 20
const maxFileBytes = 256 << 10
const maxFiles = 300
const maxExtractedBytes = 20 << 20

func validProjectPath(name string) bool {
	if name == "" || strings.HasPrefix(name, "/") || strings.ContainsAny(name, `\:<>"|?*`+"\x00") || path.Clean(name) != name || filepath.IsAbs(filepath.FromSlash(name)) || filepath.VolumeName(filepath.FromSlash(name)) != "" {
		return false
	}
	for _, component := range strings.Split(name, "/") {
		base := strings.ToLower(strings.SplitN(component, ".", 2)[0])
		if component == ".." || component == "." || strings.TrimRight(component, ". ") != component || component == ".git" || component == "node_modules" || strings.HasPrefix(component, ".env") || base == "con" || base == "prn" || base == "aux" || base == "nul" || len(base) == 4 && (strings.HasPrefix(base, "com") || strings.HasPrefix(base, "lpt")) && base[3] >= '1' && base[3] <= '9' {
			return false
		}
	}
	return true
}

// LoadZIP accepts source archives, never filesystem paths from the archive.
// The returned files can be materialized only inside an ephemeral sandbox job.
func LoadZIP(data []byte) (Files, error) {
	if len(data) == 0 || len(data) > MaxArchiveBytes {
		return nil, errors.New("ZIP upload must be at most 8 MiB")
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("invalid ZIP: %w", err)
	}
	if len(archive.File) > maxFiles {
		return nil, errors.New("too many archive entries")
	}
	files := make(Files)
	total := int64(0)
	seen := make(map[string]bool)
	for _, entry := range archive.File {
		name := entry.Name
		if !validProjectPath(strings.TrimSuffix(name, "/")) {
			return nil, fmt.Errorf("unsafe archive entry %q", name)
		}
		if entry.FileInfo().IsDir() {
			continue
		}
		if !entry.FileInfo().Mode().IsRegular() {
			return nil, fmt.Errorf("non-regular archive entry %q", name)
		}
		key := strings.ToLower(name)
		if seen[key] {
			return nil, fmt.Errorf("duplicate archive entry %q", name)
		}
		seen[key] = true
		if entry.UncompressedSize64 > maxFileBytes {
			return nil, fmt.Errorf("file %q exceeds 256 KiB", name)
		}
		r, err := entry.Open()
		if err != nil {
			return nil, err
		}
		contents, readErr := io.ReadAll(io.LimitReader(r, maxFileBytes+1))
		closeErr := r.Close()
		if readErr != nil || closeErr != nil || len(contents) > maxFileBytes {
			return nil, fmt.Errorf("could not safely read %q", name)
		}
		total += int64(len(contents))
		if total > maxExtractedBytes {
			return nil, errors.New("extracted files exceed 20 MiB")
		}
		files[name] = contents
	}
	if len(files) == 0 {
		return nil, errors.New("empty project archive")
	}
	// GitHub archives have one enclosing repository-version directory.
	var prefix string
	for name := range files {
		first, _, ok := strings.Cut(name, "/")
		if !ok || (prefix != "" && first != prefix) {
			prefix = ""
			break
		}
		prefix = first
	}
	if prefix != "" {
		stripped := make(Files, len(files))
		for name, contents := range files {
			stripped[strings.TrimPrefix(name, prefix+"/")] = contents
		}
		files = stripped
	}
	if len(files["go.mod"]) == 0 {
		return nil, errors.New("project root must contain go.mod")
	}
	return files, nil
}
