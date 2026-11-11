package agentstream

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"log/slog"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
)

// Reader reads NDJSON lines from an io.ReadCloser and emits parsed
// StreamEvents on a channel. It is tolerant of malformed lines, logging
// parse errors and continuing to read the stream.
type Reader struct {
	logger       *slog.Logger
	translator   Translator
	maxLineBytes int
}

// defaultMaxLineBytes caps one stream line. Tool results often pass 64 KiB;
// a line over the cap is skipped with a warning.
const defaultMaxLineBytes = 1 << 20

// maxLoggedLineBytes bounds how much of an unparseable line is logged.
const maxLoggedLineBytes = 512

// ReaderOption configures optional Reader behaviour.
type ReaderOption func(*Reader)

// WithTranslator configures the Reader to convert each raw log line into
// stream events using t, instead of the default passthrough translator
// (Osmia's native NDJSON envelope format, parsed via ParseEvent).
func WithTranslator(t Translator) ReaderOption {
	return func(r *Reader) {
		r.translator = t
	}
}

// NewReader creates a Reader with the given logger. By default it uses the
// passthrough translator; pass WithTranslator to plug in an engine-specific
// stream format translator.
func NewReader(logger *slog.Logger, opts ...ReaderOption) *Reader {
	r := &Reader{
		logger:       logger,
		translator:   NewPassthroughTranslator(),
		maxLineBytes: defaultMaxLineBytes,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// ReadStream reads NDJSON lines from stream until EOF or context
// cancellation. Each successfully parsed line is sent to eventCh.
// Malformed lines are logged and skipped. The stream is closed when
// reading completes or the context is cancelled.
func (r *Reader) ReadStream(ctx context.Context, stream io.ReadCloser, eventCh chan<- *StreamEvent) error {
	defer stream.Close()

	// bufio.Reader lets readLine skip a line over the limit and carry on.
	br := bufio.NewReader(stream)
	for {
		line, tooLong, readErr := readLine(br, r.maxLineBytes)
		if tooLong {
			r.logger.Warn("skipping agent stream line over the size limit",
				"limit_bytes", r.maxLineBytes,
			)
		}
		if err := r.handleLine(ctx, line, eventCh); err != nil {
			return err
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

// handleLine translates one line and sends its events.
func (r *Reader) handleLine(ctx context.Context, line []byte, eventCh chan<- *StreamEvent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(line) == 0 {
		return nil
	}

	events, err := r.translator.Translate(line)
	if err != nil {
		logged := line
		if len(logged) > maxLoggedLineBytes {
			logged = logged[:maxLoggedLineBytes]
		}
		r.logger.Warn("failed to parse agent stream line",
			"error", err,
			"line", string(logged),
			"line_bytes", len(line),
		)
		return nil
	}

	for _, ev := range events {
		select {
		case eventCh <- ev:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// readLine returns the next line without its ending. A line whose content is
// over maxBytes is discarded and tooLong is set. At EOF it may return a final
// unterminated line together with io.EOF.
func readLine(br *bufio.Reader, maxBytes int) (line []byte, tooLong bool, err error) {
	for {
		chunk, err := br.ReadSlice('\n')
		// Leave room for a CRLF ending; the content is checked once it is trimmed.
		if !tooLong {
			if len(line)+len(chunk) > maxBytes+2 {
				tooLong, line = true, nil
			} else {
				line = append(line, chunk...)
			}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		line = bytes.TrimRight(line, "\r\n")
		if len(line) > maxBytes {
			return nil, true, err
		}
		return line, tooLong, err
	}
}

// ReadPodLogs connects to a running Kubernetes pod's log stream and reads
// NDJSON events from the specified container. It delegates to ReadStream
// for the actual parsing. The follow option is set so that the stream
// remains open until the container exits or the context is cancelled.
func (r *Reader) ReadPodLogs(
	ctx context.Context,
	client kubernetes.Interface,
	namespace, podName, containerName string,
	eventCh chan<- *StreamEvent,
) error {
	opts := &corev1.PodLogOptions{
		Follow:    true,
		Container: containerName,
	}

	stream, err := client.CoreV1().Pods(namespace).GetLogs(podName, opts).Stream(ctx)
	if err != nil {
		return err
	}

	return r.ReadStream(ctx, stream, eventCh)
}
