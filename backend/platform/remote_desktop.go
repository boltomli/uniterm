package platform

import (
	"sync"
	"time"
)

// RDPKeyEvent carries physical XT set-1 scan codes, including the E0 prefix.
// CaptureID prevents queued events from crossing focus changes or reconnects.
type RDPKeyEvent struct {
	CaptureID  string `json:"captureID"`
	Scancode   uint16 `json:"scancode"`
	Down       bool   `json:"down"`
	Repeat     bool   `json:"repeat"`
	Ctrl       bool   `json:"ctrl"`
	Shift      bool   `json:"shift"`
	Meta       bool   `json:"meta"`
	Reset      bool   `json:"reset"`
	VirtualKey uint32 `json:"virtualKey,omitempty"`
}

type rdpKeyboardState struct {
	captureID string
	pressed   map[uint16]string
}

func (s *rdpKeyboardState) shortcutKey(scan uint16, altDown bool) bool {
	if _, held := s.pressed[scan]; held {
		return true
	}
	if scan == 0xe05b || scan == 0xe05c || scan == 0x38 || scan == 0x0f && altDown {
		return true
	}
	return s.captureID != "" && (s.pressed[0x38] == s.captureID ||
		s.pressed[0xe05b] == s.captureID || s.pressed[0xe05c] == s.captureID)
}

func (s *rdpKeyboardState) activate(id string) {
	s.captureID = id
	if s.pressed == nil {
		s.pressed = make(map[uint16]string)
	}
}

func (s *rdpKeyboardState) key(scan uint16, down, foreground, local bool) (RDPKeyEvent, bool) {
	// F12 remains available for the local inspector. Keys already swallowed
	// must also have their releases swallowed after focus leaves the desktop;
	// otherwise a Windows-key release can open the local Start menu.
	previous, held := s.pressed[scan]
	if !foreground || s.captureID == "" || local || scan == 0 {
		if held && !down {
			delete(s.pressed, scan)
		}
		return RDPKeyEvent{}, held
	}
	if held && previous != s.captureID {
		if !down {
			delete(s.pressed, scan)
		}
		return RDPKeyEvent{}, true
	}
	if !down && !held {
		return RDPKeyEvent{}, false
	}
	if down {
		s.pressed[scan] = s.captureID
	} else {
		delete(s.pressed, scan)
	}
	pressed := func(code uint16) bool { return s.pressed[code] == s.captureID }
	return RDPKeyEvent{
		CaptureID: s.captureID, Scancode: scan, Down: down, Repeat: held && down,
		Ctrl:  pressed(0x1d) || pressed(0xe01d),
		Shift: pressed(0x2a) || pressed(0x36),
		Meta:  pressed(0xe05b) || pressed(0xe05c),
	}, true
}

type desktopKeyboardBackend interface {
	capture(uintptr) bool
	focused() bool
	release()
	close()
}

type desktopKeyboardCapture struct {
	operations sync.Mutex
	mu         sync.Mutex
	state      rdpKeyboardState
	window     uintptr
	shortcuts  bool
	expires    time.Time
	active     bool
	closed     bool
	backend    desktopKeyboardBackend
	emit       func(RDPKeyEvent)
	queue      []RDPKeyEvent
	wake       chan struct{}
	stop       chan struct{}
	done       chan struct{}
}

func newDesktopKeyboardCapture(emit func(RDPKeyEvent)) *desktopKeyboardCapture {
	return &desktopKeyboardCapture{
		emit: emit, wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{}),
	}
}

func (c *desktopKeyboardCapture) enqueue(event RDPKeyEvent) {
	c.queue = append(c.queue, event)
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *desktopKeyboardCapture) reset() {
	if c.state.captureID != "" {
		c.enqueue(RDPKeyEvent{CaptureID: c.state.captureID, Reset: true})
	}
	c.state.activate("")
}

func (c *desktopKeyboardCapture) SetCapture(id string, window uintptr) bool {
	return c.setCapture(id, window, false)
}

func (c *desktopKeyboardCapture) SetShortcutCapture(id string, window uintptr) bool {
	return c.setCapture(id, window, true)
}

func (c *desktopKeyboardCapture) setCapture(id string, window uintptr, shortcuts bool) bool {
	if c == nil {
		return false
	}
	c.operations.Lock()
	defer c.operations.Unlock()
	c.mu.Lock()
	if c.closed || c.backend == nil || window == 0 || id == "" {
		c.mu.Unlock()
		return false
	}
	if c.active && c.state.captureID == id && c.window == window && c.shortcuts == shortcuts {
		c.expires = time.Now().Add(3 * time.Second)
		c.mu.Unlock()
		return true
	}
	c.reset()
	c.state.activate(id)
	c.active = true
	c.window, c.shortcuts, c.expires = window, shortcuts, time.Now().Add(3*time.Second)
	c.mu.Unlock()
	ok := c.backend.capture(window)
	c.mu.Lock()
	ok = ok && !c.closed
	c.active = ok
	if !ok {
		c.reset()
	}
	c.mu.Unlock()
	return ok
}

func (c *desktopKeyboardCapture) Release(id string) {
	if c == nil {
		return
	}
	c.operations.Lock()
	defer c.operations.Unlock()
	c.mu.Lock()
	if c.state.captureID != id || id == "" {
		c.mu.Unlock()
		return
	}
	c.reset()
	active := c.active
	c.active = false
	c.mu.Unlock()
	if active {
		c.backend.release()
	}
}

func (c *desktopKeyboardCapture) key(scan uint16, down, foreground, ctrl, shift, meta, alt bool) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	active := c.active && !c.closed && foreground && time.Now().Before(c.expires)
	if !active {
		c.reset()
	}
	local := scan == 0x58 || c.shortcuts && !c.state.shortcutKey(scan, alt)
	event, swallow := c.state.key(scan, down, active, local)
	if event.CaptureID != "" {
		event.Ctrl, event.Shift, event.Meta = ctrl, shift, meta
		c.enqueue(event)
	}
	return swallow
}

func (c *desktopKeyboardCapture) checkLease() {
	c.operations.Lock()
	defer c.operations.Unlock()
	c.mu.Lock()
	active := c.active && !c.closed
	expired := c.state.captureID == "" || !time.Now().Before(c.expires)
	c.mu.Unlock()
	if !active || !expired && c.backend.focused() {
		return
	}
	c.mu.Lock()
	c.reset()
	c.active = false
	c.mu.Unlock()
	c.backend.release()
}

func (c *desktopKeyboardCapture) run() {
	defer close(c.done)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-c.stop:
			c.operations.Lock()
			c.backend.close()
			c.operations.Unlock()
			return
		case <-ticker.C:
			c.checkLease()
		case <-c.wake:
			c.mu.Lock()
			batch := c.queue
			c.queue = nil
			c.mu.Unlock()
			for _, event := range batch {
				if c.emit != nil {
					c.emit(event)
				}
			}
		}
	}
}

func (c *desktopKeyboardCapture) Close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.reset()
	c.active = false
	close(c.stop)
	c.mu.Unlock()
}
