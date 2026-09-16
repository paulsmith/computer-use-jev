//go:build !darwin

package computeruse

import "errors"

func computerUseSupported() bool { return false }

func computerUseWorkerPath() (string, error) {
	return "", errors.New("computer_use is only supported on macOS")
}
