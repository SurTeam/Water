package goprotocol

import (
	"bytes"
	"fmt"
	"io"
	"testing"

	"github.com/google/uuid"
)

func BenchmarkInputEncoding(b *testing.B) {
	for _, size := range []int{32, 4096, 128 * 1024} {
		data := bytes.Repeat([]byte("x"), size)
		id := uuid.New()
		values := make([]int, size)
		for index, value := range data {
			values[index] = int(value)
		}
		message := map[string]any{"type": "terminal.send_bytes", "terminal_id": id, "bytes": values}
		b.Run(fmt.Sprintf("binary/%d", size), func(b *testing.B) {
			b.SetBytes(int64(size))
			b.ReportAllocs()
			for iteration := 0; iteration < b.N; iteration++ {
				if err := WriteInput(io.Discard, id, data); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(fmt.Sprintf("json/%d", size), func(b *testing.B) {
			b.SetBytes(int64(size))
			b.ReportAllocs()
			for iteration := 0; iteration < b.N; iteration++ {
				if err := WriteJSON(io.Discard, message); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
