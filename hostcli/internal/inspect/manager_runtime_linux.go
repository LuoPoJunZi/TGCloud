package inspect

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"
)

const (
	managerMarker = "installation.json"
	managerHelper = "tgcloud-host"
	managerEntry  = "src/tgcloud/deployment/runtime.py"
	managerMenu   = "src/tgcloud/cli/management.py"
	managerHeader = "#!/bin/sh\n# CloudDrive2 standalone manager managed v1\n"
)

var (
	errManager        = errors.New("本地管理运行包、目录或启动器无法核实")
	errManagerPublish = errors.New("本地管理工具发布失败，未切换 tgcloud 启动器")
	managerKey        = regexp.MustCompile("[a-f0-9]{64}")
	shellWord         = regexp.MustCompile("^[A-Za-z0-9_@%+=:,./-]+$")
)

type managerManifest struct {
	Schema   int               `json:"schema"`
	Owner    string            `json:"owner"`
	Version  string            `json:"version"`
	Origin   map[string]string `json:"origin"`
	Files    map[string]string `json:"files"`
	ToolHash string            `json:"host_cli_sha256,omitempty"`
}

type managerPublication struct {
	Root, Command, Lock, Source, Version, Digest string
}

func ManagerRuntimeSupported() bool { return true }

func ManagerRuntime(args []string, input io.Reader) (any, error) {
	switch {
	case len(args) == 3 && args[0] == "check":
		_, manifest, err := checkManagerDestination(args[1], args[2])
		return manifest, err
	case len(args) == 7 && args[0] == "publish":
		request := managerPublication{Root: args[1], Command: args[2], Lock: args[3], Source: args[4], Version: args[5], Digest: args[6]}
		raw, err := io.ReadAll(io.LimitReader(input, maxSourceRequest+1))
		var selection sourceSelection
		if err != nil || len(raw) > maxSourceRequest || json.Unmarshal(raw, &selection) != nil {
			return nil, errManager
		}
		return publishManager(request, selection)
	default:
		return nil, errManager
	}
}

func managerSafePath(path string, private bool) bool {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return false
	}
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err == nil {
			owner := info.Sys().(*syscall.Stat_t).Uid
			stickyAncestor := current != path && info.IsDir() && owner == 0 && info.Mode()&os.ModeSticky != 0
			if info.Mode()&os.ModeSymlink != 0 || (owner != 0 && owner != uint32(os.Geteuid())) ||
				(info.Mode().Perm()&0022 != 0 && !stickyAncestor) ||
				(current == path && private && !privateOwner(info, uint32(os.Geteuid()))) {
				return false
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return false
		}
		if filepath.Dir(current) == current {
			return true
		}
	}
}

func managerLauncher(bundle string) string {
	entry := filepath.Join(bundle, managerEntry)
	if !shellWord.MatchString(entry) {
		entry = "'" + strings.ReplaceAll(entry, "'", "'\"'\"'") + "'"
	}
	return managerHeader + "exec python3 -B " + entry + " \"$@\"\n"
}

