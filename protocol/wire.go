package protocol

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

var errTooLarge = errors.New("message larger than 16 MiB")

func newReader(r io.Reader) *bufio.Reader {
	return bufio.NewReaderSize(r, 64<<10)
}

func readLine(r *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := r.ReadSlice('\n')
		buf = append(buf, chunk...)
		if len(bytes.TrimSuffix(buf, []byte("\n"))) > MaxMessage {
			for errors.Is(err, bufio.ErrBufferFull) {
				_, err = r.ReadSlice('\n')
			}
			if err != nil && !errors.Is(err, io.EOF) {
				return nil, err
			}
			return nil, errTooLarge
		}
		switch {
		case err == nil:
			return bytes.TrimSuffix(bytes.TrimSuffix(buf, []byte("\n")), []byte("\r")), nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF) && len(buf) > 0:
			return buf, nil
		default:
			return nil, err
		}
	}
}

func envelope(line []byte, allowed ...string) (map[string]json.RawMessage, error) {
	var env map[string]json.RawMessage
	if err := json.Unmarshal(line, &env); err != nil {
		return nil, errors.New("not a JSON object")
	}
	if env == nil {
		return nil, errors.New("not a JSON object")
	}
	for k := range env {
		known := false
		for _, a := range allowed {
			if k == a {
				known = true
			}
		}
		if !known {
			return env, errors.New("unknown member " + k)
		}
	}
	var version string
	if err := json.Unmarshal(env["jsonrpc"], &version); err != nil || version != JSONRPC {
		return env, errors.New(`jsonrpc must be "2.0"`)
	}
	return env, nil
}

func messageID(env map[string]json.RawMessage) (int, error) {
	raw, ok := env["id"]
	if !ok {
		return 0, errors.New("id is missing")
	}
	var id int
	if err := json.Unmarshal(raw, &id); err != nil {
		return 0, errors.New("id must be an integer")
	}
	return id, nil
}

func present(raw json.RawMessage) bool {
	return len(raw) > 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}
