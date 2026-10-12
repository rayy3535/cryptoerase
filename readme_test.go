// SPDX-License-Identifier: Apache-2.0

package cryptoerase

import (
	"os"
	"strings"
	"testing"
)

// The READMEs' library example is the body of examples/library/main.go,
// which the build compiles, so the READMEs cannot drift from the API.
func TestREADMELibraryExample(t *testing.T) {
	prog, err := os.ReadFile("examples/library/main.go")
	if err != nil {
		t.Fatal(err)
	}
	var body strings.Builder
	_, main, _ := strings.Cut(string(prog), "func main() {\n")
	for line := range strings.Lines(strings.TrimSuffix(strings.TrimRight(main, "\n"), "}")) {
		body.WriteString(strings.TrimPrefix(line, "\t"))
	}
	for _, name := range []string{"README.md", "README.zh-CN.md"} {
		readme, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		_, after, ok := strings.Cut(string(readme), "```go\n")
		snippet, _, ok2 := strings.Cut(after, "```")
		if !ok || !ok2 {
			t.Fatalf("no Go code block in %s", name)
		}
		if snippet != body.String() {
			t.Errorf("%s library example differs from examples/library/main.go\n%s:\n%s\nmain.go:\n%s", name, name, snippet, body.String())
		}
	}
}
