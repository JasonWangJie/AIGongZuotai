package studio

import (
	"encoding/binary"
	"errors"
	"io"
)

var errSourceMP4 = errors.New("invalid or externally referenced source MP4")

// Inspect the bounded ISO BMFF container without reading media into memory.
// The same open file is rewound for moderation or for the immutable body.
func checkSourceMP4(r io.ReadSeeker, size int64) error {
	if size < 16 || size > videoTimelineSourceLimit {
		return errSourceMP4
	}
	remaining := 4096
	var hasData, hasVideo bool
	type track struct{ maxVideoRef, localRefs int }
	var walk func(string, int64, int64, int, *track) error
	walk = func(parent string, start, end int64, expected int, t *track) error {
		count := 0
		var header [16]byte
		for pos := start; pos < end; {
			count++
			remaining--
			if remaining < 0 || end-pos < 8 {
				return errSourceMP4
			}
			if _, err := r.Seek(pos, io.SeekStart); err != nil {
				return err
			}
			if _, err := io.ReadFull(r, header[:8]); err != nil {
				return err
			}
			length := int64(binary.BigEndian.Uint32(header[:4]))
			kind := string(header[4:8])
			body := pos + 8
			if length == 1 {
				if end-body < 8 {
					return errSourceMP4
				}
				if _, err := io.ReadFull(r, header[8:]); err != nil {
					return err
				}
				length = int64(binary.BigEndian.Uint64(header[8:]))
				body += 8
			} else if length == 0 {
				length = end - pos
			}
			if length < body-pos || length > end-pos {
				return errSourceMP4
			}
			stop := pos + length
			path := parent + "/" + kind
			switch path {
			case "/moov/trak":
				tr := &track{}
				if err := walk(path, body, stop, -1, tr); err != nil {
					return err
				}
				if tr.maxVideoRef > tr.localRefs {
					return errSourceMP4
				}
				hasVideo = hasVideo || tr.maxVideoRef > 0
			case "/moov", "/moov/trak/mdia", "/moov/trak/mdia/minf", "/moov/trak/mdia/minf/dinf", "/moov/trak/mdia/minf/stbl":
				if err := walk(path, body, stop, -1, t); err != nil {
					return err
				}
			case "/moov/trak/mdia/minf/dinf/dref", "/moov/trak/mdia/minf/stbl/stsd":
				// FullBox version/flags and entry_count precede the child boxes.
				// Bound declared counts by the same budget as parsed box headers.
				if stop-body < 8 {
					return errSourceMP4
				}
				if _, err := io.ReadFull(r, header[:8]); err != nil || binary.BigEndian.Uint32(header[:4]) != 0 {
					return errSourceMP4
				}
				entries := binary.BigEndian.Uint32(header[4:8])
				if entries == 0 || entries > uint32(remaining) {
					return errSourceMP4
				}
				if err := walk(path, body+8, stop, int(entries), t); err != nil {
					return err
				}
			case "/mdat":
				hasData = hasData || stop > body
			default:
				switch parent {
				case "/moov/trak/mdia/minf/dinf/dref":
					if kind != "url " || stop-body != 4 {
						return errSourceMP4
					}
					if _, err := io.ReadFull(r, header[:4]); err != nil || binary.BigEndian.Uint32(header[:4]) != 1 {
						return errSourceMP4
					}
					t.localRefs++
				case "/moov/trak/mdia/minf/stbl/stsd":
					switch kind {
					case "avc1", "avc3", "hvc1", "hev1", "av01", "vp09", "mp4v":
						var entry [28]byte
						if stop-body < 78 {
							return errSourceMP4
						}
						if _, err := io.ReadFull(r, entry[:]); err != nil {
							return err
						}
						w, h := int64(binary.BigEndian.Uint16(entry[24:])), int64(binary.BigEndian.Uint16(entry[26:]))
						ref := int(binary.BigEndian.Uint16(entry[6:8]))
						if ref == 0 || w == 0 || h == 0 || w*h > 32_000_000 {
							return errSourceMP4
						}
						t.maxVideoRef = max(t.maxVideoRef, ref)
					}
				}
			}
			pos = stop
		}
		if expected >= 0 && count != expected {
			return errSourceMP4
		}
		return nil
	}
	if err := walk("", 0, size, -1, nil); err != nil {
		return err
	}
	if !hasData || !hasVideo {
		return errSourceMP4
	}
	return nil
}
