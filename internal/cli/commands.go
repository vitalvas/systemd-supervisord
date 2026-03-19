package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/vitalvas/systemd-supervisord/internal/config"
	"github.com/vitalvas/systemd-supervisord/internal/daemon"
	"github.com/vitalvas/systemd-supervisord/internal/statemanager"
)

func NewRootCommand() *cobra.Command {
	var configPath string
	var socketPath string

	rootCmd := &cobra.Command{
		Use:          "systemd-supervisord",
		SilenceUsage: true,
	}

	rootCmd.PersistentFlags().StringVarP(&configPath, "config", "c", "/etc/systemd-supervisord/config.yaml", "config file path")
	rootCmd.PersistentFlags().StringVarP(&socketPath, "socket", "s", "/var/run/systemd-supervisord.sock", "daemon socket path")

	rootCmd.AddCommand(
		newRunCmd(&configPath),
		newListCmd(&socketPath),
		newStatusCmd(&socketPath),
		newStartCmd(&socketPath),
		newStopCmd(&socketPath),
		newRestartCmd(&socketPath),
		newCheckCmd(&configPath),
	)

	return rootCmd
}

func newRunCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use: "run",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return daemon.New(*configPath).Run(cmd.Context())
		},
	}
}

func newListCmd(socketPath *string) *cobra.Command {
	return &cobra.Command{
		Use: "list",
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := SendRequest(*socketPath, daemon.Request{Command: "list"})
			if err != nil {
				return err
			}

			if !resp.Success {
				return fmt.Errorf("%s", resp.Error)
			}

			return PrintUnitList(cmd.OutOrStdout(), resp.Data)
		},
	}
}

func newStatusCmd(socketPath *string) *cobra.Command {
	return &cobra.Command{
		Use:  "status [unit]",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := daemon.Request{Command: "status"}
			if len(args) > 0 {
				req.UnitName = args[0]
			}

			resp, err := SendRequest(*socketPath, req)
			if err != nil {
				return err
			}

			if !resp.Success {
				return fmt.Errorf("%s", resp.Error)
			}

			if req.UnitName != "" {
				return PrintUnitStatus(cmd.OutOrStdout(), resp.Data)
			}

			return PrintAllStatuses(cmd.OutOrStdout(), resp.Data)
		},
	}
}

func newStartCmd(socketPath *string) *cobra.Command {
	return &cobra.Command{
		Use:  "start <unit>",
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return sendUnitCommand(*socketPath, "start", args[0])
		},
	}
}

func newStopCmd(socketPath *string) *cobra.Command {
	return &cobra.Command{
		Use:  "stop <unit>",
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return sendUnitCommand(*socketPath, "stop", args[0])
		},
	}
}

func newRestartCmd(socketPath *string) *cobra.Command {
	return &cobra.Command{
		Use:  "restart <unit>",
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return sendUnitCommand(*socketPath, "restart", args[0])
		},
	}
}

func newCheckCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use: "check",
		RunE: func(_ *cobra.Command, _ []string) error {
			_, err := config.Load(*configPath)
			if err != nil {
				return fmt.Errorf("config validation failed: %w", err)
			}

			slog.Info("configuration is valid", "path", *configPath)

			return nil
		},
	}
}

func sendUnitCommand(socketPath, command, unitName string) error {
	resp, err := SendRequest(socketPath, daemon.Request{
		Command:  command,
		UnitName: unitName,
	})
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf("%s", resp.Error)
	}

	fmt.Printf("%s: %s\n", command, unitName)

	return nil
}

func PrintUnitList(w io.Writer, data interface{}) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}

	var units []string
	if err := json.Unmarshal(raw, &units); err != nil {
		return err
	}

	for _, name := range units {
		fmt.Fprintln(w, name)
	}

	return nil
}

func PrintUnitStatus(w io.Writer, data interface{}) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}

	var status statemanager.UnitStatus
	if err := json.Unmarshal(raw, &status); err != nil {
		return err
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)

	fmt.Fprintf(tw, "Unit:\t%s\n", status.UnitName)
	fmt.Fprintf(tw, "State:\t%s (%s)\n", status.ActiveState, status.SubState)

	if status.Healthy != nil {
		fmt.Fprintf(tw, "Healthy:\t%t\n", *status.Healthy)
	} else {
		fmt.Fprintf(tw, "Healthy:\tn/a\n")
	}

	fmt.Fprintf(tw, "Restarts:\t%d\n", status.RestartCount)

	if !status.LastTransition.IsZero() {
		fmt.Fprintf(tw, "Last Transition:\t%s\n", status.LastTransition.Format("2006-01-02 15:04:05"))
	}

	return tw.Flush()
}

func PrintAllStatuses(w io.Writer, data interface{}) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}

	var statuses []statemanager.UnitStatus
	if err := json.Unmarshal(raw, &statuses); err != nil {
		return err
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)

	fmt.Fprintf(tw, "UNIT\tSTATE\tHEALTHY\tRESTARTS\n")

	for _, s := range statuses {
		health := "n/a"
		if s.Healthy != nil {
			if *s.Healthy {
				health = "true"
			} else {
				health = "false"
			}
		}

		fmt.Fprintf(tw, "%s\t%s/%s\t%s\t%d\n", s.UnitName, s.ActiveState, s.SubState, health, s.RestartCount)
	}

	return tw.Flush()
}
