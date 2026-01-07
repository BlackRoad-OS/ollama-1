package readline

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

type Prompt struct {
	Prompt         string
	AltPrompt      string
	Placeholder    string
	AltPlaceholder string
	UseAlt         bool
}

func (p *Prompt) prompt() string {
	if p.UseAlt {
		return p.AltPrompt
	}
	return p.Prompt
}

func (p *Prompt) placeholder() string {
	if p.UseAlt {
		return p.AltPlaceholder
	}
	return p.Placeholder
}

type Terminal struct {
	reader  *bufio.Reader
	rawmode bool
	termios any
}

type Instance struct {
	Prompt     *Prompt
	Terminal   *Terminal
	History    *History
	Pasting    bool
	ToolOutput string // Last tool output for Ctrl+O expansion
}

func New(prompt Prompt) (*Instance, error) {
	term, err := NewTerminal()
	if err != nil {
		return nil, err
	}

	history, err := NewHistory()
	if err != nil {
		return nil, err
	}

	return &Instance{
		Prompt:   &prompt,
		Terminal: term,
		History:  history,
	}, nil
}

func (i *Instance) Readline() (string, error) {
	if !i.Terminal.rawmode {
		fd := os.Stdin.Fd()
		termios, err := SetRawMode(fd)
		if err != nil {
			return "", err
		}
		i.Terminal.rawmode = true
		i.Terminal.termios = termios
	}

	prompt := i.Prompt.prompt()
	if i.Pasting {
		// force alt prompt when pasting
		prompt = i.Prompt.AltPrompt
	}
	fmt.Print(prompt)

	defer func() {
		fd := os.Stdin.Fd()
		//nolint:errcheck
		UnsetRawMode(fd, i.Terminal.termios)
		i.Terminal.rawmode = false
	}()

	buf, _ := NewBuffer(i.Prompt)

	var esc bool
	var escex bool
	var metaDel bool

	var currentLineBuf []rune

	for {
		// don't show placeholder when pasting unless we're in multiline mode
		showPlaceholder := !i.Pasting || i.Prompt.UseAlt
		if buf.IsEmpty() && showPlaceholder {
			ph := i.Prompt.placeholder()
			fmt.Print(ColorGrey + ph + CursorLeftN(len(ph)) + ColorDefault)
		}

		r, err := i.Terminal.Read()

		if buf.IsEmpty() {
			fmt.Print(ClearToEOL)
		}

		if err != nil {
			return "", io.EOF
		}

		if escex {
			escex = false

			switch r {
			case KeyUp:
				i.historyPrev(buf, &currentLineBuf)
			case KeyDown:
				i.historyNext(buf, &currentLineBuf)
			case KeyLeft:
				buf.MoveLeft()
			case KeyRight:
				buf.MoveRight()
			case CharBracketedPaste:
				var code string
				for range 3 {
					r, err = i.Terminal.Read()
					if err != nil {
						return "", io.EOF
					}

					code += string(r)
				}
				if code == CharBracketedPasteStart {
					i.Pasting = true
				} else if code == CharBracketedPasteEnd {
					i.Pasting = false
				}
			case KeyDel:
				if buf.DisplaySize() > 0 {
					buf.Delete()
				}
				metaDel = true
			case MetaStart:
				buf.MoveToStart()
			case MetaEnd:
				buf.MoveToEnd()
			default:
				// skip any keys we don't know about
				continue
			}
			continue
		} else if esc {
			esc = false

			switch r {
			case 'b':
				buf.MoveLeftWord()
			case 'f':
				buf.MoveRightWord()
			case CharBackspace:
				buf.DeleteWord()
			case CharEscapeEx:
				escex = true
			}
			continue
		}

		switch r {
		case CharNull:
			continue
		case CharEsc:
			esc = true
		case CharInterrupt:
			return "", ErrInterrupt
		case CharPrev:
			i.historyPrev(buf, &currentLineBuf)
		case CharNext:
			i.historyNext(buf, &currentLineBuf)
		case CharLineStart:
			buf.MoveToStart()
		case CharLineEnd:
			buf.MoveToEnd()
		case CharBackward:
			buf.MoveLeft()
		case CharForward:
			buf.MoveRight()
		case CharBackspace, CharCtrlH:
			buf.Remove()
		case CharTab:
			// todo: convert back to real tabs
			for range 8 {
				buf.Add(' ')
			}
		case CharDelete:
			if buf.DisplaySize() > 0 {
				buf.Delete()
			} else {
				return "", io.EOF
			}
		case CharKill:
			buf.DeleteRemaining()
		case CharCtrlU:
			buf.DeleteBefore()
		case CharCtrlL:
			buf.ClearScreen()
		case CharCtrlO:
			// Ctrl+O - show tool output in pager
			if i.ToolOutput == "" {
				// No output to show, just beep
				fmt.Print("\a")
				continue
			}

			// Show pager in alternate screen (original view restored on exit)
			showPager(i.ToolOutput)
			continue
		case CharCtrlW:
			buf.DeleteWord()
		case CharCtrlZ:
			fd := os.Stdin.Fd()
			return handleCharCtrlZ(fd, i.Terminal.termios)
		case CharEnter, CharCtrlJ:
			output := buf.String()
			if output != "" {
				i.History.Add(output)
			}
			buf.MoveToEnd()
			fmt.Println()

			return output, nil
		default:
			if metaDel {
				metaDel = false
				continue
			}
			if r >= CharSpace || r == CharEnter || r == CharCtrlJ {
				buf.Add(r)
			}
		}
	}
}

