package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"organize/components"
	"organize/utils"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
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
	host = "0.0.0.0"
	port = 23234
)

const (
	fileListView viewState = iota
	fileContentView
)

type Model struct {
	cursor           int
	ready            bool
	viewport         viewport.Model
	fileNames        []string
	fileDescriptions []string
	currentView      viewState
	selectedFileName string
	fileContent      string
	terminalHeight   int
	help             help.Model
	keys             keyMap
	catimgOutput     string
	qrOutput         string
}

func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Up, k.Down, k.Quit, k.Back}
}

func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.Left, k.Right, k.Quit, k.Back},
	}
}

func runCatimg(imagePath string, height, padding int) (string, error) {
	cmd := exec.Command("catimg", imagePath, "-H", fmt.Sprintf("%d", height))
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	if err != nil {
		return "", err
	}

	// Split the output into lines
	lines := strings.Split(out.String(), "\n")

	// Add padding to the left of each line
	paddedLines := make([]string, len(lines))
	for i, line := range lines {
		paddedLines[i] = strings.Repeat(" ", padding) + line
	}

	// Join the padded lines back into a single string
	paddedOutput := strings.Join(paddedLines, "\n")

	return paddedOutput, nil
}

func runqr(padding int) (string, error) {
	cmd := exec.Command("qrencode", "-m", "2", "-t", "utf8", "https://discord.gg/WW2sttvbVG")
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	if err != nil {
		return "", err
	}

	// Split the output into lines
	lines := strings.Split(out.String(), "\n")

	// Add padding to the left of each line
	paddedLines := make([]string, len(lines))
	for i, line := range lines {
		paddedLines[i] = strings.Repeat(" ", padding) + line
	}

	// Join the padded lines back into a single string
	paddedOutput := strings.Join(paddedLines, "\n")

	return paddedOutput, nil
}

func main() {
	sshFolderPath := os.Getenv("SSH_FOLDER_PATH")
	if sshFolderPath == "" {
		sshFolderPath = ".ssh"
	}

	s, err := wish.NewServer(
		wish.WithAddress(fmt.Sprintf("%s:%d", host, port)),
		wish.WithHostKeyPath(fmt.Sprintf("%s/term_info_ed25519", sshFolderPath)),
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
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

	positionMeta, err := utils.GetPositionMeta("directory")
	if err != nil {
		wish.Fatalln(s, "can't read directory: "+err.Error())
		return nil, nil
	}

	// Capture catimg output
	catimgOutput, err := runCatimg("jodc_logo.jpeg", 15, 2)
	if err != nil {
		wish.Fatalln(s, "failed to run catimg: "+err.Error())
		return nil, nil
	}

	// Capture qrencode output
	qrOutput, err := runqr(2)
	if err != nil {
		wish.Fatalln(s, "failed to run qrencode: "+err.Error())
		return nil, nil
	}

	m := Model{
		fileNames:        positionMeta.FileNames,
		fileDescriptions: positionMeta.FileDescriptions,
		terminalHeight:   pty.Window.Height,
		help:             help.New(),
		keys:             keys,
		catimgOutput:     catimgOutput,
		qrOutput:         qrOutput,
	}
	return m, []tea.ProgramOption{tea.WithAltScreen(), tea.WithMouseCellMotion()}
}

func (m Model) Init() tea.Cmd {
	return nil
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var (
		cmd  tea.Cmd
		cmds []tea.Cmd
	)
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, m.keys.Quit):
			return m, tea.Quit
		case key.Matches(msg, m.keys.Up):
			if m.cursor > 0 && m.currentView == fileListView {
				m.cursor--
			}
		case key.Matches(msg, m.keys.Down):
			if m.cursor < len(m.fileNames)-1 && m.currentView == fileListView {
				m.cursor++
			}

		case key.Matches(msg, m.keys.Top):
			m.viewport.GotoTop()
		case key.Matches(msg, m.keys.Enter):
			if m.currentView == fileListView {
				selectedFile := m.fileNames[m.cursor]
				content, err := os.ReadFile("directory/" + selectedFile)
				if err != nil {
					m.fileContent = "Error reading file"
				} else {
					fileContent := string(content)
					m.fileContent = strings.Join(strings.Split(fileContent, "\n")[2:], "\n")
					m.selectedFileName = selectedFile
				}
				parsedFileContent, err := glamour.Render(m.fileContent, "dark")
				if err != nil {
					m.viewport.SetContent("Error parsing markdown")
				}
				m.viewport.SetContent(parsedFileContent)
				m.currentView = fileContentView
				m.viewport.GotoTop()
			}
		case key.Matches(msg, m.keys.Back):
			if m.currentView == fileContentView {
				m.currentView = fileListView
				m.viewport.GotoTop()
			}
		}
	case tea.WindowSizeMsg:
		m.help.Width = msg.Width

		headerHeight := lipgloss.Height(m.HeaderView())
		footerHeight := lipgloss.Height(m.FooterView())
		verticalMarginHeight := headerHeight + footerHeight

		if (!m.ready) {
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

func (m Model) HeaderView() string {
	title := components.HeaderStyle.Render(m.selectedFileName)
	line := strings.Repeat(lipgloss.NewStyle().
		Foreground(lipgloss.Color("#fcd34d")).
		Render("─"), utils.Max(0, m.viewport.Width-lipgloss.Width(title)))
	return lipgloss.JoinHorizontal(lipgloss.Center, title, line)
}

func (m Model) FooterView() string {
	helpView := lipgloss.PlaceHorizontal(m.viewport.Width, lipgloss.Right, m.help.View(m.keys))

	info := components.FooterStyle.Render(fmt.Sprintf("%3.f%%", m.viewport.ScrollPercent()*100))
	line := strings.Repeat(lipgloss.NewStyle().
		Foreground(lipgloss.Color("#fcd34d")).
		Render("─"), utils.Max(0, m.viewport.Width-lipgloss.Width(info)))
	footerInfo := lipgloss.JoinHorizontal(lipgloss.Center, line, info)

	return helpView + "\n" + footerInfo
}

func (m Model) View() string {
	if m.currentView == fileListView {
		s := components.TextWithBackgroundView("#fcd34d", " __THE_SUPREME_AND_POWERFUL_JODC_GANG__ ", true, false)
		// Add catimg and qrencode outputs side-by-side
		s += lipgloss.JoinHorizontal(lipgloss.Top, m.catimgOutput, m.qrOutput) + "\n"
		s += components.IntroDescriptionView(m.viewport.Width)
		s += components.OpenPositionsGrid(m.viewport.Width, m.fileNames, m.fileDescriptions, m.cursor)
		s += "\n"

		return fmt.Sprint(s)
	} else {
		return fmt.Sprintf("%s\n%s\n%s", m.HeaderView(), m.viewport.View(), m.FooterView())
	}
}