func checkManagerDestination(root, command string) (string, *managerManifest, error) {
	if root == "/" || command == "/" || root == command || strings.HasPrefix(command, root+"/") || strings.HasPrefix(root, command+"/") ||
		!managerSafePath(root, true) || !managerSafePath(command, false) {
		return "", nil, errManager
	}
	runtime := filepath.Join(root, "runtime")
	if entries, err := os.ReadDir(root); err == nil {
		if !sourceDirectory(runtime, false) || !managerSafePath(runtime, false) {
			return "", nil, errManager
		}
		for _, entry := range entries {
			if entry.Name() != "runtime" && entry.Name() != "install.lock" {
				return "", nil, errManager
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", nil, errManager
	}
	if _, err := os.Lstat(command); errors.Is(err, os.ErrNotExist) {
		return "", nil, nil
	}
	raw, err := readSourceFile(command, 4096)
	if err != nil {
		return "", nil, errManager
	}
	for _, key := range managerKey.FindAllString(string(raw), -1) {
		bundle := filepath.Join(runtime, key)
		if string(raw) == managerLauncher(bundle) {
			manifest, err := verifyManagerBundle(bundle)
			return bundle, manifest, err
		}
	}
	return "", nil, errManager
}

func managerNames(root string) ([]string, error) {
	names := []string{"scripts/run_source.py", "src/tgcloud/__init__.py", "src/tgcloud/version.py", "src/tgcloud/config.py"}
	exists := func(name string) bool {
		info, err := os.Stat(filepath.Join(root, name))
		return err == nil && info.Mode().IsRegular()
	}
	if exists("src/tgcloud/deployment/lifecycle/backup.py") || exists("src/tgcloud/deployment/backup.py") || exists("src/tgcloud/cli/naming.py") {
		names = append(names, "src/tgcloud/persistence/__init__.py", "src/tgcloud/persistence/backup.py",
			"src/tgcloud/persistence/bundle.py", "src/tgcloud/domain/__init__.py", "src/tgcloud/domain/naming.py")
	}
	if exists("src/tgcloud/persistence/paths.py") {
		names = append(names, "src/tgcloud/persistence/paths.py")
	}
	directories := []string{"cli", "deployment", "addons"}
	if exists("src/tgcloud/compose/management.py") {
		directories = append(directories, "compose")
		names = append(names, "packaging/compose/docker-compose.clouddrive2.yml", "packaging/compose-https.sh", "packaging/external-webdav.json")
	}
	for _, dir := range directories {
		branch := filepath.Join(root, "src/tgcloud", dir)
		if _, err := os.Lstat(branch); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err := filepath.WalkDir(branch, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if strings.HasSuffix(path, ".py") {
				names = append(names, strings.TrimPrefix(path, root+"/"))
			}
			return nil
		}); err != nil {
			return nil, errManager
		}
	}
	if !slices.Contains(names, managerMenu) || !slices.Contains(names, managerEntry) || len(names) > maxSnapshotFiles {
		return nil, errManager
	}
	return names, nil
}

func verifyManagerBundle(bundle string) (*managerManifest, error) {
	if !sourceDirectory(bundle, true) || !managerSafePath(bundle, true) {
		return nil, errManager
	}
	raw, err := readSourceFile(filepath.Join(bundle, managerMarker), 128*1024)
	var manifest managerManifest
	var fields map[string]json.RawMessage
	if err != nil || json.Unmarshal(raw, &manifest) != nil || json.Unmarshal(raw, &fields) != nil ||
		manifest.Schema != 1 || manifest.Owner != "tgcloud.cli" || !planVersion.MatchString(manifest.Version) ||
		len(manifest.Origin) != 3 || manifest.Origin["kind"] != "local-source" || manifest.Origin["project"] != "tgcloud" ||
		!sourceHash.MatchString(manifest.Origin["source_sha256"]) {
		return nil, errManager
	}
	if _, exists := fields["host_cli_sha256"]; exists && !sourceHash.MatchString(manifest.ToolHash) {
		return nil, errManager
	}
	names, err := managerNames(bundle)
	if err != nil || len(names) != len(manifest.Files) {
		return nil, errManager
	}
	for _, name := range names {
		path := filepath.Join(bundle, name)
		if !managerSafePath(path, false) {
			return nil, errManager
		}
		body, err := readSourceFile(path, maxSnapshotFile)
		if err != nil || sha256Text(body) != manifest.Files[name] {
			return nil, errManager
		}
	}
	err = filepath.WalkDir(bundle, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := strings.TrimPrefix(path, bundle+"/")
		if name == managerHelper && manifest.ToolHash != "" {
			// A missing or corrupt optional helper does not disable the menu,
			// but a directory in its place is unexpected and rejected.
			if entry.IsDir() {
				return errManager
			}
			return nil
		}
		if !managerSafePath(path, false) ||
			(!entry.IsDir() && (!entry.Type().IsRegular() || (name != managerMarker && manifest.Files[name] == ""))) {
			return errManager
		}
		return nil
	})
	if err != nil {
		return nil, errManager
	}
	return &manifest, nil
}

