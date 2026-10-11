package cmd

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"

	"github.com/ge/subscription-monitor/internal/appdir"
	"github.com/ge/subscription-monitor/internal/statuslinebundle"
)

// resolveStatuslineBinary keeps previews read-only. A plain go build without
// the generated Windows asset retains the existing single-binary entry.
func resolveStatuslineBinary(cli string, deploy bool) (string, error) {
	if runtime.GOOS != "windows" {
		return cli, nil
	}
	payload, err := statuslinebundle.Binary()
	if err != nil {
		if os.IsNotExist(err) || err == fs.ErrNotExist {
			return cli, nil
		}
		return "", err
	}
	dir, err := appdir.Data()
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "statusline", "submon.exe")
	if deploy {
		if err := deployStatuslineAt(path, payload); err != nil {
			return "", fmt.Errorf("部署状态栏 %s: %w", path, err)
		}
	}
	return path, nil
}

func deployStatuslineAt(path string, payload []byte) error {
	if len(payload) == 0 {
		return fmt.Errorf("状态栏程序为空")
	}
	if current, err := os.ReadFile(path); err == nil && bytes.Equal(current, payload) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".submon-statusline-*")
	if err != nil {
		return err
	}
	temp := f.Name()
	defer os.Remove(temp)
	if _, err = f.Write(payload); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Chmod(0700); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(temp, path)
}
