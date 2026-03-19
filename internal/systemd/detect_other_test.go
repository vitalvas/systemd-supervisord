//go:build !linux

package systemd

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewManagerOther(t *testing.T) {
	t.Run("returns CtlManager on non-linux", func(t *testing.T) {
		ctx := context.Background()
		mgr := NewManager(ctx)
		require.NotNil(t, mgr)

		defer mgr.Close()

		_, ok := mgr.(*CtlManager)
		assert.True(t, ok, "expected *CtlManager on non-linux platform")
	})
}
