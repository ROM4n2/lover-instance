// Package chatlogkeeper ingests chatlog-keeper's local export format — a JSON
// array of message rows — and normalizes it into the canonical model.
//
// chatlog-keeper is the M0-selected sidecar: it reads the user's own logged-in
// WeChat client passively (read-only process memory, no injection, no hooks, no
// debugger) and writes the export to a local file. This package consumes that
// file; it never talks to WeChat, never touches a key, and holds no decryption
// code (ADR-0001).
//
// The field names below are the ones M0 measured on a real 32624-message
// export, not guesses. See uniqueKeyExtraFields in internal/model for the
// identity contract that measurement produced.
package chatlogkeeper

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"

	"lover-instance/backend-go/internal/model"
)

// Message is one row of a chatlog-keeper export, verbatim.
type Message struct {
	// Ts is epoch SECONDS as a JSON number, not a digit string.
	Ts               float64 `json:"ts"`
	Sender           string  `json:"sender"`
	SenderWxID       string  `json:"sender_wxid"`
	ChatRoom         string  `json:"chat_room"`
	ConversationID   string  `json:"conversation_id"`
	Content          string  `json:"content"`
	MsgType          int     `json:"msg_type"`
	IsSelf           bool    `json:"is_self"`
	AccountID        string  `json:"account_id"`
	ThreadID         string  `json:"thread_id"`
	ServerID         string  `json:"server_id"`
	SourceOffset     string  `json:"source_offset"`
	ConversationType string  `json:"conversation_type"`
	IsGroupChat      bool    `json:"is_group_chat"`
}

// DecodeMessages reads a chatlog-keeper export: a JSON array of message rows.
//
// The read is strict on purpose, because both ways of being lax here lose data
// quietly. A decoder that stops at the first complete JSON value accepts two
// concatenated arrays — an export appended to rather than overwritten — and
// hands back only the first one with no error; the same is true of trailing
// garbage. And a document that is not an array at all (the literal `null` a
// failed export leaves behind, an object wrapper) decodes to an empty result
// indistinguishable from a legitimately empty conversation, so a caller cannot
// tell "nothing was ever there" from "the file is broken". Both are refused.
func DecodeMessages(r io.Reader) ([]Message, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read chatlog-keeper export: %w", err)
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("decode chatlog-keeper export: %w", io.ErrUnexpectedEOF)
	}
	if trimmed[0] != '[' {
		return nil, fmt.Errorf("decode chatlog-keeper export: want a JSON array of message rows, document starts with %q", trimmed[0])
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	var messages []Message
	if err := decoder.Decode(&messages); err != nil {
		return nil, fmt.Errorf("decode chatlog-keeper export: %w", err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return nil, errors.New("decode chatlog-keeper export: unexpected trailing JSON after the message array (the file holds more than one document); refusing to ingest a partial export")
		}
		return nil, fmt.Errorf("decode chatlog-keeper export: trailing content after the message array: %w", err)
	}
	return messages, nil
}

// ParseMessages decodes an export and normalizes every row into a canonical
// message. Any row that cannot be normalized aborts the whole parse, reported
// with its index: ingesting a partial export would silently drop messages, and
// silent loss is the failure mode this pipeline exists to avoid.
func ParseMessages(r io.Reader) ([]model.CanonicalMessage, error) {
	rows, err := DecodeMessages(r)
	if err != nil {
		return nil, err
	}
	canonical := make([]model.CanonicalMessage, 0, len(rows))
	for i, row := range rows {
		raw, err := row.ToRaw()
		if err != nil {
			return nil, fmt.Errorf("export row %d: %w", i, err)
		}
		message, err := model.ParseRaw(raw)
		if err != nil {
			return nil, fmt.Errorf("export row %d: %w", i, err)
		}
		canonical = append(canonical, message)
	}
	return canonical, nil
}

// Timestamp renders Ts as the epoch digit string that model.NormalizeTs
// classifies by digit count.
//
// This conversion must not be left to fmt.Sprint: formatting a float64 such as
// 1792166715.0 that way yields "1.792166715e+09", which NormalizeTs rejects as
// a non-integer. The integer part is therefore formatted explicitly. A value
// carrying sub-second precision is widened to milliseconds rather than
// truncated, so precision is never quietly thrown away.
func (m Message) Timestamp() (string, error) {
	if math.IsNaN(m.Ts) || math.IsInf(m.Ts, 0) {
		return "", fmt.Errorf("ts %v is not a finite number", m.Ts)
	}
	if m.Ts <= 0 {
		return "", fmt.Errorf("ts %v is not a positive epoch value", m.Ts)
	}
	seconds := int64(m.Ts)
	if float64(seconds) == m.Ts {
		return strconv.FormatInt(seconds, 10), nil
	}
	return strconv.FormatInt(int64(math.Round(m.Ts*1000)), 10), nil
}

// ToRaw maps one export row onto the canonical raw row.
//
// server_id carries the WeChat-native message id and source_offset the
// exporter's cursor; both go into the identity fields so that
// model.DetectUniqueKey can fall back from the former to the latter exactly as
// M0 measured it — 3 of 32624 real rows had a null server_id and every one of
// them still carried a source_offset.
func (m Message) ToRaw() (model.RawChatlogMessage, error) {
	timestamp, err := m.Timestamp()
	if err != nil {
		return model.RawChatlogMessage{}, fmt.Errorf("message %q: %w", m.SourceOffset, err)
	}
	// Seq stays zero: this exporter has no seq column at all, and copying ts
	// into it would recreate the seq-is-a-timestamp trap the project forbids.
	return model.RawChatlogMessage{
		MsgSvrID: m.ServerID,
		Time:     timestamp,
		Talker:   m.ConversationID,
		Sender:   m.SenderWxID,
		IsSelf:   m.IsSelf,
		Type:     m.MsgType,
		Content:  m.Content,
		Extra:    m.extra(),
	}, nil
}

// extra carries the export fields with no canonical column. account_id,
// thread_id and the conversation flags are per-account or per-conversation
// constants, so they are kept as evidence and never as identity:
// model.DetectUniqueKey consults only its frozen field list.
func (m Message) extra() map[string]any {
	return map[string]any{
		"account_id":        m.AccountID,
		"thread_id":         m.ThreadID,
		"source_offset":     m.SourceOffset,
		"conversation_type": m.ConversationType,
		"is_group_chat":     m.IsGroupChat,
		"sender":            m.Sender,
		"chat_room":         m.ChatRoom,
	}
}
