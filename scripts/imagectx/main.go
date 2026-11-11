// Command imagectx writes the base image's build context into a directory and
// prints its build arguments, one --build-arg flag per line. The release workflow
// uses it to publish exactly the image `airlock build` would make locally, for the
// default configuration (no extra packages).
//
//	go run ./scripts/imagectx <dir>
package main

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/alexghirelli/airlock/internal/config"
	"github.com/alexghirelli/airlock/internal/imagebuild"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: imagectx <dir>")
		os.Exit(2)
	}
	if err := run(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, "imagectx:", err)
		os.Exit(1)
	}
}

func run(dir string) error {
	r, err := imagebuild.BaseContext()
	if err != nil {
		return err
	}
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		dst := filepath.Join(dir, h.Name)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(h.Mode))
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, tr); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
	c := &config.Config{}
	args := imagebuild.BaseArgs(c)
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("--build-arg=%s=%s\n", k, args[k])
	}
	return nil
}
