package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/progress"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	titleStyle  = lipgloss.NewStyle().Bold(true)
	hintStyle   = lipgloss.NewStyle().Faint(true)
	pausedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("11"))
	logStyle    = lipgloss.NewStyle().Faint(true)
)

// bubbletea message types
type progressMsg PlaybackProgress
type logMsg string
type logDoneMsg struct{}
type doneMsg struct{}

type model struct {
	progress     progress.Model
	logs         []string
	grandElapsed int
	grandTotal   int
	paused       bool
	pauseChan    chan<- bool
	progressCh   <-chan PlaybackProgress
	logsCh       <-chan string
	width        int
	cancel       context.CancelFunc
}

func waitForProgress(ch <-chan PlaybackProgress) tea.Cmd {
	return func() tea.Msg {
		p, ok := <-ch
		if !ok {
			return doneMsg{}
		}
		return progressMsg(p)
	}
}

func waitForLog(ch <-chan string) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return logDoneMsg{}
		}
		return logMsg(msg)
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(
		waitForProgress(m.progressCh),
		waitForLog(m.logsCh),
	)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.progress.Width = msg.Width - 4
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case " ":
			if m.grandTotal > 0 {
				m.paused = !m.paused
				m.pauseChan <- m.paused
			}
		case "q", "Q", "ctrl+c":
			m.cancel()
			return m, tea.Quit
		}

	case progressMsg:
		m.grandElapsed = msg.GrandElapsed
		m.grandTotal = msg.GrandTotal
		var pct float64
		if m.grandTotal > 0 {
			pct = float64(m.grandElapsed) / float64(m.grandTotal)
			if pct > 1.0 {
				pct = 1.0
			}
		}
		cmd := m.progress.SetPercent(pct)
		return m, tea.Batch(cmd, waitForProgress(m.progressCh))

	case logMsg:
		m.logs = append(m.logs, string(msg))
		if len(m.logs) > 20 {
			m.logs = m.logs[len(m.logs)-20:]
		}
		return m, waitForLog(m.logsCh)

	case logDoneMsg:
		// logs channel closed; stop listening
		return m, nil

	case doneMsg:
		return m, tea.Quit

	case progress.FrameMsg:
		progressModel, cmd := m.progress.Update(msg)
		m.progress = progressModel.(progress.Model)
		return m, cmd
	}

	return m, nil
}

func (m model) View() string {
	remaining := m.grandTotal - m.grandElapsed
	if remaining < 0 {
		remaining = 0
	}

	var pausedStr string
	if m.paused {
		pausedStr = "  " + pausedStyle.Render("[PAUSED]")
	}

	var hint string
	if m.grandTotal == 0 {
		hint = hintStyle.Render("synthesizing...   [Q] quit")
	} else {
		hint = hintStyle.Render("[SPACE] pause/resume   [Q] quit")
	}
	header := titleStyle.Render("text2speech") + "  " + hint
	timeStr := fmt.Sprintf("Elapsed: %s   Remaining: %s%s",
		formatDuration(m.grandElapsed),
		formatDuration(remaining),
		pausedStr,
	)

	var logBuf strings.Builder
	for _, l := range m.logs {
		logBuf.WriteString(l)
	}

	return "\n" + header + "\n\n" +
		m.progress.View() + "\n" +
		timeStr + "\n\n" +
		logStyle.Render(logBuf.String())
}

func formatDuration(seconds int) string {
	m := seconds / 60
	s := seconds % 60
	return fmt.Sprintf("%d:%02d", m, s)
}

// NewDashboard creates and runs the bubbletea TUI. It blocks until the user
// quits or playback completes.
func NewDashboard(ctx context.Context, cancel context.CancelFunc, playbackProgress <-chan PlaybackProgress, logs <-chan string, pauseChan chan<- bool) error {
	m := model{
		progress:   progress.New(progress.WithDefaultGradient()),
		progressCh: playbackProgress,
		logsCh:     logs,
		pauseChan:  pauseChan,
		cancel:     cancel,
	}
	prog := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx))
	_, err := prog.Run()
	if errors.Is(err, tea.ErrProgramKilled) {
		// context was cancelled by the completion/error path — not a real error
		return nil
	}
	return err
}
