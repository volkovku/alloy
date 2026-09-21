package write

import (
	"os"
	"time"
)

// saveProfile saves one copy before fan-out and retries. Local failures must not
// prevent delivery to the remote endpoints.
func (f *fanOutClient) saveProfile(data []byte, extension string) {
	if f.config.ProfileDirectory == "" {
		return
	}
	if err := writeProfile(f.config.ProfileDirectory, data, extension); err != nil {
		f.logger.Error("failed to save profile locally", "directory", f.config.ProfileDirectory, "err", err)
	}
}

func writeProfile(directory string, data []byte, extension string) error {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	// CreateTemp provides unique names across concurrent calls and restarts and
	// creates files with mode 0600. Never use untrusted profile labels as paths.
	file, err := os.CreateTemp(directory, time.Now().UTC().Format("20060102T150405.000000000Z-")+"*"+extension)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(file.Name())
		if writeErr != nil {
			return writeErr
		}
		return closeErr
	}
	return nil
}
