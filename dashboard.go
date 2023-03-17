package main

import (
	"context"
	"fmt"
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

// Errorlog allows us to capture errors without getting trapped by the dashboard.
// Hopefully this can be removed when all the bugs are eradicated.
var Errorlog = logrus.New()

func init() {
	file, err := os.OpenFile("dashboard_error.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err == nil {
		Errorlog.Out = file
	} else {
		log.Info("Failed to log to file, using default stderr")
	}
}

// Dashboard is our terminal visualization
type Dashboard struct {
	Logs        *text.Text
	ProgressBar *gauge.Gauge
}

// NewDashboard is a constructor that builds the terminal dashboard as well as spins up two goroutines (below) that
// receive data from the two channels passed in.
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
	if err != nil {
		return nil, err
	}

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
		return nil, fmt.Errorf("Error running dashboard, err: %s", err.Error())
	}

	return dashboard, nil
}

// SetProgress gets playback progress by reading from the given channel and
// updating the dashboard progress bar accordingly.
func (d *Dashboard) SetProgress(playbackProgress chan PlaybackProgress) {
	for progress := range playbackProgress {
		var pct float64
		// dont divide by 0
		if progress.Current > 0 && progress.Total > 0 {
			pct = (float64(progress.Current) / float64(progress.Total)) * 100.0
		}
		if err := d.ProgressBar.Percent(int(pct)); err != nil {
			Errorlog.Errorf("Progress: %s, err: %v", progress.String(), err)
		}
	}
}

// WriteLogMessage gets log messages (mostly regarding polly processing status)
// by reading from the given channel and updating the dashboard text box accordingly.
func (d *Dashboard) WriteLogMessage(logs chan string) {
	for logMessage := range logs {
		if err := d.Logs.Write(logMessage); err != nil {
			Errorlog.Errorf("Log: %s, err: %v", logMessage, err)
		}
	}
}
