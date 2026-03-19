//go:build !linux

package systemd

import "context"

func NewManager(_ context.Context) Manager {
	return NewCtlManager()
}
