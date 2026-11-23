package inspect

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"unicode/utf16"
	"unicode/utf8"
)

const (
	maxSnapshotFiles = 1000
	maxSnapshotFile  = 2 * 1024 * 1024
	maxSnapshotBytes = 32 * 1024 * 1024
	maxSnapshotTool  = 8 * 1024 * 1024
	maxSourceRequest = 16 * 1024 * 1024
)

var errSnapshot = errors.New("部署源码无法核实或发生变化，停止并重新预检")

// Selection comes from the existing TGCloud product manifest and runtime rules.
type sourceSelection struct {
	Files    []string          `json:"files"`
	Payload  map[string]string `json:"payload"`
	Tool     string            `json:"tool"`
	ToolHash string            `json:"tool_sha256"`
}

func InstallSourceSupported() bool { return true }

func InstallSource(args []string, input io.Reader) (any, error) {
	if len(args) == 4 && args[0] == "mark" {
		return true, markInstallPayload(args[1], args[2], args[3])
	}
	if len(args) < 3 || len(args) > 4 {
		return nil, errSnapshot
	}

	root := args[1]
	raw, err := io.ReadAll(io.LimitReader(input, maxSourceRequest+1))
	var selection sourceSelection
	if err != nil || len(raw) > maxSourceRequest || json.Unmarshal(raw, &selection) != nil {
		return nil, errSnapshot
	}

	files, err := captureSources(root, selection.Files)
	if err != nil {
		return nil, err
	}
	digest := snapshotDigest(files)
	switch {
	case args[0] == "snapshot" && len(args) == 3:
		return digest, snapshotInstallSource(root, args[2], selection, files)
	case args[0] == "verify" && len(args) == 3:
		if args[2] == digest {
			return true, nil
		}
	case args[0] == "payload" && len(args) == 4:
		if args[3] == digest {
			return true, buildInstallPayload(args[2], selection.Payload, files)
		}
	}
	return nil, errSnapshot
}

func captureSources(root string, names []string) (map[string][]byte, error) {
	if !sourceDirectory(root, false) || len(names) == 0 || len(names) > maxSnapshotFiles {
		return nil, errSnapshot
	}
	files, size := make(map[string][]byte, len(names)), 0
	for _, name := range names {
		if _, duplicate := files[name]; duplicate || !localSourceName(name) {
			return nil, errSnapshot
		}
		body, err := readSourceFile(filepath.Join(root, name), maxSnapshotFile)
		if err != nil {
			return nil, errSnapshot
		}
		size += len(body)
		if size > maxSnapshotBytes {
			return nil, errSnapshot
		}
		files[name] = body
	}
	return files, nil
}

func localSourceName(name string) bool {
	return utf8.ValidString(name) && filepath.IsLocal(name) && name != "." && filepath.Clean(name) == name &&
		!strings.ContainsAny(name, "\\:") && !strings.ContainsFunc(name, func(r rune) bool { return r < 32 })
}

func sourceDirectory(path string, private bool) bool {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return false
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return false
	}
	info, err := os.Lstat(path)
	return err == nil && info.IsDir() && (!private ||
		(info.Mode().Perm()&0077 == 0 && info.Sys().(*syscall.Stat_t).Uid == uint32(os.Geteuid())))
}

// Match json.dumps(..., sort_keys=True, separators=(",", ":")) with its default
// ASCII escapes, so identical source files retain their Python source digest.
func snapshotDigest(files map[string][]byte) string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)

	var manifest strings.Builder
	manifest.WriteString("clouddrive2-local-source-v1\x00{")
	for i, name := range names {
		if i > 0 {
			manifest.WriteByte(',')
		}
		manifest.WriteByte('"')
		for _, r := range name {
			switch {
			case r == '"' || r == '\\':
				manifest.WriteByte('\\')
				manifest.WriteRune(r)
			case r > 0xffff:
				high, low := utf16.EncodeRune(r)
				fmt.Fprintf(&manifest, "\\u%04x\\u%04x", high, low)
			case r >= 0x7f:
				fmt.Fprintf(&manifest, "\\u%04x", r)
			default:
				manifest.WriteRune(r)
			}
		}
		fmt.Fprintf(&manifest, "\":\"%s\"", sha256Text(files[name]))
	}
	manifest.WriteByte('}')
	return sha256Text([]byte(manifest.String()))
}

