package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"time"

	"github.com/coreos/go-systemd/v22/activation"
)

type Request struct {
	Command  string `json:"command"`
	UnitName string `json:"unit_name,omitempty"`
}

type Response struct {
	Success bool        `json:"success"`
	Error   string      `json:"error,omitempty"`
	Data    interface{} `json:"data,omitempty"`
}

func (d *Daemon) ListenSocket(ctx context.Context) error {
	ln, err := d.acquireListener()
	if err != nil {
		return err
	}

	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				if ctx.Err() != nil {
					return
				}

				slog.Error("accepting socket connection", "error", err)

				time.Sleep(100 * time.Millisecond)

				continue
			}

			go d.handleConnection(ctx, conn)
		}
	}()

	return nil
}

func (d *Daemon) acquireListener() (net.Listener, error) {
	listeners, err := activation.Listeners()
	if err != nil {
		return nil, fmt.Errorf("checking socket activation: %w", err)
	}

	if len(listeners) > 0 {
		slog.Info("using socket activation", "count", len(listeners))

		return listeners[0], nil
	}

	return d.createListener()
}

func (d *Daemon) createListener() (net.Listener, error) {
	socketPath := d.cfg.Socket

	if err := removeStaleSocket(socketPath); err != nil {
		return nil, fmt.Errorf("removing existing socket: %w", err)
	}

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("listening on socket %s: %w", socketPath, err)
	}

	if err := os.Chmod(socketPath, 0o660); err != nil {
		ln.Close()

		return nil, fmt.Errorf("setting socket permissions: %w", err)
	}

	slog.Info("socket listener started", "path", socketPath)

	return ln, nil
}

func (d *Daemon) handleConnection(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	var req Request
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		slog.Error("decoding socket request", "error", err)
		writeResponse(conn, Response{Success: false, Error: "invalid request"})

		return
	}

	resp := d.processRequest(ctx, req)

	writeResponse(conn, resp)
}

func (d *Daemon) processRequest(ctx context.Context, req Request) Response {
	switch req.Command {
	case "list":
		d.mu.Lock()
		units := make([]string, 0, len(d.registeredUnits))
		for name := range d.registeredUnits {
			units = append(units, name)
		}
		d.mu.Unlock()

		return Response{Success: true, Data: units}

	case "status":
		if req.UnitName != "" {
			status := d.sm.GetStatus(req.UnitName)
			if status == nil {
				return Response{Success: false, Error: fmt.Sprintf("unit %s not found", req.UnitName)}
			}

			return Response{Success: true, Data: status}
		}

		return Response{Success: true, Data: d.sm.AllStatuses()}

	case "start":
		if req.UnitName == "" {
			return Response{Success: false, Error: "unit name required"}
		}

		if err := d.mgr.Start(ctx, req.UnitName); err != nil {
			return Response{Success: false, Error: err.Error()}
		}

		return Response{Success: true}

	case "stop":
		if req.UnitName == "" {
			return Response{Success: false, Error: "unit name required"}
		}

		if err := d.mgr.Stop(ctx, req.UnitName); err != nil {
			return Response{Success: false, Error: err.Error()}
		}

		return Response{Success: true}

	case "restart":
		if req.UnitName == "" {
			return Response{Success: false, Error: "unit name required"}
		}

		if err := d.mgr.Restart(ctx, req.UnitName); err != nil {
			return Response{Success: false, Error: err.Error()}
		}

		return Response{Success: true}

	default:
		return Response{Success: false, Error: fmt.Sprintf("unknown command: %s", req.Command)}
	}
}

func removeStaleSocket(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}

	if err != nil {
		return err
	}

	if info.IsDir() {
		return fmt.Errorf("path is a directory: %s", path)
	}

	if info.Mode().Type()&os.ModeSocket == 0 {
		return fmt.Errorf("path is not a unix socket: %s", path)
	}

	return os.Remove(path)
}

func writeResponse(conn net.Conn, resp Response) {
	if err := json.NewEncoder(conn).Encode(resp); err != nil {
		slog.Error("encoding socket response", "error", err)
	}
}
