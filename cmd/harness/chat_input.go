package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

// chatInput reads user prompts from either a plain stream or an interactive
// TTY.
type chatInput interface {
	// ReadLine returns the next submitted prompt line.
	ReadLine() (string, bool, error)

	// Close restores any terminal state owned by the input reader.
	Close() error
}

// chatLineResult carries one submitted prompt or input failure.
type chatLineResult struct {
	// Line is the submitted user prompt.
	Line string

	// OK reports whether a line was read before EOF.
	OK bool

	// Err stores any input failure.
	Err error
}

// scannerChatInput reads newline-delimited prompts from non-interactive input.
type scannerChatInput struct {
	// scanner reads complete newline-delimited prompts.
	scanner *bufio.Scanner

	// stdout receives the plain prompt marker.
	stdout io.Writer
}

// terminalChatInput reads prompts through a tiny raw-mode terminal editor.
type terminalChatInput struct {
	// mu serializes prompt redraws with model and tool output.
	mu sync.Mutex

	// stdin is the terminal input device.
	stdin *os.File

	// stdout receives rendered prompt islands.
	stdout io.Writer

	// rendered reports whether a prompt island is currently on screen.
	rendered bool

	// lastRows is the row count occupied by the last prompt island.
	lastRows int

	// cursorRow is the cursor row within the rendered composer block.
	cursorRow int

	// lastWidth is the terminal width used by the last render.
	lastWidth int

	// lastInputRows stores the wrapped input rows from the last render.
	lastInputRows []string

	// input stores the currently edited prompt runes.
	input []rune

	// footerText stores the metadata row shown below the prompt island.
	footerText string

	// statusText stores the transient working status above the prompt.
	statusText string

	// statusFrame stores the current status animation frame.
	statusFrame int

	// statusStartedAt stores when the status line began.
	statusStartedAt time.Time
}

// errChatInputInterrupted reports an explicit interactive input interruption.
var errChatInputInterrupted = errors.New("chat input interrupted")

// newChatInput selects the richest prompt reader supported by the streams.
func newChatInput(stdin io.Reader, stdout io.Writer) chatInput {
	stdinFile, ok := stdin.(*os.File)
	if ok && shouldStyle(stdout) && isTerminalFile(stdinFile) {
		return &terminalChatInput{
			stdin:  stdinFile,
			stdout: stdout,
		}
	}

	scanner := bufio.NewScanner(stdin)
	scanner.Buffer(make([]byte, 0, 4096), 1024*1024)

	return &scannerChatInput{
		scanner: scanner,
		stdout:  stdout,
	}
}

// readChatLines continuously reads prompt submissions into a channel.
func readChatLines(input chatInput) <-chan chatLineResult {
	results := make(chan chatLineResult, 16)
	go func() {
		defer close(results)
		for {
			line, ok, err := input.ReadLine()
			results <- chatLineResult{
				Line: line,
				OK:   ok,
				Err:  err,
			}
			if err != nil || !ok {
				return
			}
		}
	}()

	return results
}

// terminalComposer returns input as a terminal composer when available.
func terminalComposer(input chatInput) *terminalChatInput {
	composer, ok := input.(*terminalChatInput)
	if !ok {
		return nil
	}

	return composer
}

// ReadLine renders a plain prompt and reads one newline-delimited prompt.
func (i *scannerChatInput) ReadLine() (string, bool, error) {
	printChatPrompt(i.stdout)
	if !i.scanner.Scan() {
		if err := i.scanner.Err(); err != nil {
			return "", false, err
		}

		return "", false, nil
	}
	resetChatPrompt(i.stdout)

	return i.scanner.Text(), true, nil
}

// Close releases scanner input resources.
func (i *scannerChatInput) Close() error {
	return nil
}

