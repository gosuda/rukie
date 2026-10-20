package auth

import (
	"errors"
	"flag"
	"io"
	"os"
	"strings"
)

type SecretSource struct {
	stdin *bool
	file  *string
}

func RegisterSecretFlags(fs *flag.FlagSet, field string) SecretSource {
	return SecretSource{
		stdin: fs.Bool(field+"-stdin", false, "read "+field+" from stdin"),
		file:  fs.String(field+"-file", "", "read "+field+" from a file"),
	}
}

func (s SecretSource) Resolve(stdin io.Reader) (string, error) {
	if *s.stdin && *s.file != "" {
		return "", errors.New("only one secret source may be set")
	}
	if *s.file != "" {
		raw, err := readBoundedFile(*s.file)
		if err != nil {
			return "", err
		}
		return checkSecretLength(raw)
	}
	if *s.stdin {
		raw, readErr := io.ReadAll(io.LimitReader(stdin, secretReadCap))
		if readErr != nil {
			return "", readErr
		}
		if len(raw) >= secretReadCap {
			return "", errors.New("password exceeds 512 bytes")
		}
		return checkSecretLength(string(raw))
	}
	return "", errors.New("a secret source is required")
}

const secretReadCap = 65536

func readBoundedFile(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // G304 is intended here: --password-file names an operator-supplied secret file
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	raw, readErr := io.ReadAll(io.LimitReader(f, secretReadCap))
	if readErr != nil {
		return "", readErr
	}
	if len(raw) >= secretReadCap {
		return "", errors.New("password exceeds 512 bytes")
	}
	return string(raw), nil
}

func checkSecretLength(raw string) (string, error) {
	trimmed := strings.TrimRight(raw, "\r\n")
	if len(trimmed) > PasswordMaxBytes {
		return "", errors.New("password exceeds 512 bytes")
	}
	return trimmed, nil
}
