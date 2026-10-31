// Command commentspacing checks blank lines before standalone Go comment blocks.
// Source: plutoframework/pluto/scripts/commentspacing; synchronize library copies.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// missing returns physical line numbers, ignoring strings and generated files.
func missing(source []byte) ([]int, error) {
	files := token.NewFileSet()

	parsed, err := parser.ParseFile(
		files,
		"source.go",
		source,
		parser.ParseComments,
	)
	if err != nil {
		return nil, err
	}

	if ast.IsGenerated(parsed) {
		return nil, nil
	}

	file := files.File(parsed.Pos())

	var scan scanner.Scanner
	scan.Init(file, source, nil, scanner.ScanComments)

	lines := bytes.Split(source, []byte("\n"))

	var result []int

	previousEnd := 0

	for {
		position, kind, literal := scan.Scan()
		if kind == token.EOF {
			break
		}

		if kind != token.COMMENT {
			continue
		}

		physical := file.PositionFor(position, false)
		line := physical.Line
		end := line + strings.Count(literal, "\n")
		prefix := lines[line-1][:physical.Column-1]
		standalone := len(bytes.TrimSpace(prefix)) == 0

		continuation := line <= previousEnd+1
		if standalone {
			previousEnd = end
		} else {
			previousEnd = 0
		}

		if !standalone || continuation || line == 1 {
			continue
		}

		// Preserve directives, including their attachment to adjacent code.
		directive := false

		for _, prefix := range []string{
			"//go:", "//line ", "//export ", "//nolint", "//lint:",
			"// #nosec", "//#nosec",
		} {
			if strings.HasPrefix(literal, prefix) {
				directive = true
			}
		}

		if !directive && len(bytes.TrimSpace(lines[line-2])) != 0 {
			result = append(result, line)
		}
	}

	return result, nil
}

func format(source []byte, lines []int) []byte {
	parts := bytes.SplitAfter(source, []byte("\n"))

	newline := []byte("\n")
	if bytes.Contains(source, []byte("\r\n")) {
		newline = []byte("\r\n")
	}

	var result []byte

	next := 0
	for index, part := range parts {
		if next < len(lines) && lines[next] == index+1 {
			result = append(result, newline...)
			next++
		}

		result = append(result, part...)
	}

	return result
}

func run(root string, write bool, formatter string) error {
	failed := false

	err := filepath.WalkDir(
		root,
		func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}

			if entry.IsDir() {
				switch entry.Name() {
				case ".git", "vendor", "node_modules", "testdata":
					return filepath.SkipDir
				}

				return nil
			}

			if entry.Type()&os.ModeSymlink != 0 ||
				!strings.HasSuffix(path, ".go") {
				return nil
			}

			source, err := os.ReadFile(path)
			if err != nil {
				return err
			}

			parsed, err := parser.ParseFile(
				token.NewFileSet(),
				path,
				source,
				parser.ParseComments,
			)
			if err != nil {
				return err
			}

			if ast.IsGenerated(parsed) {
				return nil
			}

			if formatter != "" && !write {
				command := exec.Command(formatter, "fmt", "--stdin")
				command.Stdin = bytes.NewReader(source)
				command.Stderr = os.Stderr

				formatted, err := command.Output()
				if err != nil {
					return fmt.Errorf("%s: formatter: %w", path, err)
				}

				lines, err := missing(formatted)
				if err != nil {
					return err
				}

				if !bytes.Equal(source, format(formatted, lines)) {
					fmt.Fprintf(
						os.Stderr,
						"%s: formatting differs; run the repository formatter\n",
						path,
					)

					failed = true
				}
			}

			lines, err := missing(source)
			if err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}

			if len(lines) == 0 {
				return nil
			}

			if write {
				info, err := entry.Info()
				if err != nil {
					return err
				}

				return os.WriteFile(
					path,
					format(source, lines),
					info.Mode().Perm(),
				)
			}

			for _, line := range lines {
				fmt.Fprintf(
					os.Stderr,
					"%s:%d: blank line required before comment block\n",
					path,
					line,
				)
			}

			failed = true

			return nil
		},
	)
	if err != nil {
		return err
	}

	if failed {
		return fmt.Errorf(
			"comment spacing differs; run the repository formatter",
		)
	}

	return nil
}

func main() {
	formatter := flag.String(
		"formatter",
		"",
		"golangci wrapper for combined format checks",
	)
	write := flag.Bool("write", false, "insert missing blank lines")

	flag.Parse()

	root := "."
	if flag.NArg() > 0 {
		root = flag.Arg(0)
	}

	if err := run(root, *write, *formatter); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
