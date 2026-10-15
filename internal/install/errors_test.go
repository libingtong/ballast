package install

import (
	"errors"
	"fmt"
	"testing"

	"github.com/libingtong/ballast/internal/model"
)

// TestSentinelsAliasModel: the installer sentinels are the model values
// (contract v1.3), so errors.Is matches both spellings.
func TestSentinelsAliasModel(t *testing.T) {
	for _, c := range []struct {
		pkg, mdl error
	}{
		{ErrUsage, model.ErrInstallUsage},
		{ErrClientNotDetected, model.ErrClientNotDetected},
		{ErrBinaryUnstable, model.ErrBinaryUnstable},
		{ErrOrgWriteDenied, model.ErrOrgWriteDenied},
		{ErrLocked, model.ErrInstallLocked},
	} {
		w := fmt.Errorf("wrapped: %w", c.pkg)
		if c.pkg != c.mdl || !errors.Is(w, c.mdl) {
			t.Errorf("%v is not the model sentinel %v", c.pkg, c.mdl)
		}
	}
}
