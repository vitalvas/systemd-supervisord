//go:build linux

package systemd

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStringProp(t *testing.T) {
	t.Run("existing string key", func(t *testing.T) {
		props := map[string]interface{}{
			"ActiveState": "active",
		}
		assert.Equal(t, "active", stringProp(props, "ActiveState"))
	})

	t.Run("missing key returns empty string", func(t *testing.T) {
		props := map[string]interface{}{
			"ActiveState": "active",
		}
		assert.Equal(t, "", stringProp(props, "NonExistent"))
	})

	t.Run("non-string value returns empty string", func(t *testing.T) {
		props := map[string]interface{}{
			"PID": 1234,
		}
		assert.Equal(t, "", stringProp(props, "PID"))
	})

	t.Run("nil value returns empty string", func(t *testing.T) {
		props := map[string]interface{}{
			"Key": nil,
		}
		assert.Equal(t, "", stringProp(props, "Key"))
	})

	t.Run("empty map", func(t *testing.T) {
		props := map[string]interface{}{}
		assert.Equal(t, "", stringProp(props, "Any"))
	})

	t.Run("empty string value", func(t *testing.T) {
		props := map[string]interface{}{
			"Description": "",
		}
		assert.Equal(t, "", stringProp(props, "Description"))
	})

	t.Run("bool value returns empty string", func(t *testing.T) {
		props := map[string]interface{}{
			"Flag": true,
		}
		assert.Equal(t, "", stringProp(props, "Flag"))
	})

	t.Run("multiple keys", func(t *testing.T) {
		props := map[string]interface{}{
			"ActiveState": "active",
			"SubState":    "running",
			"LoadState":   "loaded",
		}
		assert.Equal(t, "active", stringProp(props, "ActiveState"))
		assert.Equal(t, "running", stringProp(props, "SubState"))
		assert.Equal(t, "loaded", stringProp(props, "LoadState"))
	})
}