// ReadLine reads one submitted prompt through a raw terminal prompt editor.
func (i *terminalChatInput) ReadLine() (string, bool, error) {
	previous, err := enableRawTerminal(i.stdin)
	if err != nil {
		return "", false, err
	}
	defer func() {
		_ = restoreTerminal(i.stdin, previous)
	}()

	reader := bufio.NewReader(i.stdin)
	i.mu.Lock()
	i.input = i.input[:0]
	err = i.renderLocked()
	i.mu.Unlock()
	if err != nil {
		return "", false, err
	}
	stopResize := i.watchResize()
	defer stopResize()
	for {
		r, _, err := reader.ReadRune()
		if err != nil {
			return "", false, err
		}
		switch r {
		case '\n', '\r':
			i.mu.Lock()
			line := i.submitLocked()
			i.mu.Unlock()

			return line, true, nil

		case '\x03':
			i.mu.Lock()
			i.finishLocked()
			i.mu.Unlock()

			return "", false, errChatInputInterrupted

		case '\x04':
			i.mu.Lock()
			empty := len(i.input) == 0
			if empty {
				i.finishLocked()
			}
			i.mu.Unlock()
			if empty {
				return "", false, nil
			}

		case '\x7f', '\b':
			i.mu.Lock()
			if len(i.input) > 0 {
				i.input = i.input[:len(i.input)-1]
			}
			err = i.renderLocked()
			i.mu.Unlock()

		case '\x1b':
			continue

		default:
			if isPromptRune(r) {
				i.mu.Lock()
				i.input = append(i.input, r)
				err = i.renderLocked()
				i.mu.Unlock()
			}
		}
		if err != nil {
			return "", false, err
		}
	}
}

// Close restores any terminal state owned by the input reader.
func (i *terminalChatInput) Close() error {
	return nil
}

// watchResize redraws the active prompt when the terminal changes size.
func (i *terminalChatInput) watchResize() func() {
	signals := make(chan os.Signal, 1)
	done := make(chan struct{})
	var once sync.Once
	signal.Notify(signals, syscall.SIGWINCH)
	go func() {
		defer signal.Stop(signals)
		for {
			select {
			case <-signals:
				i.mu.Lock()
				if i.rendered {
					_ = i.renderLocked()
				}
				i.mu.Unlock()

			case <-done:
				return
			}
		}
	}()

	return func() {
		once.Do(func() {
			close(done)
		})
	}
}

// WithOutput temporarily clears the active prompt while output is written.
func (i *terminalChatInput) WithOutput(write func()) {
	i.mu.Lock()
	if !i.rendered {
		write()
		i.mu.Unlock()

		return
	}
	width := terminalWidth(i.stdout)
	i.clearLocked(width)
	i.clearRenderStateLocked()
	write()
	_ = i.renderLocked()
	i.mu.Unlock()
}

// SetFooter updates the metadata row below the prompt island.
func (i *terminalChatInput) SetFooter(text string) {
	i.mu.Lock()
	i.footerText = text
	if i.rendered {
		_ = i.renderLocked()
	}
	i.mu.Unlock()
}

// SetStatus updates the working status line above the prompt island.
func (i *terminalChatInput) SetStatus(text string) {
	i.mu.Lock()
	if i.statusStartedAt.IsZero() {
		i.statusStartedAt = time.Now()
	}
	i.statusText = text
	_ = i.renderLocked()
	i.mu.Unlock()
}

// AdvanceStatus moves the status animation forward by one frame.
func (i *terminalChatInput) AdvanceStatus() {
	i.mu.Lock()
	i.statusFrame++
	_ = i.renderLocked()
	i.mu.Unlock()
}

// ClearStatus removes the working status line above the prompt island.
func (i *terminalChatInput) ClearStatus() {
	i.mu.Lock()
	i.statusText = ""
	i.statusFrame = 0
	i.statusStartedAt = time.Time{}
	_ = i.renderLocked()
	i.mu.Unlock()
}

// renderLocked redraws the prompt island for the current input text.
func (i *terminalChatInput) renderLocked() error {
	width := terminalWidth(i.stdout)
	inputRows := wrappedPromptRows(i.input, width)
	rows := i.renderedComposerRows(inputRows, width)
	if i.rendered && width == i.lastWidth && len(rows) == i.lastRows {
		i.overwriteLocked(rows)
	} else {
		if i.rendered {
			i.clearLocked(width)
		}
		i.writeComposerRowsLocked(rows)
	}

	i.rendered = true
	i.lastRows = len(rows)
	i.lastWidth = width
	i.lastInputRows = append(i.lastInputRows[:0], inputRows...)
	i.moveToPromptCursorLocked(i.input, width, len(inputRows))

	return nil
}