// SetRawMode enables raw mode to prevent terminal from interpreting control chars
func (i *Instance) SetRawMode(on bool) {
	fd := os.Stdin.Fd()
	if on && !i.Terminal.rawmode {
		termios, err := SetRawMode(fd)
		if err == nil {
			i.Terminal.rawmode = true
			i.Terminal.termios = termios
		}
	} else if !on && i.Terminal.rawmode {
		UnsetRawMode(fd, i.Terminal.termios)
		i.Terminal.rawmode = false
	}
}

func (i *Instance) HistoryEnable() {
	i.History.Enabled = true
}

func (i *Instance) HistoryDisable() {
	i.History.Enabled = false
}

func (i *Instance) historyPrev(buf *Buffer, currentLineBuf *[]rune) {
	if i.History.Pos > 0 {
		if i.History.Pos == i.History.Size() {
			*currentLineBuf = []rune(buf.String())
		}
		buf.Replace([]rune(i.History.Prev()))
	}
}

func (i *Instance) historyNext(buf *Buffer, currentLineBuf *[]rune) {
	if i.History.Pos < i.History.Size() {
		buf.Replace([]rune(i.History.Next()))
		if i.History.Pos == i.History.Size() {
			buf.Replace(*currentLineBuf)
		}
	}
}

func NewTerminal() (*Terminal, error) {
	fd := os.Stdin.Fd()
	termios, err := SetRawMode(fd)
	if err != nil {
		return nil, err
	}
	if err := UnsetRawMode(fd, termios); err != nil {
		return nil, err
	}

	t := &Terminal{
		reader: bufio.NewReader(os.Stdin),
	}

	return t, nil
}

func (t *Terminal) Read() (rune, error) {
	r, _, err := t.reader.ReadRune()
	if err != nil {
		return 0, err
	}
	return r, nil
}

// showPager displays content in a simple pager that exits on 'q' or Ctrl+O
func showPager(content string) {
	lines := strings.Split(content, "\n")
	offset := 0

	// Get terminal size (default to 80x24 if we can't determine)
	termWidth, termHeight := 80, 24
	if w, h, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
		termWidth, termHeight = w, h-1 // Leave room for status line
	}

	// Enter alternate screen buffer (preserves chat history)
	fmt.Print(EnterAltScreen)
	defer fmt.Print(ExitAltScreen)

	reader := bufio.NewReader(os.Stdin)

	for {
		// Clear screen and move cursor to top
		fmt.Print(ClearScreen + CursorReset)

		// Display visible lines
		end := offset + termHeight
		if end > len(lines) {
			end = len(lines)
		}
		for i := offset; i < end; i++ {
			line := lines[i]
			if len(line) > termWidth {
				line = line[:termWidth]
			}
			fmt.Println(line)
		}

		// Show status line
		fmt.Printf(ColorGrey+"[Lines %d-%d of %d] Press q or Ctrl+O to exit, j/k or arrows to scroll"+ColorDefault, offset+1, end, len(lines))

		// Read input
		r, _, err := reader.ReadRune()
		if err != nil {
			return
		}

		switch r {
		case 'q', 'Q', CharCtrlO, CharInterrupt:
			return
		case 'j', CharCtrlJ, CharEnter:
			if offset < len(lines)-termHeight {
				offset++
			}
		case 'k':
			if offset > 0 {
				offset--
			}
		case ' ', CharForward: // Page down (space or Ctrl+F)
			offset += termHeight
			if offset > len(lines)-termHeight {
				offset = len(lines) - termHeight
				if offset < 0 {
					offset = 0
				}
			}
		case CharBackward: // Page up (Ctrl+B)
			offset -= termHeight
			if offset < 0 {
				offset = 0
			}
		case 'g': // Go to top
			offset = 0
		case 'G': // Go to bottom
			offset = len(lines) - termHeight
			if offset < 0 {
				offset = 0
			}
		case CharEsc:
			// Handle escape sequences for arrow keys
			r2, _, _ := reader.ReadRune()
			if r2 == '[' {
				r3, _, _ := reader.ReadRune()
				switch r3 {
				case 'A': // Up
					if offset > 0 {
						offset--
					}
				case 'B': // Down
					if offset < len(lines)-termHeight {
						offset++
					}
				case '5': // Page up
					reader.ReadRune() // consume ~
					offset -= termHeight
					if offset < 0 {
						offset = 0
					}
				case '6': // Page down
					reader.ReadRune() // consume ~
					offset += termHeight
					if offset > len(lines)-termHeight {
						offset = len(lines) - termHeight
						if offset < 0 {
							offset = 0
						}
					}
				}
			}
		}
	}
}
