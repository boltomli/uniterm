package session

import (
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/ys-ll/uniterm/backend/log"
	"golang.org/x/text/encoding"
	"golang.org/x/text/transform"
)

const (
	// Telnet protocol constants
	telnetIAC  = 255
	telnetWILL = 251
	telnetWONT = 252
	telnetDO   = 253
	telnetDONT = 254
	telnetSB   = 250
	telnetSE   = 240

	// Telnet options
	telnetOptBinary          = 0
	telnetOptEcho            = 1
	telnetOptSuppressGoAhead = 3
	telnetOptTerminalType    = 24
	telnetOptNAWS            = 31

	// Sub-negotiation
	telnetTTYPEIs   = 0
	telnetTTYPESend = 1

	// telnetPendingCap bounds the bytes filterIAC may retain for an
	// incomplete command (split option byte, lone IAC, unfinished sub-
	// negotiation). A peer that never completes the sequence must not be
	// able to grow the buffer without limit: past the cap the tail is
	// dropped with an error log and parsing restarts clean. Real sub-
	// negotiations (TTYPE, NAWS) are a few dozen bytes at most.
	telnetPendingCap = 4096
)

type TelnetSession struct {
	*baseSession
	conn     net.Conn
	cancel   context.CancelFunc
	quit     chan struct{}
	quitOnce sync.Once

	enc            encoding.Encoding // input(write) codec; nil = utf-8 passthrough
	encIn          inputEncoder      // UTF-8 → session encoding for writes (own lock)
	decoder        *encoding.Decoder // persistent streaming decoder for output(read)
	decodeLeftover []byte            // trailing partial multibyte bytes between reads
	decodeScratch  []byte            // reusable src buffer for decodeOutput

	// Telnet option state (configured in Connect, consumed by Write)
	localEcho      bool
	telnetSendMode string // "character" | "line"
	newlineMode    string // "cr" | "crlf"
	termType       string // terminal name replied to the server's TTYPE SEND
	lineBuf        []byte // line mode only: bytes since the last flushed line

	// iacPending holds an incomplete telnet command carried across reads
	// (readLoop goroutine only): a split IAC WILL/WONT/DO/DONT missing its
	// option byte, a lone trailing IAC, or an IAC SB ... sequence still
	// awaiting its terminating IAC SE. Capped by telnetPendingCap.
	iacPending []byte
}

func NewTelnetSession(id string) *TelnetSession {
	return &TelnetSession{
		baseSession: &baseSession{
			id:          id,
			sessionType: "telnet",
			status:      StatusDisconnected,
		},
		quit: make(chan struct{}),
	}
}

