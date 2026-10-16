//go:build !unix

package runner

// No O_NOFOLLOW / O_NONBLOCK here; origin=hook jobs never run on these
// platforms (no sandbox backend), the code only has to compile.
const (
	openFlagsSource = 0
	openFlagsLeaf   = 0
)