func lockManager(path string) (*os.File, error) {
	dir := filepath.Dir(path)
	if !managerSafePath(path, false) || os.MkdirAll(dir, 0700) != nil || !sourceDirectory(dir, true) {
		return nil, errManager
	}
	file, err := openPrivate(path, syscall.O_CREAT|syscall.O_RDWR, uint32(os.Geteuid()))
	if err != nil {
		return nil, errManager
	}
	if syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		file.Close()
		return nil, errors.New("另一个 TGCloud 管理操作正在发布或清理运行包")
	}
	return file, nil
}

func managerCandidate(request managerPublication, selection sourceSelection) (managerManifest, map[string][]byte, error) {
	manifest := managerManifest{Schema: 1, Owner: "tgcloud.cli", Version: request.Version,
		Origin: map[string]string{"kind": "local-source", "project": "tgcloud", "source_sha256": request.Digest},
		Files:  make(map[string]string)}
	source, err := captureSources(request.Source, selection.Files)
	if err != nil || snapshotDigest(source) != request.Digest || !planVersion.MatchString(request.Version) {
		return manifest, nil, errSnapshot
	}
	names, err := managerNames(request.Source)
	if err != nil {
		return manifest, nil, err
	}
	files := make(map[string][]byte, len(names)+2)
	for _, name := range names {
		body, exists := source[name]
		if !exists {
			return manifest, nil, errManager
		}
		files[name] = body
		manifest.Files[name] = sha256Text(body)
	}
	if selection.Tool != "" || selection.ToolHash != "" {
		body, err := readSnapshotTool(request.Source, selection)
		if err != nil {
			return manifest, nil, err
		}
		files[managerHelper], manifest.ToolHash = body, selection.ToolHash
	}
	return manifest, files, nil
}

func publishManager(request managerPublication, selection sourceSelection) (map[string]any, error) {
	lock, err := lockManager(request.Lock)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	active, previous, err := checkManagerDestination(request.Root, request.Command)
	if err != nil {
		return nil, err
	}
	manifest, files, err := managerCandidate(request, selection)
	if err != nil {
		return nil, err
	}
	bundle, err := publishManagerBundle(request.Root, active, previous, manifest, files)
	if err != nil {
		return nil, err
	}
	if current, _, err := checkManagerDestination(request.Root, request.Command); err != nil || current != active {
		return nil, errManager
	}
	if err := switchManagerLauncher(request.Command, bundle); err != nil {
		return nil, err
	}
	return map[string]any{"version": request.Version, "matches": true, "state": "ok",
		"commands": map[string]string{"version": "ok", "listen": "ok"}}, nil
}

func sameManagerManifest(left, right *managerManifest) bool {
	return left != nil && right != nil && left.Schema == right.Schema && left.Owner == right.Owner &&
		left.Version == right.Version && left.ToolHash == right.ToolHash && maps.Equal(left.Origin, right.Origin) && maps.Equal(left.Files, right.Files)
}