// renderedComposerRows builds the visible rows owned by the live composer.
func (i *terminalChatInput) renderedComposerRows(inputRows []string,
	width int) []string {

	rows := make(
		[]string, 0,
		composerRowCount(
			len(inputRows), i.statusText != "", i.footerText != "",
		),
	)
	if i.statusText != "" {
		rows = append(
			rows, ansiReset+strings.Repeat(" ", width),
			statusComposerLine(
				i.statusFrame, i.statusText, i.statusStartedAt,
				width,
			),
			ansiReset+strings.Repeat(" ", width),
		)
	}
	rows = append(rows, promptIslandRowWithTextWidth("", width))
	for _, row := range inputRows {
		rows = append(rows, promptIslandRowWithTextWidth(row, width))
	}
	rows = append(rows, promptIslandRowWithTextWidth("", width))
	if i.footerText != "" {
		rows = append(rows, footerComposerLine(i.footerText, width))
	}

	return rows
}

// writeComposerRowsLocked writes composer rows from the current cursor row.
func (i *terminalChatInput) writeComposerRowsLocked(rows []string) {
	for index, row := range rows {
		if index > 0 {
			fmt.Fprint(i.stdout, "\n")
		}
		fmt.Fprintf(i.stdout, "\r%s", row)
	}
}

// overwriteLocked refreshes a same-height composer without blanking it first.
func (i *terminalChatInput) overwriteLocked(rows []string) {
	fmt.Fprintf(i.stdout, "\r%s", ansiMoveUp(i.cursorRow-1))
	i.writeComposerRowsLocked(rows)
}

// clearRenderStateLocked forgets rendered geometry after erasing the composer.
func (i *terminalChatInput) clearRenderStateLocked() {
	i.rendered = false
	i.lastRows = 0
	i.cursorRow = 0
	i.lastWidth = 0
	i.lastInputRows = i.lastInputRows[:0]
}

// moveToPromptCursor places the cursor on the current input row.
func (i *terminalChatInput) moveToPromptCursorLocked(input []rune, width int,
	inputRows int) {

	cursorCol := promptCursorColumn(input, width)
	i.cursorRow = composerPrefixRows(i.statusText != "") + inputRows + 1
	fmt.Fprintf(
		i.stdout, "\r%s%s", ansiMoveUp(i.lastRows-i.cursorRow),
		ansiMoveRight(cursorCol),
	)
}

// clear removes the previously rendered prompt island from the terminal.
func (i *terminalChatInput) clearLocked(width int) {
	if i.lastRows <= 0 {
		return
	}

	rows := reflowedComposerRows(i.lastRows, i.lastWidth, width)
	cursorRow := i.reflowedCursorRow(width)
	blank := strings.Repeat(" ", width)
	fmt.Fprintf(i.stdout, "\r%s", ansiMoveUp(cursorRow-1))
	for row := 0; row < rows; row++ {
		fmt.Fprintf(i.stdout, "\r%s%s", ansiReset, blank)
		if row < rows-1 {
			fmt.Fprint(i.stdout, "\n")
		}
	}
	fmt.Fprintf(i.stdout, "\r%s", ansiMoveUp(rows-1))
}

// reflowedCursorRow returns the visual cursor row after terminal wrapping.
func (i *terminalChatInput) reflowedCursorRow(width int) int {
	rowWraps := reflowedRowsForLine(i.lastWidth, width)
	rowsBeforeCursor := (i.cursorRow - 1) * rowWraps
	cursorCol := promptCursorColumn(i.input, i.lastWidth)
	cursorWraps := 0
	if width > 0 {
		cursorWraps = cursorCol / width
	}

	return rowsBeforeCursor + cursorWraps + 1
}

