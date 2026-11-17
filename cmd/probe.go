package cmd

import (
	"context"
	"errors"
	"os/signal"
	"syscall"
	"time"

	"github.com/lighttous/ppanel-node/probeagent"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

var (
	probeAPIHost      string
	probeToken        string
	probePollInterval time.Duration
)

var probeCommand = cobra.Command{
	Use:   "probe",
	Short: "Run a mainland connectivity probe agent",
	Args:  cobra.NoArgs,
	RunE:  runProbe,
}

func init() {
	probeCommand.Flags().StringVar(&probeAPIHost, "api-host", "", "PPanel API base URL")
	probeCommand.Flags().StringVar(&probeToken, "token", "", "probe agent bearer token")
	probeCommand.Flags().DurationVar(
		&probePollInterval,
		"poll-interval",
		5*time.Second,
		"task polling interval",
	)
	_ = probeCommand.MarkFlagRequired("api-host")
	_ = probeCommand.MarkFlagRequired("token")
	command.AddCommand(&probeCommand)
}

func runProbe(_ *cobra.Command, _ []string) error {
	showVersion()
	client, err := probeagent.NewClient(probeAPIHost, probeToken)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	log.WithField("api_host", probeAPIHost).Info("starting connectivity probe agent")
	err = probeagent.NewRunner(client, probePollInterval).Run(ctx)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
