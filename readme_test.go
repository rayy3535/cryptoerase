// SPDX-License-Identifier: Apache-2.0

package cryptoerase

import (
	"os"
	"strings"
	"testing"
)

// The README's library example is the body of examples/library/main.go,
// which the build compiles, so the README cannot drift from the API.
func TestREADMELibraryExample(t *testing.T) {
	readme, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	prog, err := os.ReadFile("examples/library/main.go")
	if err != nil {
		t.Fatal(err)
	}
	_, after, ok := strings.Cut(string(readme), "```go\n")
	snippet, _, ok2 := strings.Cut(after, "```")
	if !ok || !ok2 {
		t.Fatal("no Go code block in README.md")
	}
	var body strings.Builder
	_, main, _ := strings.Cut(string(prog), "func main() {\n")
	for line := range strings.Lines(strings.TrimSuffix(strings.TrimRight(main, "\n"), "}")) {
		body.WriteString(strings.TrimPrefix(line, "\t"))
	}
	if snippet != body.String() {
		t.Errorf("README.md library example differs from examples/library/main.go\nREADME:\n%s\nmain.go:\n%s", snippet, body.String())
	}
}
