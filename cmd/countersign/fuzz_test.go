package main

import (
	"bufio"
	"bytes"
	"strings"
	"testing"

	"github.com/ClearedStack/countersign/internal/item"
)

func FuzzReadPromptLine(f *testing.F) {
	f.Add([]byte("approve\n\nskip\nquit\n"))
	f.Add([]byte("override\r\nReason with CRLF\r\n"))
	f.Add([]byte("no-newline-at-eof"))
	f.Add([]byte(""))
	f.Add(bytes.Repeat([]byte("a"), item.MaxPayload+128))

	f.Fuzz(func(t *testing.T, data []byte) {
		r := bufio.NewReaderSize(bytes.NewReader(data), 64)
		for steps := 0; ; steps++ {
			if steps > len(data)+2 {
				t.Fatalf("readPromptLine did not make progress on %d-byte input", len(data))
			}
			line, tooLong, eof, err := readPromptLine(r)
			if err != nil {
				t.Fatalf("unexpected error from bytes.Reader: %v", err)
			}
			if eof {
				break
			}
			if tooLong {
				if line != "" {
					t.Fatalf("tooLong returned non-empty line of %d bytes", len(line))
				}
				continue
			}
			if len(line) > item.MaxPayload+1 {
				t.Fatalf("line exceeded MaxPayload bound: %d bytes", len(line))
			}
			if strings.HasSuffix(line, "\n") || strings.HasSuffix(line, "\r") {
				t.Fatalf("trailing CR/LF was not stripped: %q", line)
			}
		}
	})
}
