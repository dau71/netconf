package transport

import (
	"bufio"
	"bytes"
	"io"
	"testing"
)

var (
	rfcFramedRPC = []byte(`
<?xml version="1.0" encoding="UTF-8"?>
<rpc message-id="105"
xmlns="urn:ietf:params:xml:ns:netconf:base:1.0">
  <get-config>
    <source><running/></source>
    <config xmlns="http://example.com/schema/1.2/config">
     <users/>
    </config>
  </get-config>
</rpc>
]]>]]>`)
	rfcUnframedRPC = rfcFramedRPC[:len(rfcFramedRPC)-6]
)

var framedTests = []struct {
	name        string
	input, want []byte
	err         error
}{
	{"normal",
		[]byte("foo]]>]]>"),
		[]byte("foo"),
		nil},
	{"empty frame",
		[]byte("]]>]]>"),
		[]byte(""),
		nil},
	{"next message",
		[]byte("foo]]>]]>bar]]>]]>"),
		[]byte("foo"), nil},
	{"no delim",
		[]byte("uhohwhathappened"),
		[]byte("uhohwhathappened"),
		io.ErrUnexpectedEOF},
	{"truncated delim",
		[]byte("foo]]>"),
		[]byte("foo]]>"),
		io.ErrUnexpectedEOF},
	{"rfc example rpc", rfcFramedRPC, rfcUnframedRPC, nil},
}

func TestFrameReaderReadByte(t *testing.T) {
	for _, tc := range framedTests {
		t.Run(tc.name, func(t *testing.T) {
			r := bufio.NewReader(bytes.NewReader(tc.input))
			fr := NewFrameReader(r)

			buf := make([]byte, 8192)

			var (
				b   byte
				n   int
				err error
			)
			for {
				b, err = fr.ReadByte()
				if err != nil {
					break
				}
				buf[n] = b
				n++
			}
			buf = buf[:n]

			if err != io.EOF && err != tc.err {
				t.Errorf("unexpected error during read (want: %v, got: %v)", tc.err, err)
			}

			if !bytes.Equal(buf, tc.want) {
				t.Errorf("unexpected read (want: %q, got: %q)", tc.want, buf)
			}
		})
	}
}

func TestFrameReaderRead(t *testing.T) {
	for _, tc := range framedTests {
		t.Run(tc.name, func(t *testing.T) {
			r := NewFrameReader(bufio.NewReader(bytes.NewReader(tc.input)))

			got, err := io.ReadAll(r)
			if err != tc.err {
				t.Errorf("unexpected error during read (want: %v, got: %v)", tc.err, err)
			}

			if !bytes.Equal(got, tc.want) {
				t.Errorf("unexpected read (want: %q, got: %q)", tc.want, got)
			}
		})
	}
}

func TestFrameWriter(t *testing.T) {
	buf := bytes.Buffer{}
	w := NewFrameWriter(bufio.NewWriter(&buf))
	n, err := w.Write([]byte("foo"))
	if err != nil {
		t.Fatalf("failed to write: %v", err)
	}

	if n != 3 {
		t.Errorf("failed number of bytes written (got %d, want %d)", n, 3)
	}

	if err := w.Close(); err != nil {
		t.Fatalf("failed to close writer: %v", err)
	}

	want := []byte("foo]]>]]>\n")
	if !bytes.Equal(buf.Bytes(), want) {
		t.Errorf("unexpected data written (want %q, got %q", want, buf.Bytes())
	}

}

func BenchmarkFrameReaderReadByte(b *testing.B) {
	src := bytes.NewReader(rfcFramedRPC)
	readers := []struct {
		name string
		r    io.ByteReader
	}{
		// test against bufio as a "baseline"
		{"bufio", bufio.NewReader(src)},
		{"framereader", NewFrameReader(bufio.NewReader(src))},
	}

	for _, bc := range readers {
		b.Run(bc.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				bc.r.ReadByte()
				b.SetBytes(1)
			}
		})
	}
}

type onlyReader struct {
	io.Reader
}

type onlyWriter struct {
	io.Writer
}

func BenchmarkFrameReaderRead(b *testing.B) {
	src := bytes.NewReader(rfcFramedRPC)
	readers := []struct {
		name string
		r    io.Reader
	}{
		// test against a standard reader and a bufio for a baseline
		{"bare", onlyReader{src}},
		{"bufio", onlyReader{bufio.NewReader(src)}},
		{"framereader", onlyReader{NewFrameReader(bufio.NewReader(src))}},
	}
	dstBuf := &bytes.Buffer{}
	dst := onlyWriter{dstBuf}

	for _, bc := range readers {
		b.Run(bc.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				src.Reset(rfcFramedRPC)
				dstBuf.Reset()
				n, err := io.Copy(&dst, bc.r)
				if err != nil {
					b.Fatal(err)
				}
				b.SetBytes(int64(n))
			}

		})
	}
}

var (
	rfcChunkedRPC = []byte(`
#4
<rpc
#18
 message-id="102"

#79
     xmlns="urn:ietf:params:xml:ns:netconf:base:1.0">
  <close-session/>
</rpc>
##
`)

	rfcUnchunkedRPC = []byte(`<rpc message-id="102"
     xmlns="urn:ietf:params:xml:ns:netconf:base:1.0">
  <close-session/>
</rpc>`)
)

