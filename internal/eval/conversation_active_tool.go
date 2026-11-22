package eval

import "context"

// Wait for the fixture and keep servicing the same bounded, privacy-checked
// receive path. Only the fixture waiter runs concurrently; it never reads
// frames or mutates the transcript. Join it before any guidance can be sent.
func (flow *conversation) waitForActiveTool(
	ctx context.Context, fixture *activeToolFixture, args activeToolArgs, requestID string, turn int,
) error {
	receiveCtx, cancel := context.WithCancel(ctx)
	settled := make(chan struct{})
	var entryErr error
	go func() {
		defer close(settled)
		entryErr = fixture.waitEntered(receiveCtx, args)
		cancel()
	}()
	defer func() {
		cancel()
		<-settled
	}()
	for {
		reply, rejected := flow.receiveTurnReply(receiveCtx, requestID, turn)
		if rejected != nil {
			if receiveCtx.Err() != nil &&
				(rejected.code == "canceled_remote_unknown" || rejected.code == "timeout_remote_unknown") {
				<-settled
				return entryErr
			}
			return rejected
		}
		premature := reply.Kind != "stream"
		if premature {
			// A pre-barrier reply is diagnostic evidence, not the scripted outcome.
			reply.Kind, reply.Content, reply.Usage = "early_terminal", "", nil
		}
		if !flow.append(reply) {
			return &turnReplyError{Inconclusive, "event_limit"}
		}
		if premature {
			return &turnReplyError{Inconclusive, "terminal_before_active_tool"}
		}
	}
}
