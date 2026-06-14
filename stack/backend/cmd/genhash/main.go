// Command genhash reads the operator password from stdin and prints the
// encoded argon2id hash to place in AUTH_PASSWORD_HASH. The password is read
// from stdin rather than argv so it never lands in shell history or process
// listings:
//
//	printf '%s' "$PASSWORD" | go run ./cmd/genhash
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"

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
	hash, err := service.HashOperatorPassword(scanner.Text())
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, hash)
	return err
}
