package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
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
	socketPath := d.cfg.Socket

	if err := os.RemoveAll(socketPath); err != nil {
		return fmt.Errorf("removing existing socket: %w", err)
	}

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listening on socket %s: %w", socketPath, err)
	}

	if err := os.Chmod(socketPath, 0o660); err != nil {
		ln.Close()

		return fmt.Errorf("setting socket permissions: %w", err)
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

				continue
			}

			go d.handleConnection(ctx, conn)
		}
	}()

	slog.Info("socket listener started", "path", socketPath)

	return nil
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

		return Response{Success: true, Data: d.sm.GetAllStatuses()}

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

func writeResponse(conn net.Conn, resp Response) {
	if err := json.NewEncoder(conn).Encode(resp); err != nil {
		slog.Error("encoding socket response", "error", err)
	}
}
