package agent

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// startChunkServer mimics the panel's /api/agent/upload chunk handler: chunk 0
// truncates, later chunks append, last is ignored (completion is via the result
// message). It returns the reassembled archive and a failure injector.
func startChunkServer(t *testing.T, failOnChunk int) (*httptest.Server, *bytes.Buffer) {
	t.Helper()
	var got bytes.Buffer
	var idx int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := idx
		idx++
		if failOnChunk >= 0 && cur == failOnChunk {
			httpxErr(w, 500)
			return
		}
		if _, err := io.Copy(&got, r.Body); err != nil {
			t.Fatalf("server copy: %v", err)
		}
		w.WriteHeader(200)
	}))
	return srv, &got
}

func httpxErr(w http.ResponseWriter, code int) { w.WriteHeader(code) }

// TestUploadStreamReassembles verifies the streamed, chunked upload reassembles
// into exactly the input bytes across sizes that exercise the partial-last and
// exact-chunk-boundary code paths. Uses a tiny injected chunk size so the
// boundaries are cheap to exercise.
func TestUploadStreamReassembles(t *testing.T) {
	const chunk = int64(8)
	cases := []int{
		0,              // empty stream
		1,              // tiny, single partial chunk
		int(chunk) - 1, // partial last chunk
		int(chunk),     // exact one chunk (boundary)
		int(chunk) + 1, // full + 1 byte
		2 * int(chunk), // exact two chunks (boundary)
		2*int(chunk) + int(chunk)/2,
	}
	for _, n := range cases {
		payload := bytes.Repeat([]byte("x"), n)
		srv, got := startChunkServer(t, -1)
		err := uploadStreamChunks(srv.URL+"/u?id=1&token=t", bytes.NewReader(payload), chunk)
		srv.Close()
		if err != nil {
			t.Fatalf("size %d: uploadStream returned %v", n, err)
		}
		if !bytes.Equal(got.Bytes(), payload) {
			t.Fatalf("size %d: reassembled %d bytes, want %d", n, got.Len(), len(payload))
		}
	}
}

// TestUploadStreamPropagatesServerError checks a mid-stream HTTP failure
// surfaces as an error rather than a silent success.
func TestUploadStreamPropagatesServerError(t *testing.T) {
	payload := bytes.Repeat([]byte("y"), 24) // 3 chunks at chunk=8
	srv, _ := startChunkServer(t, 1)         // fail the second chunk
	err := uploadStreamChunks(srv.URL+"/u?id=1&token=t", bytes.NewReader(payload), 8)
	srv.Close()
	if err == nil {
		t.Fatal("expected error from failing chunk server, got nil")
	}
}

// TestEofReaderFlagsEOF ensures eofReader marks eof only on a true EOF and lets a
// wrapped error pass through.
func TestEofReaderFlagsEOF(t *testing.T) {
	er := &eofReader{r: bytes.NewReader([]byte("abc"))}
	buf := make([]byte, 2)
	// A bytes.Reader (like io.PipeReader) returns the last bytes with nil and
	// delivers EOF on the following 0-byte read; eof must be set only then.
	for {
		n, err := er.Read(buf)
		if n == 0 && errors.Is(err, io.EOF) {
			break
		}
		if err != nil && !errors.Is(err, io.EOF) {
			t.Fatalf("unexpected err: %v", err)
		}
	}
	if !er.eof {
		t.Fatal("eof not set after draining reader")
	}

	want := errors.New("producer boom")
	er2 := &eofReader{r: errReader{err: want}}
	_, err := er2.Read(buf)
	if !errors.Is(err, want) {
		t.Fatalf("wrapped error lost: got %v want %v", err, want)
	}
	if er2.eof {
		t.Fatal("eof must NOT be set for a non-EOF producer error")
	}
}

// errReader is a reader that always fails with err (simulates a producer error
// signalled via io.Pipe.CloseWithError).
type errReader struct{ err error }

func (e errReader) Read(p []byte) (int, error) { return 0, e.err }

// TestPipeCountWriterCounts confirms the byte counter used for the reported
// archive size is accurate.
func TestPipeCountWriterCounts(t *testing.T) {
	var buf bytes.Buffer
	cw := &pipeCountWriter{w: &buf}
	if _, err := cw.Write([]byte(strings.Repeat("a", 100))); err != nil {
		t.Fatal(err)
	}
	if _, err := cw.Write([]byte(strings.Repeat("b", 50))); err != nil {
		t.Fatal(err)
	}
	if cw.n != 150 {
		t.Fatalf("counted %d, want 150", cw.n)
	}
	if buf.Len() != 150 {
		t.Fatalf("forwarded %d bytes, want 150", buf.Len())
	}
}