func (s *TelnetSession) Connect(config ConnectionConfig) error {
	s.SetLogOnConnect(config.LogOnConnect)
	s.setStatus(StatusConnecting)
	if config.Name != "" {
		s.title = config.Name
	} else {
		s.title = fmt.Sprintf("%s:%d", config.Host, config.Port)
	}

	addr := net.JoinHostPort(config.Host, strconv.Itoa(config.Port))
	dialer := net.Dialer{Timeout: 15 * time.Second}
	conn, err := dialer.Dial("tcp", addr)
	if err != nil {
		s.setStatus(StatusError)
		return fmt.Errorf("telnet dial: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.conn = conn
	s.cancel = cancel
	s.setStatus(StatusConnected)

	// Store telnet option configuration for use by Write()
	s.localEcho = config.LocalEcho
	s.telnetSendMode = config.TelnetSendMode
	s.newlineMode = config.NewlineMode
	s.termType = EffectiveTermType(config.TermType)

	// Proactively negotiate binary transmission, character-at-a-time mode,
	// and terminal type — unless the user opted for passive negotiation.
	if config.TelnetNegotiationMode != "passive" {
		s.conn.Write([]byte{telnetIAC, telnetWILL, telnetOptBinary})
		s.conn.Write([]byte{telnetIAC, telnetDO, telnetOptSuppressGoAhead})
		s.conn.Write([]byte{telnetIAC, telnetWILL, telnetOptTerminalType})

		if cols, rows := s.GetPendingSize(); cols > 0 && rows > 0 {
			s.sendNAWS(cols, rows)
		} else {
			s.sendNAWS(80, 24)
		}
	}

	s.startEmitLoop()
	go s.readLoop(ctx)
	go s.runPostLoginScript(ctx, config.PostLoginScript)

	// Auto-login: send username/password if configured
	if config.User != "" {
		go s.sendAutoLogin(ctx, config.User, config.Password)
	}

	return nil
}

func (s *TelnetSession) readLoop(ctx context.Context) {
	buf := make([]byte, 16384)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		n, err := s.conn.Read(buf)
		if n > 0 {
			s.RecordReadActivity()
			s.handleRead(buf[:n])
		}
		if err != nil {
			if err != io.EOF {
				s.emitData([]byte(fmt.Sprintf("\r\n\x1b[31m[read error: %v]\x1b[0m\r\n", err)))
			} else {
				s.emitData(disconnectNotice("Connection closed by remote host."))
			}
			s.Disconnect()
			return
		}
	}
}

func (s *TelnetSession) handleRead(data []byte) {
	filtered := s.filterIAC(data)
	if len(filtered) > 0 {
		s.emitData(s.decodeOutput(filtered))
	}
}

// filterIAC strips telnet IAC command sequences from data and returns the
// terminal payload. The parser is stateful: a command split across reads (a
// missing option byte, a lone trailing IAC, or an IAC SB … sequence still
// awaiting its terminating IAC SE) is retained in s.iacPending and merged
// with the next read instead of being emitted as data or dropped. Incomplete
// tails are never emitted, and every loop iteration either consumes at least
// one input byte or breaks out to retain the tail, so a truncated command
// can no longer spin the readLoop forever. Must only be called from the
// single readLoop goroutine (tests call it from one goroutine too).
func (s *TelnetSession) filterIAC(data []byte) []byte {
	if len(s.iacPending) > 0 {
		merged := make([]byte, 0, len(s.iacPending)+len(data))
		merged = append(merged, s.iacPending...)
		merged = append(merged, data...)
		s.iacPending = s.iacPending[:0]
		data = merged
	}

	var out []byte
	i := 0
loop:
	for i < len(data) {
		if data[i] != telnetIAC {
			out = append(out, data[i])
			i++
			continue
		}
		if i+1 == len(data) {
			break loop // lone trailing IAC: hold for the next read
		}
		cmd := data[i+1]
		switch cmd {
		case telnetWILL, telnetWONT, telnetDO, telnetDONT:
			if i+2 == len(data) {
				break loop // option byte arrives with the next read
			}
			s.handleNegotiation(cmd, data[i+2])
			i += 3
		case telnetSB:
			// Find the terminating IAC SE; IAC IAC inside the payload is an
			// escaped 0xFF data byte, not the terminator.
			end := -1
			for j := i + 2; j < len(data); j++ {
				if data[j] != telnetIAC {
					continue
				}
				if j+1 == len(data) {
					break // split IAC: incomplete
				}
				switch data[j+1] {
				case telnetSE:
					end = j + 2
				case telnetIAC:
					j++ // escaped 0xFF payload byte
				}
				if end >= 0 {
					break
				}
			}
			if end < 0 {
				break loop // unfinished sub-negotiation: hold for the next read
			}
			s.handleSubNegotiation(data, i)
			i = end
		case telnetIAC:
			out = append(out, telnetIAC)
			i += 2
		default:
			i += 2
		}
	}
	s.holdIAC(data[i:])
	return out
}

// holdIAC retains the incomplete command tail for the next read, capped at
// telnetPendingCap: past the cap the tail is dropped with an error log and
// parsing restarts from a clean state, so a peer that never completes a
// sequence cannot grow the buffer without bound.
func (s *TelnetSession) holdIAC(tail []byte) {
	if len(tail) == 0 {
		s.iacPending = s.iacPending[:0]
		return
	}
	if len(tail) > telnetPendingCap {
		log.Writef("session %s: incomplete telnet IAC sequence exceeds %d bytes, dropping it",
			s.id, telnetPendingCap)
		s.iacPending = s.iacPending[:0]
		return
	}
	s.iacPending = append(s.iacPending[:0], tail...)
}

func (s *TelnetSession) handleSubNegotiation(data []byte, start int) {
	if start+3 >= len(data) {
		return
	}
	opt := data[start+2]
	if opt == telnetOptTerminalType && data[start+3] == telnetTTYPESend {
		// Server requests terminal type: reply with the configured value.
		term := []byte(s.termType)
		msg := make([]byte, 0, 4+len(term))
		msg = append(msg, telnetIAC, telnetSB, telnetOptTerminalType, telnetTTYPEIs)
		msg = append(msg, term...)
		msg = append(msg, telnetIAC, telnetSE)
		s.conn.Write(msg)
	}
}

func (s *TelnetSession) handleNegotiation(cmd byte, opt byte) {
	switch cmd {
	case telnetWILL:
		// Server offers to do something.
		switch opt {
		case telnetOptBinary, telnetOptSuppressGoAhead:
			s.reply(telnetDO, opt) // Accept
		case telnetOptEcho:
			s.reply(telnetDO, opt) // Accept server echoing
		case telnetOptTerminalType:
			s.reply(telnetDO, opt) // Accept server knows terminal type
		default:
			s.reply(telnetDONT, opt)
		}
	case telnetDO:
		// Server asks us to do something.
		switch opt {
		case telnetOptBinary, telnetOptSuppressGoAhead, telnetOptNAWS, telnetOptTerminalType:
			s.reply(telnetWILL, opt) // Accept
		case telnetOptEcho:
			s.reply(telnetWONT, opt) // We don't echo locally
		default:
			s.reply(telnetWONT, opt)
		}
	}
}

func (s *TelnetSession) reply(cmd byte, opt byte) {
	if s.conn == nil {
		return
	}
	s.conn.Write([]byte{telnetIAC, cmd, opt})
}

func (s *TelnetSession) sendNAWS(cols, rows int) {
	if s.conn == nil {
		return
	}
	data := []byte{
		telnetIAC, telnetSB, telnetOptNAWS,
		byte(cols >> 8), byte(cols & 0xff),
		byte(rows >> 8), byte(rows & 0xff),
		telnetIAC, telnetSE,
	}
	s.conn.Write(data)
}

func (s *TelnetSession) sendAutoLogin(ctx context.Context, user, password string) {
	// Conservative fix (SESSION-05): the previous fixed 1500ms / 1200ms
	// sleeps could land the username in the shell prompt on slow
	// servers. Replace with a shorter initial wait and bail early on
	// context cancel. A full prompt-detection rewrite would require
	// touching the readLoop and is out of scope for a conservative fix.
	time.Sleep(500 * time.Millisecond)

	select {
	case <-ctx.Done():
		return
	default:
	}

	if s.conn != nil {
		s.conn.Write([]byte(user + "\r\n"))
	}

	if password != "" {
		time.Sleep(500 * time.Millisecond)
		select {
		case <-ctx.Done():
			return
		default:
		}
		if s.conn != nil {
			s.conn.Write([]byte(password + "\r\n"))
		}
	}
}

func (s *TelnetSession) runPostLoginScript(ctx context.Context, script string) {
	s.baseSession.RunPostLoginScript(ctx, script, func(data []byte) {
		if s.conn != nil {
			s.conn.Write(data)
		}
	}, s.IsConnected)
}

// flushLineBuffer writes the buffered line to the server (applying CRLF
// translation when configured) and clears lineBuf. Under newlineMode "crlf"
// a '\r' becomes "\r\n" unless it is already followed by '\n' — so a
// \r\n-terminated line is sent as typed, never doubled.
func (s *TelnetSession) flushLineBuffer() error {
	flush := s.lineBuf
	if s.newlineMode == "crlf" {
		translated := make([]byte, 0, len(flush)+1)
		for i, fb := range flush {
			if fb == '\r' && !(i+1 < len(flush) && flush[i+1] == '\n') {
				translated = append(translated, '\r', '\n')
			} else {
				translated = append(translated, fb)
			}
		}
		flush = translated
	}
	_, err := s.conn.Write(flush)
	s.lineBuf = s.lineBuf[:0]
	return err
}

func (s *TelnetSession) Write(data []byte) error {
	if s.conn == nil {
		return fmt.Errorf("not connected")
	}

	encoded := s.encodeInput(data)

	// Line mode: buffer until a newline, flush every complete line, keep the
	// trailing partial line in lineBuf for the next Write. The old code
	// returned right after the first flush, silently dropping everything
	// after the first newline (pasting "a\rb\r" sent only "a").
	if s.telnetSendMode == "line" {
		remStart := len(s.lineBuf) // index in lineBuf where this call's unflushed bytes start
		flush := func() error {
			remStart = 0
			return s.flushLineBuffer()
		}
		// A '\r' defers its flush until the next byte is seen: if that byte
		// is '\n', the pair is a single CRLF terminator (one flush, no
		// spurious empty line for the lone '\n'). A '\n' terminates at once.
		pendingCR := false
		for _, b := range encoded {
			if pendingCR && b != '\n' {
				if err := flush(); err != nil {
					return err
				}
			}
			pendingCR = false
			s.lineBuf = append(s.lineBuf, b)
			switch b {
			case '\n':
				if err := flush(); err != nil {
					return err
				}
			case '\r':
				pendingCR = true
			}
		}
		if pendingCR {
			// Input ended on a line terminator: the line is complete.
			if err := flush(); err != nil {
				return err
			}
		}
		// Echo only what remains buffered (the trailing partial line this
		// call contributed), and only when local echo is on — a flushed line
		// was never echoed by the old code either, and servers that don't
		// echo don't care what we display.
		if s.localEcho && len(s.lineBuf) > remStart {
			s.emitData(append([]byte(nil), s.lineBuf[remStart:]...))
		}
		return nil
	}

	// Character mode: apply CRLF translation, then send immediately
	if s.newlineMode == "crlf" {
		var translated []byte
		for _, b := range encoded {
			if b == '\r' {
				translated = append(translated, '\r', '\n')
			} else {
				translated = append(translated, b)
			}
		}
		encoded = translated
	}

	_, err := s.conn.Write(encoded)

	// Local echo: show typed characters in terminal (for servers that don't echo)
	if err == nil && s.localEcho {
		s.emitData(data)
	}

	return err
}

func (s *TelnetSession) Disconnect() error {
	s.quitOnce.Do(func() {
		close(s.quit)
	})
	if s.cancel != nil {
		s.cancel()
	}
	if s.conn != nil {
		s.conn.Close()
	}
	s.setStatus(StatusDisconnected)
	return nil
}

func (s *TelnetSession) Resize(cols, rows int) error {
	s.SetPendingSize(cols, rows)
	if s.conn == nil {
		return fmt.Errorf("session not connected")
	}
	s.sendNAWS(cols, rows)
	return nil
}

func (s *TelnetSession) IsConnected() bool {
	return s.Status() == StatusConnected
}

// SetEncoding configures the character encoding for this session.
// name: "" / "utf-8" (passthrough) | "gbk" | "gb2312" | "gb18030" |
// "big5" | "shift-jis" | "euc-jp" | "euc-kr".
func (s *TelnetSession) SetEncoding(name string) {
	enc := encodingByName(name)
	s.mu.Lock()
	s.enc = enc
	if enc == nil {
		s.decoder = nil
		s.encIn.set(nil)
	} else {
		s.decoder = enc.NewDecoder()
		s.encIn.set(enc.NewEncoder())
	}
	s.decodeLeftover = nil
	s.mu.Unlock()
}

// decodeOutput converts a chunk of remote bytes to UTF-8 using the configured
// decoder. Partial trailing multibyte sequences are buffered until the next
// call. Must only be called from the single readLoop goroutine.
func (s *TelnetSession) decodeOutput(data []byte) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.decoder == nil {
		return data
	}
	s.decodeScratch = s.decodeScratch[:0]
	s.decodeScratch = append(s.decodeScratch, s.decodeLeftover...)
	s.decodeScratch = append(s.decodeScratch, data...)
	src := s.decodeScratch

	var out []byte
	dst := make([]byte, 8192)
	for {
		nDst, nSrc, err := s.decoder.Transform(dst, src, false)
		out = append(out, dst[:nDst]...)
		src = src[nSrc:]
		if err == transform.ErrShortDst {
			continue
		}
		break
	}
	if len(src) > 0 {
		s.decodeLeftover = append(s.decodeLeftover[:0], src...)
	} else {
		s.decodeLeftover = src[:0]
	}
	return out
}

// encodeInput converts user keystrokes (UTF-8) to the configured encoding
// before writing to the remote. Each call handles a complete UTF-8 input.
func (s *TelnetSession) encodeInput(data []byte) []byte {
	return s.encIn.encode(data)
}
