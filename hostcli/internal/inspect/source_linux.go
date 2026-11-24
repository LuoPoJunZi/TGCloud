package inspect

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
)

const SourceMarkerName = "tgcloud-source.json"
const sourceMarkerName = SourceMarkerName

var RequiredSourceFiles = []string{"app/main.py", "app/tgcloud/version.py", "Dockerfile", "docker-compose.yml", "manage.sh"}
var requiredSourceFiles = RequiredSourceFiles

var (
	sourceHash = regexp.MustCompile(`^[0-9a-f]{64}$`)
	errSource  = errors.New("本地部署来源检查未通过，未激活安装")
)

type SourceMarker struct {
	Schema       int               `json:"schema"`
	Owner        string            `json:"owner"`
	Project      string            `json:"project"`
	Mode         string            `json:"mode"`
	Edition      string            `json:"edition"`
	Version      string            `json:"version"`
	SourceSHA256 string            `json:"source_sha256"`
	Files        map[string]string `json:"files"`
	State        string            `json:"state"`
}

type sourceMarker = SourceMarker

func (marker SourceMarker) CodeFingerprint() string { return marker.codeFingerprint() }

func SHA256Text(data []byte) string { return sha256Text(data) }

func ReadSourceFile(path string, limit int64) ([]byte, error) { return readSourceFile(path, limit) }

func VerifyDeploymentSource(directory, state string) (SourceMarker, error) {
	return verifyDeploymentSource(directory, state)
}

func sha256Text(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func readSourceFile(path string, limit int64) ([]byte, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return nil, errSource
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Size() > limit || before.Sys().(*syscall.Stat_t).Nlink != 1 {
		return nil, errSource
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errSource
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !sameSourceFile(before, opened) {
		return nil, errSource
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errSource
	}
	current, statErr := file.Stat()
	after, err := os.Lstat(path)
	resolved, resolveErr := filepath.EvalSymlinks(path)
	if err != nil || statErr != nil || resolveErr != nil || resolved != path ||
		!sameSourceFile(opened, current) || !sameSourceFile(opened, after) {
		return nil, errSource
	}
	return data, nil
}

func sameSourceFile(left, right os.FileInfo) bool {
	return os.SameFile(left, right) && left.Size() == right.Size() && left.ModTime() == right.ModTime() &&
		left.Sys().(*syscall.Stat_t).Ctim == right.Sys().(*syscall.Stat_t).Ctim
}

func verifyPendingSource(directory string) (sourceMarker, error) {
	return verifyDeploymentSource(directory, "pending")
}

func verifyDeploymentSource(directory, state string) (sourceMarker, error) {
	var marker sourceMarker
	data, err := readSourceFile(filepath.Join(directory, sourceMarkerName), 256*1024)
	if err != nil || json.Unmarshal(data, &marker) != nil || !validSourceMarker(data, marker, state) {
		return sourceMarker{}, errSource
	}
	for _, required := range requiredSourceFiles {
		if _, exists := marker.Files[required]; !exists {
			return sourceMarker{}, errSource
		}
	}
	for name, expected := range marker.Files {
		if !managedSource(name) || !sourceHash.MatchString(expected) {
			return sourceMarker{}, errSource
		}
		body, err := readSourceFile(filepath.Join(directory, name), 2*1024*1024)
		if err != nil || sha256Text(body) != expected {
			return sourceMarker{}, errSource
		}
	}
	if err := filepath.WalkDir(filepath.Join(directory, "app"), func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.Type()&os.ModeSymlink != 0 {
			return errSource
		}
		if filepath.Ext(path) == ".py" {
			name := strings.TrimPrefix(path, directory+"/")
			if _, listed := marker.Files[name]; !listed || !entry.Type().IsRegular() {
				return errSource
			}
		}
		return nil
	}); err != nil {
		return sourceMarker{}, errSource
	}
	return marker, nil
}

func validSourceMarker(data []byte, marker sourceMarker, state string) bool {
	if marker.Schema != 1 || marker.Owner != "tgcloud.local-source" || marker.Project != "tgcloud" ||
		marker.Mode != "shell" || marker.Edition != "clouddrive2" || marker.State != state ||
		!versionPattern.MatchString(marker.Version) || !sourceHash.MatchString(marker.SourceSHA256) ||
		len(marker.Files) == 0 || len(marker.Files) > 1000 {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || len(fields) != 9 {
		return false
	}
	for _, name := range []string{"schema", "owner", "project", "mode", "edition", "state", "version", "source_sha256", "files"} {
		if _, exists := fields[name]; !exists {
			return false
		}
	}
	return true
}

func managedSource(name string) bool {
	if !filepath.IsLocal(name) || filepath.Clean(name) != name || strings.ContainsAny(name, "\\:") ||
		strings.ContainsFunc(name, func(character rune) bool { return character < 32 }) {
		return false
	}
	if strings.HasPrefix(name, "app/") {
		return strings.HasSuffix(name, ".py")
	}
	return name != "app" && name != sourceMarkerName && slices.Contains(managedUpgradeFiles, name)
}

// Match tgcloud.deployment.fingerprints using the verified host file hashes.
func (marker sourceMarker) codeFingerprint() string {
	names := make([]string, 0, len(marker.Files))
	for name := range marker.Files {
		if strings.HasPrefix(name, "app/") {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	hash := sha256.New()
	for _, name := range names {
		fmt.Fprintf(hash, "%s  %s\n", marker.Files[name], name)
	}
	return "tgcloud-code-v2:sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func activateSource(directory string, marker sourceMarker) error {
	marker.State = "ready"
	return writeSourceMarker(directory, marker)
}

func writeSourceMarker(directory string, marker sourceMarker) error {
	file, err := os.CreateTemp(directory, ".source-")
	if err != nil {
		return errSource
	}
	defer os.Remove(file.Name())
	err = json.NewEncoder(file).Encode(marker)
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err != nil || closeErr != nil {
		return errSource
	}
	if err := os.Rename(file.Name(), filepath.Join(directory, sourceMarkerName)); err != nil {
		return errSource
	}
	return nil
}
