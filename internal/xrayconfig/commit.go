package xrayconfig

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ConfigPathPlaceholder in a validate command is replaced by the path of the
// candidate configuration, for example
// ["xray", "run", "-test", "-config", "{config}"].
const ConfigPathPlaceholder = "{config}"

// validateCommandFor returns the validate command pointed at candidate. An
// argument equal to the output path is replaced too, so existing commands keep
// working; a command referencing neither would check the live file instead of
// the candidate and is rejected.
func validateCommandFor(cmd []string, outputPath, candidate string) ([]string, error) {
	out := make([]string, len(cmd))
	referenced := false
	for i, arg := range cmd {
		switch {
		case strings.Contains(arg, ConfigPathPlaceholder):
			out[i] = strings.ReplaceAll(arg, ConfigPathPlaceholder, candidate)
			referenced = true
		case arg == outputPath:
			out[i] = candidate
			referenced = true
		default:
			out[i] = arg
		}
	}
	if !referenced {
		return nil, fmt.Errorf("validate command must reference %s or the output path %s", ConfigPathPlaceholder, outputPath)
	}
	return out, nil
}

// fileSnapshot is the content, permissions and ownership of a config file.
type fileSnapshot struct {
	exists   bool
	data     []byte
	mode     fs.FileMode
	uid, gid int
	hasOwner bool
}

func readFileSnapshot(path string) (fileSnapshot, error) {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return fileSnapshot{}, nil
	}
	if err != nil {
		return fileSnapshot{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fileSnapshot{}, err
	}
	snapshot := fileSnapshot{exists: true, data: data, mode: info.Mode().Perm()}
	snapshot.uid, snapshot.gid, snapshot.hasOwner = fileOwner(info)
	return snapshot, nil
}

// writeCandidate writes data next to outputPath, so the later rename is
// atomic, with the permissions and ownership the committed file should have.
// The name keeps a .json suffix because Xray picks the parser by extension.
func writeCandidate(outputPath string, data []byte, perm fs.FileMode, owner fileSnapshot) (string, error) {
	dir := filepath.Dir(outputPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create directory %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(outputPath)+".candidate-*.json")
	if err != nil {
		return "", fmt.Errorf("create candidate: %w", err)
	}
	name := tmp.Name()
	fail := func(err error) (string, error) {
		_ = tmp.Close()
		_ = os.Remove(name)
		return "", err
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(fmt.Errorf("write candidate: %w", err))
	}
	if err := tmp.Sync(); err != nil {
		return fail(fmt.Errorf("sync candidate: %w", err))
	}
	if err := tmp.Close(); err != nil {
		return fail(fmt.Errorf("close candidate: %w", err))
	}
	if err := os.Chmod(name, perm); err != nil {
		return fail(fmt.Errorf("chmod candidate: %w", err))
	}
	if owner.hasOwner {
		if err := os.Lchown(name, owner.uid, owner.gid); err != nil && !errors.Is(err, fs.ErrPermission) {
			return fail(fmt.Errorf("chown candidate: %w", err))
		}
	}
	return name, nil
}

// commitCandidate atomically replaces outputPath with the candidate.
func commitCandidate(candidate, outputPath string) error {
	if err := os.Rename(candidate, outputPath); err != nil {
		return fmt.Errorf("replace %s: %w", outputPath, err)
	}
	syncDir(filepath.Dir(outputPath))
	return nil
}

// restoreFile puts a previous config back atomically with its original
// permissions and ownership.
func restoreFile(outputPath string, previous fileSnapshot) error {
	if !previous.exists {
		return errors.New("no previous config to restore")
	}
	if current, err := os.ReadFile(outputPath); err == nil && bytes.Equal(current, previous.data) {
		return nil
	}
	candidate, err := writeCandidate(outputPath, previous.data, previous.mode, previous)
	if err != nil {
		return err
	}
	if err := commitCandidate(candidate, outputPath); err != nil {
		_ = os.Remove(candidate)
		return err
	}
	return nil
}

func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}

// appliedMarkerPath records the hash of the config Xray last loaded
// successfully, so an agent restart does not restart Xray for a config it
// already runs.
func appliedMarkerPath(outputPath string) string {
	return filepath.Join(filepath.Dir(outputPath), "."+filepath.Base(outputPath)+".applied")
}

func readAppliedMarker(outputPath string) string {
	data, err := os.ReadFile(appliedMarkerPath(outputPath))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func writeAppliedMarker(outputPath, hash string) error {
	return atomicWriteFile(appliedMarkerPath(outputPath), []byte(hash+"\n"), 0o644)
}
