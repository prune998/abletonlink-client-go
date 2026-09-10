package link

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// NodeId is an 8-byte identifier. It is used both to identify a node on the
// network and as a session id (sessions are identified by their founding
// node), mirroring ableton::link::NodeId.
type NodeId [8]byte

func randomNodeId() NodeId {
	var id NodeId
	_, err := rand.Read(id[:])
	if err != nil {
		// crypto/rand never fails on supported platforms, but fall back to
		// a deterministic-ish value rather than panicking.
		panic("link: failed to generate random node id: " + err.Error())
	}
	return id
}

func (n NodeId) String() string { return "0x" + hex.EncodeToString(n[:]) }

func (n NodeId) compare(o NodeId) int {
	for i := range n {
		if n[i] != o[i] {
			if n[i] < o[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func (n NodeId) eq(o NodeId) bool { return n == o }

func parseNodeId(s string) (NodeId, error) {
	var id NodeId
	b, err := hex.DecodeString(s)
	if err != nil {
		return id, err
	}
	if len(b) != 8 {
		return id, fmt.Errorf("link: node id must be 8 bytes, got %d", len(b))
	}
	copy(id[:], b)
	return id, nil
}
