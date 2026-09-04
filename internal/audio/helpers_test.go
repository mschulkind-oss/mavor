package audio

import "os"

func writeFakeWAV(path string) error {
	return os.WriteFile(path, []byte("RIFF....WAVEfmt fake"), 0o644)
}

func readFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}