// reflowedComposerRows returns the visual composer height after terminal wrap.
func reflowedComposerRows(rows int, oldWidth int, newWidth int) int {
	if rows <= 0 {
		return 0
	}

	return rows * reflowedRowsForLine(oldWidth, newWidth)
}

// reflowedRowsForLine returns how many visual rows one old terminal row uses.
func reflowedRowsForLine(oldWidth int, newWidth int) int {
	if oldWidth <= 0 || newWidth <= 0 || oldWidth <= newWidth {
		return 1
	}

	return (oldWidth + newWidth - 1) / newWidth
}

// submitLocked accepts current input and commits only non-blank prompts.
func (i *terminalChatInput) submitLocked() string {
	line := string(i.input)
	if strings.TrimSpace(line) == "" {
		i.discardLocked()

		return line
	}
	i.finishLocked()

	return line
}

// finishLocked moves the cursor below the active prompt island.
func (i *terminalChatInput) finishLocked() {
	if !i.rendered {
		return
	}
	width := terminalWidth(i.stdout)
	inputRows := wrappedPromptRows(i.input, width)
	i.clearLocked(width)
	fmt.Fprint(
		i.stdout, promptIslandRows(i.stdout, inputRows), ansiReset,
		"\n",
	)
	i.clearRenderStateLocked()
	i.input = i.input[:0]
}

// discardLocked erases the live prompt without committing it to scrollback.
func (i *terminalChatInput) discardLocked() {
	if !i.rendered {
		i.input = i.input[:0]

		return
	}
	width := terminalWidth(i.stdout)
	i.clearLocked(width)
	i.clearRenderStateLocked()
	i.input = i.input[:0]
}

// isPromptRune reports whether a rune should be inserted into the prompt.
func isPromptRune(r rune) bool {
	return r >= ' ' && r != utf8.RuneError
}

// wrappedPromptRows wraps the prompt marker and input at terminal width.
func wrappedPromptRows(input []rune, width int) []string {
	if width < 1 {
		width = 1
	}
	content := append([]rune("> "), input...)
	rows := make([]string, 0, len(content)/width+1)
	for len(content) > width {
		rows = append(rows, string(content[:width]))
		content = content[width:]
	}
	rows = append(rows, string(content))

	return rows
}

// promptCursorColumn returns the cursor column after the current input text.
func promptCursorColumn(input []rune, width int) int {
	if width < 1 {
		return 0
	}

	return (len(input) + len("> ")) % width
}

// composerPrefixRows returns rows rendered before the prompt island itself.
func composerPrefixRows(hasStatus bool) int {
	if !hasStatus {
		return 0
	}

	return 3
}

// composerRowCount returns the complete rendered height of the live composer.
func composerRowCount(inputRows int, hasStatus bool, hasFooter bool) int {
	if inputRows < 1 {
		inputRows = 1
	}

	rows := composerPrefixRows(hasStatus) + inputRows + 2
	if hasFooter {
		rows++
	}

	return rows
}

// statusComposerLine renders the transient status row above the input island.
func statusComposerLine(frame int, text string, startedAt time.Time,
	width int) string {

	frameText := statusPulseDot(frame)
	elapsed := formatElapsed(time.Since(startedAt))
	line := fmt.Sprintf("%s %s (%s)", frameText, text, elapsed)

	return ansiDim + padPromptRow(line, width) + ansiReset
}

// footerComposerLine renders the metadata row below the prompt island.
func footerComposerLine(text string, width int) string {
	return ansiReset + ansiDim + padPromptRow(text, width) + ansiReset
}

// padPromptRow pads one display row to the full terminal width.
func padPromptRow(row string, width int) string {
	if width <= 0 {
		return row
	}
	size := len([]rune(row))
	if size >= width {
		return row
	}

	return row + strings.Repeat(" ", width-size)
}

// isTerminalFile reports whether file is connected to a terminal device.
func isTerminalFile(file *os.File) bool {
	info, err := file.Stat()
	if err != nil {
		return false
	}

	return info.Mode()&os.ModeCharDevice != 0
}