func publishManagerBundle(root, active string, previous *managerManifest, manifest managerManifest, files map[string][]byte) (string, error) {
	raw, err := json.Marshal(manifest)
	if err != nil || len(raw)+1 > 128*1024 {
		return "", errManager
	}
	raw = append(raw, '\n')
	files[managerMarker] = raw
	target := filepath.Join(root, "runtime")
	if err := os.MkdirAll(target, 0700); err != nil {
		return "", errManagerPublish
	}
	bundle := filepath.Join(target, sha256Text(raw))
	if sameManagerManifest(previous, &manifest) {
		bundle = active
	}
	if _, err := os.Lstat(bundle); errors.Is(err, os.ErrNotExist) {
		candidate, err := os.MkdirTemp(target, ".manager-")
		if err != nil {
			return "", errManagerPublish
		}
		defer os.RemoveAll(candidate)
		for name, body := range files {
			path := filepath.Join(candidate, name)
			if os.MkdirAll(filepath.Dir(path), 0700) != nil || writeManagerFile(path, body, name == managerHelper) != nil {
				return "", errManagerPublish
			}
		}
		if _, err := verifyManagerBundle(candidate); err != nil {
			return "", err
		}
		if body, exists := files[managerHelper]; exists && !managerHelperMatches(filepath.Join(candidate, managerHelper), body) {
			return "", errManager
		}
		if err := probeManagerBundle(candidate, manifest.Version); err != nil {
			return "", err
		}
		if os.Rename(candidate, bundle) != nil {
			return "", errManagerPublish
		}
		return bundle, nil
	}
	saved, err := verifyManagerBundle(bundle)
	if err != nil || !sameManagerManifest(saved, &manifest) {
		return "", errManager
	}
	if err := probeManagerBundle(bundle, manifest.Version); err != nil {
		return "", err
	}
	if body, exists := files[managerHelper]; exists {
		path := filepath.Join(bundle, managerHelper)
		if !managerHelperMatches(path, body) && replaceManagerFile(path, body) != nil {
			return "", errManagerPublish
		}
	}
	return bundle, nil
}

func managerHelperMatches(path string, body []byte) bool {
	info, err := os.Lstat(path)
	if err != nil || !privateOwner(info, uint32(os.Geteuid())) || info.Mode().Perm()&0100 == 0 {
		return false
	}
	installed, err := readSourceFile(path, maxSnapshotTool)
	return err == nil && bytes.Equal(installed, body)
}

func writeManagerFile(path string, body []byte, executable bool) error {
	mode := os.FileMode(0600)
	if executable {
		mode = 0700
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	return finishManagerFile(file, body, mode)
}

func finishManagerFile(file *os.File, body []byte, mode os.FileMode) error {
	if err := file.Chmod(mode); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(body); err != nil {
		file.Close()
		return err
	}
	return errors.Join(file.Sync(), file.Close())
}

func replaceManagerFile(path string, body []byte) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".tgcloud-")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if err := finishManagerFile(temp, body, 0700); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}

func switchManagerLauncher(command, bundle string) error {
	if os.MkdirAll(filepath.Dir(command), 0755) != nil || replaceManagerFile(command, []byte(managerLauncher(bundle))) != nil {
		return errManagerPublish
	}
	return nil
}

func probeManagerBundle(bundle, version string) error {
	probe := func(args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		// The Go publisher has verified this bundle. Load its Python bootstrap
		// and probe the same dispatcher without rehashing the bundle twice.
		script := "import runpy, sys; runpy.run_path(sys.argv.pop(1)); sys.argv.pop(0); runpy.run_path(sys.argv[0], run_name='__main__')"
		command := exec.CommandContext(ctx, "python3", append([]string{"-I", "-S", "-B", "-X", "utf8", "-c", script,
			filepath.Join(bundle, managerEntry), filepath.Join(bundle, "scripts/run_source.py")}, args...)...)
		out, err := command.Output()
		return strings.TrimSpace(string(out)), err
	}
	actual, err := probe("--version")
	if err != nil || actual != "TGCloud CLI v"+version {
		return errors.New("本地管理版本核验未通过，未切换 tgcloud 启动器")
	}
	help, err := probe("listen", "--help")
	if err != nil || !strings.Contains(help, "--edition") || !strings.Contains(help, "login") ||
		!strings.Contains(help, "status") || !strings.Contains(help, "disable") {
		return errors.New("本地管理命令核验未通过，未切换 tgcloud 启动器")
	}
	return nil
}
