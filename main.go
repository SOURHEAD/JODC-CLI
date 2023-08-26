package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/log"
	"github.com/charmbracelet/ssh"
	"github.com/charmbracelet/wish"
	bm "github.com/charmbracelet/wish/bubbletea"
	lm "github.com/charmbracelet/wish/logging"
)

type viewState int

const (
	host = "localhost"
	port = 23234
)

const (
	fileListView viewState = iota
	fileContentView
)

var (
	headerStyle = func() lipgloss.Style {
		b := lipgloss.RoundedBorder()
		b.Right = "├"
		return lipgloss.NewStyle().BorderStyle(b).Foreground(lipgloss.Color("#fcd34d")).Bold(true).Padding(0, 1)
	}()

	footerStyle = func() lipgloss.Style {
		b := lipgloss.RoundedBorder()
		b.Left = "┤"
		return headerStyle.Copy().Bold(false).BorderStyle(b)
	}()
)

type model struct {
	cursor         int
	ready          bool
	viewport       viewport.Model
	fileNames      []string
	currentView    viewState
	selectedFile   string
	fileContent    string
	terminalHeight int
}

func main() {
	s, err := wish.NewServer(
		wish.WithAddress(fmt.Sprintf("%s:%d", host, port)),
		wish.WithHostKeyPath(".ssh/term_info_ed25519"),
		wish.WithMiddleware(
			bm.Middleware(teaHandler),
			lm.Middleware(),
		),
	)
	if err != nil {
		log.Error("could not start server", "error", err)
	}

	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	log.Info("Starting SSH server", "host", host, "port", port)
	go func() {
		if err = s.ListenAndServe(); err != nil && !errors.Is(err, ssh.ErrServerClosed) {
			log.Error("could not start server", "error", err)
			done <- nil
		}
	}()

	<-done
	log.Info("Stopping SSH server")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer func() { cancel() }()
	if err := s.Shutdown(ctx); err != nil && !errors.Is(err, ssh.ErrServerClosed) {
		log.Error("could not stop server", "error", err)
	}
}

func teaHandler(s ssh.Session) (tea.Model, []tea.ProgramOption) {
	pty, _, active := s.Pty()
	if !active {
		wish.Fatalln(s, "no active terminal, skipping")
		return nil, nil
	}

	fileNames, err := readFiles("data")
	if err != nil {
		wish.Fatalln(s, "can't read directory")
		return nil, nil
	}

	m := model{
		fileNames:      fileNames,
		terminalHeight: pty.Window.Height,
	}
	return m, []tea.ProgramOption{tea.WithAltScreen(), tea.WithMouseCellMotion()}
}

func (m model) Init() tea.Cmd {
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var (
		cmd  tea.Cmd
		cmds []tea.Cmd
	)
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "up":
			if m.cursor > 0 && m.currentView == fileListView {
				m.cursor--
			}
		case "down":
			if m.cursor < len(m.fileNames) && m.currentView == fileListView {
				m.cursor++
			}
		case "enter":
			if m.currentView == fileListView {
				selectedFile := m.fileNames[m.cursor-1]
				content, err := os.ReadFile("data/" + selectedFile)
				if err != nil {
					m.fileContent = "Error reading file"
				} else {
					m.fileContent = string(content)
					m.selectedFile = selectedFile
				}
				parsedFileContent, err := glamour.Render(m.fileContent, "dark")
				if err != nil {
					m.viewport.SetContent("Error parsing markdown")
				}
				m.viewport.SetContent(parsedFileContent)
				m.currentView = fileContentView
			}
		case "esc":
			if m.currentView == fileContentView {
				m.currentView = fileListView
			}
		}
	case tea.WindowSizeMsg:
		headerHeight := lipgloss.Height(m.headerView())
		footerHeight := lipgloss.Height(m.footerView())
		verticalMarginHeight := headerHeight + footerHeight

		if !m.ready {
			m.viewport = viewport.New(msg.Width, msg.Height-verticalMarginHeight)
			m.viewport.YPosition = headerHeight
			m.viewport.HighPerformanceRendering = false
			m.ready = true
		} else {
			m.viewport.Width = msg.Width
			m.viewport.Height = msg.Height - verticalMarginHeight
		}
	}
	m.viewport, cmd = m.viewport.Update(msg)
	cmds = append(cmds, cmd)

	return m, tea.Batch(cmds...)
}

func (m model) headerView() string {
	title := headerStyle.Render(m.selectedFile)
	line := strings.Repeat("─", Max(0, m.viewport.Width-lipgloss.Width(title)))
	return lipgloss.JoinHorizontal(lipgloss.Center, title, line)
}

func (m model) footerView() string {
	info := footerStyle.Render(fmt.Sprintf("%3.f%%", m.viewport.ScrollPercent()*100))
	line := strings.Repeat("─", Max(0, m.viewport.Width-lipgloss.Width(info)))
	return lipgloss.JoinHorizontal(lipgloss.Center, line, info)
}

func (m model) View() string {
	if m.currentView == fileListView {
		s, err := glamour.Render("# Files\n", "dark")
		for i, fileName := range m.fileNames {
			selected := m.cursor == i+1
			styledFileName := renderEntry(fileName, selected)
			s += styledFileName + "\n"
		}
		s += "\n"
		s += "Press 'q' to quit\n"

		if err != nil {
			return "Error: Unable to parse markdown"
		}
		return fmt.Sprint(s)
	} else {
		return fmt.Sprintf("%s\n%s\n%s", m.headerView(), m.viewport.View(), m.footerView())
	}
}
