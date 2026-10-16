package runner

import "github.com/libingtong/ballast/internal/sandbox"

type sandboxSpecT = sandbox.Spec

func sandboxKind(s string) sandbox.Kind { return sandbox.Kind(s) }
