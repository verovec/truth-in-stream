// Command genhash reads the operator password from stdin and prints the
// encoded argon2id hash to place in AUTH_PASSWORD_HASH. The password is read
// from stdin rather than argv so it never lands in shell history or process
// listings:
//
//	printf '%s' "$PASSWORD" | go run ./cmd/genhash
package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/alexedwards/argon2id"

	"github.com/verovec/truth-in-stream/backend/internal/service"
)

func main() {
	if err := generate(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func generate(in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	scanner.Scan()
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("reading password from stdin: %w", err)
	}
	password := scanner.Text()
	if password == "" {
		return errors.New("password must not be empty")
	}
	hash, err := argon2id.CreateHash(password, service.OperatorHashParams)
	if err != nil {
		return fmt.Errorf("hashing password: %w", err)
	}
	_, err = fmt.Fprintln(out, hash)
	return err
}
