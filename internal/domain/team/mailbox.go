package team

import (
	"encoding/json"
	"fmt"
	"time"
)

// MaxInboxSize bounds one mailbox. A mailbox holds the messages an agent has
// not read yet, which stay small; past this it is not a file the product
// wrote.
const MaxInboxSize = 4 << 20

// MessageType is what an entry carrying no type of its own means, which is the
// value Claude Code fills in when it reads such an entry.
const MessageType = "message"

// Message is one entry of an agent's mailbox.
type Message struct {
	// Type is the kind of message. It is MessageType unless the sender named
	// another kind.
	Type string
	// From is the name of the sender.
	From string
	Text string
	// Summary is the short form the sender wrote, empty when it wrote none.
	Summary string
	// Color is the color of the sender, as the team configuration records it.
	Color string
	// Timestamp is kept as it was written so the text on screen is the text on
	// disk; At reads it as a time.
	Timestamp string
	// Read is the sender-visible flag Claude Code sets once the agent has read
	// the message.
	Read bool
}

// At reports the time the message carries, and whether it is one this product
// can read.
func (m Message) At() (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, m.Timestamp)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// Inbox is one mailbox file.
type Inbox struct {
	Messages []Message
	// Dropped counts the entries of the file that are not messages this
	// product can read. Claude Code drops them as well, and prunes them from
	// the file; we only report them, since the file is not ours to rewrite.
	Dropped int
}

// Unread counts the messages the agent has not read.
func (i Inbox) Unread() int {
	n := 0
	for _, m := range i.Messages {
		if !m.Read {
			n++
		}
	}
	return n
}

// Last returns the message written last, which is the last entry of the file:
// a mailbox is appended to.
func (i Inbox) Last() (Message, bool) {
	if len(i.Messages) == 0 {
		return Message{}, false
	}
	return i.Messages[len(i.Messages)-1], true
}

type wireMessage struct {
	Type      *string `json:"type"`
	From      *string `json:"from"`
	Text      *string `json:"text"`
	Summary   *string `json:"summary"`
	Color     *string `json:"color"`
	Timestamp *string `json:"timestamp"`
	Read      *bool   `json:"read"`
}

// ParseInbox decodes a mailbox file. An entry that is not a message, or one
// missing the sender, the text or the time, is counted and skipped rather than
// failing the file: Claude Code reads such a file that way, and a mailbox the
// sidebar cannot read at all would hide every message that is fine.
func ParseInbox(data []byte) (Inbox, error) {
	if len(data) > MaxInboxSize {
		return Inbox{}, fmt.Errorf("%w: %d bytes", ErrTooLarge, len(data))
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(data, &entries); err != nil {
		return Inbox{}, fmt.Errorf("decode mailbox: %w", err)
	}
	in := Inbox{Messages: make([]Message, 0, len(entries))}
	for _, raw := range entries {
		var w wireMessage
		if err := json.Unmarshal(raw, &w); err != nil {
			in.Dropped++
			continue
		}
		if w.From == nil || w.Text == nil || w.Timestamp == nil {
			in.Dropped++
			continue
		}
		m := Message{
			Type:      MessageType,
			From:      *w.From,
			Text:      *w.Text,
			Timestamp: *w.Timestamp,
		}
		if w.Type != nil {
			m.Type = *w.Type
		}
		if w.Summary != nil {
			m.Summary = *w.Summary
		}
		if w.Color != nil {
			m.Color = *w.Color
		}
		if w.Read != nil {
			m.Read = *w.Read
		}
		in.Messages = append(in.Messages, m)
	}
	return in, nil
}