func readSnapshotTool(root string, selection sourceSelection) ([]byte, error) {
	if (selection.Tool != "hostcli/bin/linux-amd64/tgcloud-host" &&
		selection.Tool != "hostcli/bin/linux-arm64/tgcloud-host") || !sourceHash.MatchString(selection.ToolHash) {
		return nil, errSnapshot
	}
	body, err := readSourceFile(filepath.Join(root, selection.Tool), maxSnapshotTool)
	if err != nil || sha256Text(body) != selection.ToolHash {
		return nil, errSnapshot
	}
	return body, nil
}

func snapshotInstallSource(root, destination string, selection sourceSelection, files map[string][]byte) error {
	if selection.Tool != "" || selection.ToolHash != "" {
		if _, duplicate := files[selection.Tool]; duplicate {
			return errSnapshot
		}
		body, err := readSnapshotTool(root, selection)
		if err != nil {
			return err
		}
		files[selection.Tool] = body
	}
	return writeSourceOutput(destination, files, 0700, 0600, func() error {
		for name, body := range files {
			limit := int64(maxSnapshotFile)
			if name == selection.Tool {
				limit = maxSnapshotTool
			}
			current, err := readSourceFile(filepath.Join(root, name), limit)
			frozen, frozenErr := readSourceFile(filepath.Join(destination, name), limit)
			if err != nil || frozenErr != nil || !slices.Equal(current, body) || !slices.Equal(frozen, body) {
				return errSnapshot
			}
		}
		return nil
	})
}

func buildInstallPayload(destination string, mapping map[string]string, files map[string][]byte) error {
	if len(mapping) == 0 || len(mapping) > maxSnapshotFiles {
		return errSource
	}
	output := make(map[string][]byte, len(mapping))
	for target, name := range mapping {
		body, exists := files[name]
		if !exists || !managedSource(target) {
			return errSource
		}
		output[target] = body
	}
	for _, name := range requiredSourceFiles {
		if _, exists := output[name]; !exists {
			return errSource
		}
	}
	return writeSourceOutput(destination, output, 0755, 0644, nil)
}

func writeSourceOutput(destination string, files map[string][]byte, dirMode, fileMode os.FileMode, verify func() error) (err error) {
	if !filepath.IsAbs(destination) || filepath.Clean(destination) != destination ||
		!sourceDirectory(filepath.Dir(destination), true) {
		return errSnapshot
	}
	if err := os.Mkdir(destination, 0700); err != nil {
		return errSnapshot
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(destination)
		}
	}()
	for name, body := range files {
		path := filepath.Join(destination, name)
		parent := filepath.Dir(path)
		if err := os.MkdirAll(parent, dirMode); err != nil {
			return errSnapshot
		}
		// COPY must remain readable to the non-root Bot even when the installer
		// runs with umask 077.
		for dir := parent; dir != destination; dir = filepath.Dir(dir) {
			if err := os.Chmod(dir, dirMode); err != nil {
				return errSnapshot
			}
		}
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return errSnapshot
		}
		_, writeErr := file.Write(body)
		modeErr := file.Chmod(fileMode)
		closeErr := file.Close()
		if writeErr != nil || modeErr != nil || closeErr != nil {
			return errSnapshot
		}
	}
	if verify != nil {
		return verify()
	}
	return nil
}

func markInstallPayload(directory, version, digest string) error {
	if !sourceDirectory(directory, true) || !versionPattern.MatchString(version) || !sourceHash.MatchString(digest) {
		return errSource
	}
	marker := sourceMarker{Schema: 1, Owner: "tgcloud.local-source", Project: "tgcloud", Mode: "shell",
		Edition: "clouddrive2", Version: version, SourceSHA256: digest, Files: make(map[string]string), State: "pending"}
	if err := filepath.WalkDir(directory, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.Type()&os.ModeSymlink != 0 {
			return errSource
		}
		if entry.IsDir() {
			return nil
		}
		name := strings.TrimPrefix(path, directory+"/")
		if !managedSource(name) || len(marker.Files) >= maxSnapshotFiles {
			return errSource
		}
		body, err := readSourceFile(path, maxSnapshotFile)
		if err != nil {
			return errSource
		}
		marker.Files[name] = sha256Text(body)
		return nil
	}); err != nil {
		return err
	}
	for _, name := range requiredSourceFiles {
		if _, exists := marker.Files[name]; !exists {
			return errSource
		}
	}
	return writeSourceMarker(directory, marker)
}