var chunkedTests = []struct {
	name        string
	input, want []byte
	err         error
}{
	{"normal",
		[]byte("\n#3\nfoo\n##\n"),
		[]byte("foo"),
		nil},
	{"empty frame",
		[]byte("\n##\n"),
		[]byte(""),
		nil},
	{"multichunk",
		[]byte("\n#3\nfoo\n#3\nbar\n##\n"),
		[]byte("foobar"),
		nil},
	{"missing header",
		[]byte("uhoh"),
		[]byte(""),
		ErrMalformedChunk},
	{"eof in header",
		[]byte("\n#\n"),
		[]byte(""),
		io.ErrUnexpectedEOF},
	{"no headler",
		[]byte("\n00\n"),
		[]byte(""),
		ErrMalformedChunk},
	{"malformed header",
		[]byte("\n#big\n"),
		[]byte(""),
		ErrMalformedChunk},
	{"zero len chunk",
		[]byte("\n#0\n"),
		[]byte(""),
		ErrMalformedChunk},
	{"too big chunk",
		[]byte("\n#4294967296\n"),
		[]byte(""),
		ErrMalformedChunk},
	{"rfc example rpc", rfcChunkedRPC, rfcUnchunkedRPC, nil},
}

func TestChunkReaderReadByte(t *testing.T) {
	for _, tc := range chunkedTests {
		t.Run(tc.name, func(t *testing.T) {
			r := bufio.NewReader(bytes.NewReader(tc.input))
			cr := NewChunkReader(r)

			buf := make([]byte, 8192)

			var (
				b   byte
				n   int
				err error
			)
			for {
				b, err = cr.ReadByte()
				if err != nil {
					break
				}
				buf[n] = b
				n++
			}
			buf = buf[:n]

			if err != io.EOF && err != tc.err {
				t.Errorf("unexpected error during read (want: %v, got: %v)", tc.err, err)
			}

			if !bytes.Equal(buf, tc.want) {
				t.Errorf("unexpected read (want: %q, got: %q)", tc.want, buf)
			}
		})
	}
}

func TestChunkReaderRead(t *testing.T) {
	for _, tc := range chunkedTests {
		t.Run(tc.name, func(t *testing.T) {
			r := NewChunkReader(bufio.NewReader(bytes.NewReader(tc.input)))

			got, err := io.ReadAll(r)
			if err != tc.err {
				t.Errorf("unexpected error during read (want: %v, got: %v)", tc.err, err)
			}

			if !bytes.Equal(got, tc.want) {
				t.Errorf("unexpected read (want: %q, got: %q)", tc.want, got)
			}
		})
	}
}

func TestChunkWriter(t *testing.T) {
	buf := bytes.Buffer{}
	w := NewChunkWriter(bufio.NewWriter(&buf))
	n, err := w.Write([]byte("foo"))
	if err != nil {
		t.Fatalf("failed to write: %v", err)
	}

	if n != 3 {
		t.Errorf("failed number of bytes written (got %d, want %d)", n, 3)
	}

	n, err = w.Write([]byte("bar"))
	if err != nil {
		t.Fatalf("failed to write: %v", err)
	}

	if n != 3 {
		t.Errorf("failed number of bytes written (got %d, want %d)", n, 3)
	}

	if err := w.Close(); err != nil {
		t.Fatalf("failed to close writer: %v", err)
	}

	want := []byte("\n#3\nfoo\n#3\nbar\n##\n")
	if !bytes.Equal(buf.Bytes(), want) {
		t.Errorf("unexpected data written (want %q, got %q", want, buf.Bytes())
	}

}

func BenchmarkChunkedReadByte(b *testing.B) {
	src := bytes.NewReader(rfcChunkedRPC)
	readers := []struct {
		name string
		r    io.ByteReader
	}{
		// test against bufio as a "baseline"
		{"bufio", bufio.NewReader(src)},
		{"chunkreader", NewFrameReader(bufio.NewReader(src))},
	}

	for _, bc := range readers {
		b.Run(bc.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				bc.r.ReadByte()
				b.SetBytes(1)
			}
		})
	}
}

func BenchmarkChunkedRead(b *testing.B) {
	src := bytes.NewReader(rfcChunkedRPC)
	readers := []struct {
		name string
		r    io.Reader
	}{
		// test against a standard reader and a bufio for a baseline
		{"bare", onlyReader{src}},
		{"bufio", onlyReader{bufio.NewReader(src)}},
		{"chunkedreader", onlyReader{NewChunkReader(bufio.NewReader(src))}},
	}
	dstBuf := &bytes.Buffer{}
	dst := onlyWriter{dstBuf}

	for _, bc := range readers {
		b.Run(bc.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				src.Reset(rfcChunkedRPC)
				dstBuf.Reset()
				n, err := io.Copy(&dst, bc.r)
				if err != nil {
					b.Fatal(err)
				}
				b.SetBytes(int64(n))
			}
		})
	}
}