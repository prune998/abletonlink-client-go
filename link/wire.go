package link

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// This file implements the byte-level wire formats used by Ableton Link:
//
//   - discovery messages ("_asdp_v1"), exchanged via multicast and unicast
//     UDP, carrying the peer state payload entries
//   - measurement messages ("_link_v1"), exchanged via unicast UDP between
//     peers' measurement endpoints, carrying ping/pong payloads
//
// All integers are encoded big endian (network byte order). Payloads are a
// sequence of entries: a uint32 key, a uint32 size and the value bytes.
// Entries with an empty value are skipped when encoding. Unknown keys must be
// ignored when parsing, which keeps the implementation forward compatible.

const (
	maxMessageSize = 512

	// Note: the trailing byte of both magic headers is the literal byte
	// 0x01 (the protocol version), not the character '1'. This mirrors
	// ableton::discovery::v1 and ableton::link::v1 kProtocolHeader.
	discoveryMagic = "_asdp_v\x01"
	linkMagic      = "_link_v\x01"

	// Discovery message types
	discoveryAlive   byte = 1
	discoveryResp    byte = 2
	discoveryByeBye  byte = 3
	discoveryTTL     byte = 5
	discoveryGroupID      = uint16(0)

	// Link measurement message types
	linkPing byte = 1
	linkPong byte = 2

	// Peer state payload entry keys
	keyTimeline   uint32 = 0x746d6c6e // 'tmln'
	keySession    uint32 = 0x73657373 // 'sess'
	keyStartStop  uint32 = 0x73747374 // 'stst'
	keyMeepV4     uint32 = 0x6d657034 // 'mep4'
	keyMeepV6     uint32 = 0x6d657036 // 'mep6'
	keyHostTime   uint32 = 0x5f5f6874 // '__ht'
	keyGHostTime  uint32 = 0x5f5f6774 // '__gt'
	keyPrevGHostT uint32 = 0x5f706774 // '_pgt'
)

// entry is a single payload entry.
type entry struct {
	key   uint32
	value []byte
}

// encodeEntries encodes a sequence of payload entries, skipping empty ones.
func encodeEntries(buf []byte, entries []entry) []byte {
	for _, e := range entries {
		if len(e.value) == 0 {
			continue
		}
		buf = binary.BigEndian.AppendUint32(buf, e.key)
		buf = binary.BigEndian.AppendUint32(buf, uint32(len(e.value)))
		buf = append(buf, e.value...)
	}
	return buf
}

// parseEntries parses a payload as a sequence of entries, invoking handler
// for each. Unknown keys are ignored.
func parseEntries(payload []byte, handler func(key uint32, value []byte) error) error {
	for len(payload) > 0 {
		if len(payload) < 8 {
			return errors.New("link: truncated payload entry header")
		}
		key := binary.BigEndian.Uint32(payload)
		size := binary.BigEndian.Uint32(payload[4:])
		payload = payload[8:]
		if uint64(size) > uint64(len(payload)) {
			return errors.New("link: payload entry size exceeds message")
		}
		value := payload[:size]
		payload = payload[size:]
		if handler != nil {
			if err := handler(key, value); err != nil {
				return err
			}
		}
	}
	return nil
}

// encodeDiscoveryMessage builds an _asdp_v1 discovery message.
func encodeDiscoveryMessage(msgType, ttl byte, groupID uint16, id NodeId, entries []entry) []byte {
	buf := make([]byte, 0, maxMessageSize)
	buf = append(buf, discoveryMagic...)
	buf = append(buf, msgType, ttl)
	buf = binary.BigEndian.AppendUint16(buf, groupID)
	buf = append(buf, id[:]...)
	buf = encodeEntries(buf, entries)
	if len(buf) >= maxMessageSize {
		panic(fmt.Sprintf("link: discovery message too large: %d", len(buf)))
	}
	return buf
}

// discoveryHeader is the parsed header of a discovery message.
type discoveryHeader struct {
	msgType byte
	ttl     byte
	groupID uint16
	nodeID  NodeId
}

// parseDiscoveryMessage parses the header of a discovery message and returns
// it along with the raw payload bytes.
func parseDiscoveryMessage(data []byte) (discoveryHeader, []byte, bool) {
	var hdr discoveryHeader
	minLen := len(discoveryMagic) + 2 + 2 + 8
	if len(data) < minLen || string(data[:len(discoveryMagic)]) != discoveryMagic {
		return hdr, nil, false
	}
	p := data[len(discoveryMagic):]
	hdr.msgType = p[0]
	hdr.ttl = p[1]
	hdr.groupID = binary.BigEndian.Uint16(p[2:])
	copy(hdr.nodeID[:], p[4:])
	return hdr, p[12:], true
}

// encodeLinkMessage builds a _link_v1 measurement message.
func encodeLinkMessage(msgType byte, entries []entry) []byte {
	buf := make([]byte, 0, maxMessageSize)
	buf = append(buf, linkMagic...)
	buf = append(buf, msgType)
	buf = encodeEntries(buf, entries)
	if len(buf) >= maxMessageSize {
		panic(fmt.Sprintf("link: link message too large: %d", len(buf)))
	}
	return buf
}

// parseLinkMessage parses the header of a measurement message and returns the
// message type along with the raw payload bytes.
func parseLinkMessage(data []byte) (byte, []byte, bool) {
	minLen := len(linkMagic) + 1
	if len(data) < minLen || string(data[:len(linkMagic)]) != linkMagic {
		return 0, nil, false
	}
	return data[len(linkMagic)], data[len(linkMagic)+1:], true
}

// encodeMicrosEntry encodes a microsecond value as an int64 entry value.
func encodeMicrosEntry(key uint32, t Micros) entry {
	return entry{key, binary.BigEndian.AppendUint64(nil, uint64(int64(t)))}
}

func decodeMicros(value []byte) (Micros, error) {
	if len(value) != 8 {
		return 0, errors.New("link: bad micros entry size")
	}
	return Micros(int64(binary.BigEndian.Uint64(value))), nil
}
