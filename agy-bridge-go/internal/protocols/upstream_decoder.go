package protocols

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/jeffhuen/muse-bridge/agy-bridge-go/internal/upstream"
)

// StreamEventType represents the lifecycle event emitted during stream decoding.
type StreamEventType string

const (
	StreamEventPart     StreamEventType = "part"
	StreamEventUsage    StreamEventType = "usage"
	StreamEventTerminal StreamEventType = "terminal"
	StreamEventError    StreamEventType = "error"
)

// StreamEvent is a typed event decoded from upstream Google SSE data.
type StreamEvent struct {
	Type         StreamEventType
	Part         upstream.Part
	FinishReason string
	Usage        *upstream.UsageMetadata
	Error        error
}

// UpstreamDecoder reads raw SSE stream from PredictionService and yields structured StreamEvents
// while preserving original part order, tool call identity, and thought signatures.
type UpstreamDecoder struct {
	reader           *bufio.Reader
	pendingParts     []upstream.Part
	pendingTerminal  string
	terminalReceived bool
	readErr          error
}

// NewUpstreamDecoder creates a decoder over an SSE reader.
func NewUpstreamDecoder(r io.Reader) *UpstreamDecoder {
	return &UpstreamDecoder{
		reader: bufio.NewReader(r),
	}
}

// Next returns the next StreamEvent from the upstream stream.
// It returns (nil, io.EOF) when the stream has cleanly finished.
func (d *UpstreamDecoder) Next() (*StreamEvent, error) {
	// Deliver any pending parts from the last candidate
	if len(d.pendingParts) > 0 {
		part := d.pendingParts[0]
		d.pendingParts = d.pendingParts[1:]
		return &StreamEvent{
			Type: StreamEventPart,
			Part: part,
		}, nil
	}

	// Deliver pending terminal event after parts are exhausted
	if d.pendingTerminal != "" {
		reason := d.pendingTerminal
		d.pendingTerminal = ""
		d.terminalReceived = true
		return &StreamEvent{
			Type:         StreamEventTerminal,
			FinishReason: reason,
		}, nil
	}

	if d.readErr != nil {
		err := d.readErr
		d.readErr = nil
		return nil, err
	}

	for {
		line, err := d.reader.ReadBytes('\n')
		if len(line) > 0 {
			lineStr := strings.TrimSpace(string(line))
			if strings.HasPrefix(lineStr, "data:") {
				dataStr := strings.TrimSpace(lineStr[5:])
				var event upstream.SSEStreamEvent
				if json.Unmarshal([]byte(dataStr), &event) == nil {
					if event.Error != nil {
						d.readErr = fmt.Errorf("upstream error %d: %s (%s)", event.Error.Code, event.Error.Message, event.Error.Status)
						return &StreamEvent{
							Type:  StreamEventError,
							Error: d.readErr,
						}, nil
					}
					if event.Response != nil {
						resp := event.Response
						var usageEvent *StreamEvent
						if resp.UsageMetadata != nil {
							usageEvent = &StreamEvent{
								Type:  StreamEventUsage,
								Usage: resp.UsageMetadata,
							}
						}

						for _, cand := range resp.Candidates {
							if len(cand.Content.Parts) > 0 {
								d.pendingParts = append(d.pendingParts, cand.Content.Parts...)
							}
							if cand.FinishReason != "" {
								d.pendingTerminal = cand.FinishReason
							}
						}

						if usageEvent != nil {
							return usageEvent, nil
						}

						if len(d.pendingParts) > 0 {
							part := d.pendingParts[0]
							d.pendingParts = d.pendingParts[1:]
							return &StreamEvent{
								Type: StreamEventPart,
								Part: part,
							}, nil
						}

						if d.pendingTerminal != "" {
							reason := d.pendingTerminal
							d.pendingTerminal = ""
							d.terminalReceived = true
							return &StreamEvent{
								Type:         StreamEventTerminal,
								FinishReason: reason,
							}, nil
						}
					}
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				if !d.terminalReceived {
					return nil, fmt.Errorf("upstream stream ended prematurely without finish reason")
				}
				return nil, io.EOF
			}
			return nil, err
		}
	}
}

// TurnAccumulator collects all decoded parts from an upstream stream into a complete turn representation.
type TurnAccumulator struct {
	Parts            []upstream.Part
	Usage            *upstream.UsageMetadata
	FinishReason     string
	VisibleText      string
	ThoughtText      string
	LastTextSig      string
	HasToolCalls     bool
	TerminalReceived bool
}

// AccumulateTurn reads all events from an UpstreamDecoder into a TurnAccumulator.
func AccumulateTurn(decoder *UpstreamDecoder) (*TurnAccumulator, error) {
	acc := &TurnAccumulator{}
	var visibleBuilder strings.Builder
	var thoughtBuilder strings.Builder

	for {
		event, err := decoder.Next()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		switch event.Type {
		case StreamEventPart:
			acc.Parts = append(acc.Parts, event.Part)
			if event.Part.FunctionCall != nil {
				acc.HasToolCalls = true
			} else if event.Part.Thought {
				thoughtBuilder.WriteString(event.Part.Text)
			} else {
				visibleBuilder.WriteString(event.Part.Text)
				if event.Part.ThoughtSignature != "" {
					acc.LastTextSig = event.Part.ThoughtSignature
				}
			}
		case StreamEventUsage:
			acc.Usage = event.Usage
		case StreamEventTerminal:
			acc.FinishReason = event.FinishReason
			acc.TerminalReceived = true
		case StreamEventError:
			return nil, event.Error
		}
	}

	acc.VisibleText = visibleBuilder.String()
	acc.ThoughtText = thoughtBuilder.String()
	if !acc.TerminalReceived {
		return nil, fmt.Errorf("upstream stream ended prematurely without finish reason")
	}
	return acc, nil
}
