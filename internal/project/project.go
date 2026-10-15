package project

import (
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/ports"
	"github.com/libingtong/ballast/internal/sys"
)

// Loader implements ports.ProjectLoader.
type Loader struct {
	FS sys.FS
}

var _ ports.ProjectLoader = (*Loader)(nil)

// LoadProject implements ports.ProjectLoader.
func (l *Loader) LoadProject(root string) (*model.ProjectConfig, error) {
	panic("unimplemented: project.Loader.LoadProject")
}

// Decode strictly decodes and validates project.json bytes.
func Decode(data []byte) (*model.ProjectConfig, error) {
	panic("unimplemented: project.Decode")
}

// Hash returns hex(sha256(data)) (VerifyRecord.ProjectJSONHash).
func Hash(data []byte) string {
	panic("unimplemented: project.Hash")
}

// Stack identifies a detected toolchain.
type Stack string

// The 9 stacks.
const (
	StackGo     Stack = "go"
	StackNode   Stack = "node"
	StackPython Stack = "python"
	StackRust   Stack = "rust"
	StackMaven  Stack = "maven"
	StackGradle Stack = "gradle"
	StackDotnet Stack = "dotnet"
	StackSwift  Stack = "swift"
	StackRuby   Stack = "ruby"
)

// AllStacks in detection order.
var AllStacks = []Stack{StackGo, StackNode, StackPython, StackRust, StackMaven, StackGradle, StackDotnet, StackSwift, StackRuby}

// Suggestion is one detected stack with a proposed verify entry.
type Suggestion struct {
	Stack   Stack            `json:"stack"`
	Markers []string         `json:"markers"`
	Verify  model.VerifySpec `json:"verify"`
}

// Detect inspects root (non-recursive except *.sln/*.csproj one level).
func Detect(fsys sys.FS, root string) ([]Suggestion, error) {
	panic("unimplemented: project.Detect")
}

// Propose builds a ProjectConfig from suggestions (names "test",
// "test-<stack>" when several; all required) with the default exempt list.
func Propose(s []Suggestion) *model.ProjectConfig {
	panic("unimplemented: project.Propose")
}
