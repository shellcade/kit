package session

import (
	"bytes"
	"io"
	"sync"

	"github.com/shellcade/kit/v2/host/sdk"
)

// MemSession is the test-only in-memory Session double: scripted keystroke input
// and captured frame output, with no socket. It is the forcing function that
// keeps lobby and game code honest about the Session boundary.
type MemSession struct {
	id   sdk.Player
	caps Caps

	inR *io.PipeReader
	inW *io.PipeWriter

	mu         sync.Mutex
	cols, rows int
	out        bytes.Buffer

	win       chan Size
	closeOnce sync.Once
}

// NewMemSession builds an in-memory session.
func NewMemSession(id sdk.Player, caps Caps, cols, rows int) *MemSession {
	pr, pw := io.Pipe()
	return &MemSession{
		id:   id,
		caps: caps,
		inR:  pr,
		inW:  pw,
		cols: cols,
		rows: rows,
		win:  make(chan Size, 8),
	}
}

func (s *MemSession) Read(p []byte) (int, error) { return s.inR.Read(p) }

func (s *MemSession) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.out.Write(p)
}

func (s *MemSession) Identity() sdk.Player { return s.id }

func (s *MemSession) Window() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cols, s.rows
}

func (s *MemSession) WindowChanges() <-chan Size { return s.win }
func (s *MemSession) Capabilities() Caps         { return s.caps }
func (s *MemSession) RemoteIP() string           { return "test" }

func (s *MemSession) Close() error {
	s.closeOnce.Do(func() {
		close(s.win)
		_ = s.inW.Close()
		_ = s.inR.Close()
	})
	return nil
}

// ---- test helpers ----------------------------------------------------------

// Feed writes scripted bytes as keystrokes (blocks until consumed).
func (s *MemSession) Feed(b []byte) (int, error) { return s.inW.Write(b) }

// CloseInputWriter half-closes the session: only the input pipe's write end,
// so the program's reader sees a clean io.EOF while the transport (output,
// window channel) stays up — the state a disconnect-detection test needs to
// stage before the full Close.
func (s *MemSession) CloseInputWriter() error { return s.inW.Close() }

// Output returns the bytes written by the renderer so far.
func (s *MemSession) Output() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.out.String()
}

// Resize updates the window size and emits a window-change event.
func (s *MemSession) Resize(cols, rows int) {
	s.mu.Lock()
	s.cols, s.rows = cols, rows
	s.mu.Unlock()
	s.win <- Size{Cols: cols, Rows: rows}
}
