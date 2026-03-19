//go:build linux

package systemd

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewManagerLinux(t *testing.T) {
	t.Run("returns non-nil manager", func(t *testing.T) {
		ctx := context.Background()
		mgr := NewManager(ctx)
		require.NotNil(t, mgr)

		defer mgr.Close()
	})
}
