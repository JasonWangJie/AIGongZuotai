package studio

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// A pipe is private to one parent and one child. The kind is outside the JSON
// so even an invalid header cannot allocate a dispatch-sized buffer for an ACK.
type childFrameKind byte

const (
	childHello childFrameKind = iota + 1
	childReady
	childStart
	childStarted
	childDelta
	childAck
	childCancel
	childEnd
	childCancelReceipt
)
const childProtocolVersion = 2

var (
	errChatChildInterrupted = errors.New("chat execution child interrupted")
	errChildProtocol        = errors.New("invalid chat execution pipe message")
	errChildProvider        = errors.New("chat provider stream incomplete")
)

type childHelloData struct {
	Protocol                     int
	Owner, Mode, BaseURL, APIKey string
	Slots                        int
	DemoDelay                    time.Duration
}
type childTag struct {
	Lane     int
	Ticket   uint64
	Identity chatChildIdentity
}
type childRequestData struct {
	Model   string
	Request ChatRequest
	Usage   bool
}
type childFrame struct {
	Tag      *childTag         `json:",omitempty"`
	Hello    *childHelloData   `json:",omitempty"`
	Start    *childRequestData `json:",omitempty"`
	Seq      uint64            `json:",omitempty"`
	Text     string            `json:",omitempty"`
	Usage    *ChatUsage        `json:",omitempty"`
	Failed   bool              `json:",omitempty"`
	CancelID string            `json:",omitempty"`
}

func childFrameLimit(kind childFrameKind) int {
	switch kind {
	case childHello:
		return 32 << 10
	case childStart:
		return 6*MaxChatContentBytes + (32 << 10)
	case childDelta:
		return 6*maxRunOutputBytes + 4096
	case childReady, childStarted, childAck, childCancel, childEnd, childCancelReceipt:
		return 4096
	}
	return 0
}

func readChildFrame(r io.Reader) (childFrameKind, childFrame, error) {
	var header [5]byte
	var frame childFrame
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return 0, frame, err
	}
	kind := childFrameKind(header[0])
	size := binary.BigEndian.Uint32(header[1:])
	if size == 0 || size > uint32(childFrameLimit(kind)) {
		return 0, frame, errChildProtocol
	}
	raw := make([]byte, int(size))
	if _, err := io.ReadFull(r, raw); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return 0, frame, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	object := bytes.TrimSpace(raw)
	if len(object) == 0 || object[0] != '{' || !utf8.Valid(raw) || dec.Decode(&frame) != nil || dec.Decode(&struct{}{}) != io.EOF || !frame.matches(kind) {
		return 0, childFrame{}, errChildProtocol
	}
	return kind, frame, nil
}

func (f childFrame) matches(kind childFrameKind) bool {
	// Every kind accepts only its own fields. Wrong-attempt responses must
	// never reach another stream even when a physical lane gets reused.
	if (f.Hello != nil) != (kind == childHello) || (f.Start != nil) != (kind == childStart) ||
		(f.Tag != nil) != (kind != childHello && kind != childReady) ||
		(f.Usage != nil || f.Failed) && kind != childEnd ||
		kind != childDelta && f.Text != "" ||
		kind != childDelta && kind != childAck && kind != childEnd && kind != childCancelReceipt && f.Seq != 0 {
		return false
	}
	if kind == childCancel || kind == childCancelReceipt {
		if !validOperationsID(f.CancelID, "cancel") {
			return false
		}
	} else if f.CancelID != "" {
		return false
	}
	if f.Tag != nil && (!f.Tag.valid() || f.Tag.Lane < 0 || f.Tag.Lane >= 64 || f.Tag.Ticket == 0) {
		return false
	}
	switch kind {
	case childDelta:
		return f.Seq > 0 && f.Text != "" && len(f.Text) <= maxRunOutputBytes && strings.IndexByte(f.Text, 0) < 0
	case childAck:
		return f.Seq > 0
	case childEnd:
		return f.Usage == nil || validChildUsage(*f.Usage)
	}
	return true
}

func (t childTag) valid() bool {
	id := t.Identity
	if id.Epoch == 0 {
		return false
	}
	for _, s := range []string{id.RunID, id.Owner, id.ExecutionID, id.Generation} {
		if len(s) < 1 || len(s) > 128 {
			return false
		}
		for i := 0; i < len(s); i++ {
			c := s[i]
			if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
				return false
			}
		}
	}
	return true
}

func validChildUsage(u ChatUsage) bool {
	if u.Source != "demo" && u.Source != "gateway" {
		return false
	}
	switch u.Status {
	case "reported", "demo", "missing", "invalid":
	default:
		return false
	}
	switch u.Reason {
	case "", "usage_missing", "usage_invalid", "usage_conflict", "stream_incomplete", "request_not_started":
	default:
		return false
	}
	for _, n := range []int64{u.PromptTokens, u.CompletionTokens, u.TotalTokens} {
		if n < 0 || n > maxReportedChatTokens {
			return false
		}
	}
	if u.Status == "reported" || u.Status == "demo" {
		return u.Complete && u.Reason == "" && u.TotalTokens == u.PromptTokens+u.CompletionTokens &&
			(u.Status == "reported" && u.Source == "gateway" || u.Status == "demo" && u.Source == "demo")
	}
	return true
}

type childFrameWriter struct {
	mu  sync.Mutex
	out io.Writer
}

func (w *childFrameWriter) send(kind childFrameKind, f childFrame) error {
	if !f.matches(kind) {
		return errChildProtocol
	}
	raw, err := json.Marshal(f)
	if err != nil || len(raw) > childFrameLimit(kind) {
		return errChildProtocol
	}
	var header [5]byte
	header[0] = byte(kind)
	binary.BigEndian.PutUint32(header[1:], uint32(len(raw)))
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, err = io.Copy(w.out, bytes.NewReader(header[:])); err != nil {
		return err
	}
	_, err = io.Copy(w.out, bytes.NewReader(raw))
	return err
}
