package demo

import (
	_ "embed"
)

//go:embed sample_chat.json
var SampleChatJSON []byte

// SampleTalker is the fixed conversation id used in sample_chat.json.
const SampleTalker = "wxid_peer_b"
