package main

// an MVT tile is a protobuf msg, and a protobuf msg is laid out like so:
//
// msg = a run of fields, one after another
// field = a tag, then a payload
// tag = a varint containing (field number << 3) | a wire type
// wire 0 = payload is one var
// wire 1 = payload is 8 bytes
// wire 2 = payload is a varint length, then that many bytes
// wire 5 = payload is 4 bytes
//
// MVT field nums needed here:
//
// Tile : layers = 3
// Layer : values = 4 (repeated field, one entry per distinct val)
// Value : string = 1, bool = 7
//
// features point at entries in a layer's val table by idx, so replacing a bool entry with a string
// entry in the same pos changes what the value is without modifying file otherwise

import (
	"encoding/binary"
	"errors"
)

var errBadProto = errors.New("malformed protobuf")

// field is one field of a message, cut out of bytes without being modified
type field struct {
	num  uint64
	wire uint64
	raw  []byte // the field exactly as it appeared, tag included
	body []byte // the payload, bytes after the length for wire 2, the varint bytes for wire 0
}

// splitFields cuts a msg into its fields, this DOES NOT copy, every slice points into msg
func splitFields(msg []byte) ([]field, error) {
	var out []field

	for len(msg) > 0 {
		tag, n := binary.Uvarint(msg)
		if n <= 0 {
			return nil, errBadProto
		}

		f := field{num: tag >> 3, wire: tag & 7}
		rest := msg[n:]

		size := 0      // payload bytes, including a length prefix if it exists
		bodyStart := 0 // where the payload proper starts inside rest

		switch f.wire {
		case 0:
			_, m := binary.Uvarint(rest)
			if m <= 0 {
				return nil, errBadProto
			}
			size = m
		case 1:
			size = 8
		case 2:
			l, m := binary.Uvarint(rest)
			if m <= 0 || l > uint64(len(rest)-m) {
				return nil, errBadProto
			}
			size, bodyStart = m+int(l), m
		case 5:
			size = 4
		default:
			return nil, errBadProto // wire types 3 and 4 (groups) are NOT used by MVT
		}

		if size > len(rest) {
			return nil, errBadProto
		}

		f.raw = msg[:n+size]
		f.body = rest[bodyStart:size]
		out = append(out, f)
		msg = msg[n+size:]
	}
	return out, nil
}

// appendBytesField appends a length-delim (wire 2) field into dst
func appendBytesField(dst []byte, num uint64, payload []byte) []byte {
	dst = binary.AppendUvarint(dst, num<<3|2)
	dst = binary.AppendUvarint(dst, uint64(len(payload)))
	return append(dst, payload...)
}

// rewriteValue turns a bool inside one Value msg into the txt "true" or "false", returns if anthing
// was changed
func rewriteValue(value []byte) ([]byte, bool, error) {
	fields, err := splitFields(value)
	if err != nil {
		return nil, false, err
	}

	var out []byte
	changed := false

	for _, f := range fields {
		if f.num == 7 && f.wire == 0 {
			v, _ := binary.Uvarint(f.body)
			text := "false"
			if v != 0 {
				text = "true"
			}
			out = appendBytesField(out, 1, []byte(text))
			changed = true
			continue
		}
		out = append(out, f.raw...)
	}

	return out, changed, nil
}

// rewriteLayer rewrites all bools in one Layer's values table and returns the new layer along w/
// how many values it changed
func rewriteLayer(layer []byte) ([]byte, int, error) {
	fields, err := splitFields(layer)
	if err != nil {
		return nil, 0, err
	}

	var out []byte
	count := 0

	for _, f := range fields {
		if f.num == 4 && f.wire == 2 {
			nv, changed, err := rewriteValue(f.body)
			if err != nil {
				return nil, 0, err
			}
			if changed {
				out = appendBytesField(out, 4, nv)
				count++
				continue
			}
		}
		out = append(out, f.raw...)
	}

	return out, count, nil
}

// boolsToText rewrites all bool values in every layer of a MVT tile into the txt "true" or "false",
// and returns the new tile & how many values it changed. if nothing was changed/there was nothing
// to change, original tile is returned as is
func boolsToText(tile []byte) ([]byte, int, error) {
	fields, err := splitFields(tile)
	if err != nil {
		return nil, 0, err
	}

	var out []byte
	total := 0

	for _, f := range fields {
		if f.num == 3 && f.wire == 2 {
			nl, n, err := rewriteLayer(f.body)
			if err != nil {
				return nil, 0, err
			}
			if n > 0 {
				out = appendBytesField(out, 3, nl)
				total += n
				continue
			}
		}
		out = append(out, f.raw...)
	}

	if total == 0 {
		return tile, 0, nil
	}

	return out, total, nil
}
