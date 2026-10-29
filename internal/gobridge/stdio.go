// Package gobridge relays a private Water connection through SSH stdio. Base64
// lines preserve arbitrary protocol bytes through Windows login shell encodings.
package gobridge

import (
	"bufio"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
)

const BlockSize = 32 * 1024
const framePrefix = "WATER_SSH_1 "

// Relay owns connection, input and output until either peer disconnects. It
// bounds a frame and propagates pipe backpressure rather than retaining output.
func Relay(connection net.Conn, input io.ReadCloser, output io.WriteCloser) error {
	done := make(chan error, 2)
	go func() { done <- encode(output, connection) }()
	go func() { done <- decode(connection, input) }()
	err := <-done
	_ = connection.Close()
	_ = input.Close()
	_ = output.Close()
	<-done
	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}
func encode(dst io.Writer, src io.Reader) error {
	buffer := make([]byte, BlockSize)
	for {
		n, err := src.Read(buffer)
		if n > 0 {
			if _, e := fmt.Fprintln(dst, framePrefix+base64.StdEncoding.EncodeToString(buffer[:n])); e != nil {
				return e
			}
		}
		if err != nil {
			return err
		}
	}
}
func decode(dst io.Writer, src io.Reader) error {
	scan := bufio.NewScanner(src)
	scan.Buffer(make([]byte, 4096), base64.StdEncoding.EncodedLen(BlockSize)+len(framePrefix)+2)
	started, bannerBytes := false, 0
	for scan.Scan() {
		line := scan.Text()
		if !strings.HasPrefix(line, framePrefix) {
			// OpenSSH may run a login profile before our bridge. Ignore only
			// bounded preamble text; protocol corruption after startup is fatal.
			bannerBytes += len(line) + 1
			if started || bannerBytes > 8*1024 {
				return errors.New("invalid SSH bridge preamble or frame")
			}
			continue
		}
		started = true
		data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(line, framePrefix))
		if err != nil {
			return fmt.Errorf("invalid SSH bridge frame: %w", err)
		}
		if len(data) > BlockSize {
			return errors.New("SSH bridge frame exceeds limit")
		}
		for len(data) > 0 {
			n, err := dst.Write(data)
			if err != nil {
				return err
			}
			if n == 0 {
				return io.ErrShortWrite
			}
			data = data[n:]
		}
	}
	if err := scan.Err(); err != nil {
		return err
	}
	return io.EOF
}
