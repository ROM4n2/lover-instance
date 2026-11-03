package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Contact holds display metadata for an account or conversation peer.
type Contact struct {
	WxID     string `json:"wxid"`
	Nickname string `json:"nickname"`
	Remark   string `json:"remark"`
	IsSelf   bool   `json:"is_self"`
}

// ConversationSummary is an active chat with message count and human-friendly display name.
type ConversationSummary struct {
	Talker       string `json:"talker"`
	DisplayName  string `json:"display_name"`
	MessageCount int    `json:"message_count"`
	IsChatroom   bool   `json:"is_chatroom"`
	IsMain       bool   `json:"is_main"`
}

// SaveContact upserts a contact's display name or remark.
func (s *Store) SaveContact(ctx context.Context, c Contact) error {
	const query = `
INSERT INTO contacts (wxid, nickname, remark, is_self)
VALUES (?, ?, ?, ?)
ON CONFLICT(wxid) DO UPDATE SET
  nickname = CASE WHEN excluded.nickname != '' THEN excluded.nickname ELSE contacts.nickname END,
  remark   = CASE WHEN excluded.remark != '' THEN excluded.remark ELSE contacts.remark END,
  is_self  = excluded.is_self;`
	isSelf := 0
	if c.IsSelf {
		isSelf = 1
	}
	_, err := s.db.ExecContext(ctx, query, c.WxID, c.Nickname, c.Remark, isSelf)
	if err != nil {
		return fmt.Errorf("save contact %q: %w", c.WxID, err)
	}
	return nil
}

// GetContact looks up a contact by wxid.
func (s *Store) GetContact(ctx context.Context, wxid string) (Contact, bool, error) {
	const query = `SELECT wxid, nickname, remark, is_self FROM contacts WHERE wxid = ?`
	var c Contact
	var isSelf int
	err := s.db.QueryRowContext(ctx, query, wxid).Scan(&c.WxID, &c.Nickname, &c.Remark, &isSelf)
	if err == sql.ErrNoRows {
		return Contact{}, false, nil
	}
	if err != nil {
		return Contact{}, false, fmt.Errorf("get contact %q: %w", wxid, err)
	}
	c.IsSelf = isSelf != 0
	return c, true, nil
}

// ListConversations returns all conversations present in messages, ordered by message count DESC.
func (s *Store) ListConversations(ctx context.Context) ([]ConversationSummary, error) {
	const query = `
SELECT 
    m.talker,
    COALESCE(NULLIF(c.remark, ''), NULLIF(c.nickname, ''), m.talker) as display_name,
    COUNT(*) as msg_count,
    MAX(m.is_chatroom) as is_chatroom
FROM messages m
LEFT JOIN contacts c ON c.wxid = m.talker
GROUP BY m.talker
ORDER BY msg_count DESC;`

	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list conversations: %w", err)
	}
	defer rows.Close()

	var list []ConversationSummary
	maxCount := 0
	for rows.Next() {
		var item ConversationSummary
		var isChatroom int
		if err := rows.Scan(&item.Talker, &item.DisplayName, &item.MessageCount, &isChatroom); err != nil {
			return nil, fmt.Errorf("scan conversation: %w", err)
		}
		item.IsChatroom = isChatroom != 0
		if len(list) == 0 {
			maxCount = item.MessageCount
		}
		if !item.IsChatroom && (item.MessageCount == maxCount || len(list) == 0) {
			item.IsMain = true
		}
		list = append(list, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return list, nil
}

// BackfillFromExports scans JSON exports in exportsDir to populate missing contact display names.
func (s *Store) BackfillFromExports(ctx context.Context, exportsDir string) error {
	talkers, err := s.ListTalkers(ctx)
	if err != nil || len(talkers) == 0 {
		return err
	}

	entries, err := os.ReadDir(exportsDir)
	if err != nil {
		return nil
	}

	names := make(map[string]string)
	for _, entry := range entries {
		var path string
		if entry.IsDir() {
			candidate := filepath.Join(exportsDir, entry.Name(), "wechat_messages.json")
			if _, err := os.Stat(candidate); err == nil {
				path = candidate
			}
		} else if strings.HasSuffix(entry.Name(), ".json") {
			path = filepath.Join(exportsDir, entry.Name())
		}
		if path == "" {
			continue
		}

		f, err := os.Open(path)
		if err != nil {
			continue
		}
		var rows []struct {
			ConversationID string `json:"conversation_id"`
			ChatRoom       string `json:"chat_room"`
			Sender         string `json:"sender"`
			IsSelf         bool   `json:"is_self"`
		}
		if err := json.NewDecoder(f).Decode(&rows); err == nil {
			for _, r := range rows {
				if r.ConversationID == "" {
					continue
				}
				if _, ok := names[r.ConversationID]; !ok {
					name := r.ChatRoom
					if name == "" || name == r.ConversationID {
						if !r.IsSelf && r.Sender != "" && r.Sender != r.ConversationID {
							name = r.Sender
						}
					}
					if name != "" && name != r.ConversationID {
						names[r.ConversationID] = name
					}
				}
			}
		}
		f.Close()
	}

	for wxid, name := range names {
		_ = s.SaveContact(ctx, Contact{
			WxID:     wxid,
			Nickname: name,
			IsSelf:   false,
		})
	}
	return nil
}
