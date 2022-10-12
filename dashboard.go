package main

import (
	"context"
	"os"

	"github.com/mum4k/termdash"
	"github.com/mum4k/termdash/cell"
	"github.com/mum4k/termdash/container"
	"github.com/mum4k/termdash/linestyle"
	"github.com/mum4k/termdash/terminal/tcell"
	"github.com/mum4k/termdash/terminal/terminalapi"
	"github.com/mum4k/termdash/widgets/gauge"
	"github.com/mum4k/termdash/widgets/text"
	"github.com/sirupsen/logrus"
	log "github.com/sirupsen/logrus"
)

// Errorlog allows us to capture errors without getting trapped by the dashboard
var Errorlog = logrus.New()

func init() {
	file, err := os.OpenFile("dashboard_error.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err == nil {
		Errorlog.Out = file
	} else {
		log.Info("Failed to log to file, using default stderr")
	}
}

type Dashboard struct {
	Logs        *text.Text
	ProgressBar *gauge.Gauge
}

func NewDashboard(ctx context.Context, cancel context.CancelFunc, playbackProgress chan PlaybackProgress, logs chan string) (*Dashboard, error) {
	var dashboard = new(Dashboard)

	var term, err = tcell.New()
	if err != nil {
		return nil, err
	}
	defer term.Close()

	dashboard.ProgressBar, err = gauge.New(
		gauge.Height(1),
		gauge.Color(cell.ColorNumber(33)),
		gauge.Border(linestyle.Light),
		gauge.BorderTitle("Section progress"),
	)

	dashboard.Logs, err = text.New(text.RollContent(), text.WrapAtWords())
	if err != nil {
		return nil, err
	}

	c, err := container.New(
		term,
		container.Border(linestyle.Light),
		container.BorderTitle("PRESS Q TO QUIT"),
		container.SplitHorizontal(
			container.Top(
				container.PlaceWidget(dashboard.ProgressBar),
			),
			container.Bottom(
				container.Border(linestyle.Light),
				container.BorderTitle("Logs"),
				container.PlaceWidget(dashboard.Logs),
			),
		),
	)
	if err != nil {
		return nil, err
	}

	quitter := func(k *terminalapi.Keyboard) {
		if k.Key == 'q' || k.Key == 'Q' {
			term.Close()
		}
	}

	go dashboard.SetProgress(playbackProgress)
	go dashboard.WriteLogMessage(logs)

	if err := termdash.Run(ctx, term, c, termdash.KeyboardSubscriber(quitter)); err != nil {
		log.Fatalf("Error running dashboard, err: %s", err.Error())
	}

	return dashboard, nil
}

func (d *Dashboard) SetProgress(playbackProgress chan PlaybackProgress) {
	for progress := range playbackProgress {
		if err := d.ProgressBar.Absolute(progress.Current, progress.Total); err != nil {
			Errorlog.Errorf("Progress: %s, err: %v", progress.String(), err)
		}
	}
}

func (d *Dashboard) WriteLogMessage(logs chan string) {
	for logMessage := range logs {
		if err := d.Logs.Write(logMessage); err != nil {
			Errorlog.Errorf("Log: %s, err: %v", logMessage, err)
		}
	}
}
