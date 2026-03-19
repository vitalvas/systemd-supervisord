package cli

import (
	"encoding/json"
	"fmt"
	"net"
	"time"

	"github.com/vitalvas/systemd-supervisord/internal/daemon"
)

func SendRequest(socketPath string, req daemon.Request) (*daemon.Response, error) {
	conn, err := net.DialTimeout("unix", socketPath, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("connecting to daemon: %w", err)
	}
	defer conn.Close()

	if err := conn.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		return nil, fmt.Errorf("setting deadline: %w", err)
	}

	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return nil, fmt.Errorf("sending request: %w", err)
	}

	var resp daemon.Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	return &resp, nil
}
