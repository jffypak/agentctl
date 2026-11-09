package domain

import "time"

// AckCandidate is a pasted message that a UserPromptSubmit may acknowledge.
type AckCandidate struct {
	ID         int64
	State      MessageState // sent or unconfirmed (others are never matched)
	BodySHA256 string
	ClaimedAt  time.Time // the paste may race its own MarkSent: the window opens at the claim
	SentAt     time.Time
}

// AckInput is one UserPromptSubmit seen for a session.
type AckInput struct {
	PromptSHA256 string        // hex sha256 of the submitted prompt ("" when unknown)
	At           time.Time     // when the submit was recorded
	Window       time.Duration // AckWindow
	Candidates   []AckCandidate
}

// AckDecision is MatchAck's verdict; MessageID 0 means "no message".
type AckDecision struct {
	MessageID int64
	Match     AckMatch
}

// MatchAck applies Delivery rule 4: a submit whose prompt hash equals a
// pasted message's body hash acknowledges it (ack_match=hash), including a
// late match on an unconfirmed message. Otherwise the first submit within
// Window of a message being sent acknowledges the oldest such sent message
// (ack_match=timing); Claude may report a large paste as "[Pasted text #1]",
// so the hash cannot be relied on. Unconfirmed messages are never matched by
// timing: their window has passed. Ties go to the oldest message.
func MatchAck(in AckInput) AckDecision {
	if in.PromptSHA256 != "" {
		for _, c := range sortedByID(in.Candidates) {
			if (c.State == MsgSent || c.State == MsgUnconfirmed) && c.BodySHA256 == in.PromptSHA256 {
				return AckDecision{c.ID, AckHash}
			}
		}
	}
	for _, c := range sortedByID(in.Candidates) {
		if c.State != MsgSent || c.SentAt.IsZero() {
			continue
		}
		open := c.SentAt
		if !c.ClaimedAt.IsZero() && c.ClaimedAt.Before(open) {
			open = c.ClaimedAt
		}
		if !in.At.Before(open) && !in.At.After(c.SentAt.Add(in.Window)) {
			return AckDecision{c.ID, AckTiming}
		}
	}
	return AckDecision{}
}

func sortedByID(cs []AckCandidate) []AckCandidate {
	out := append([]AckCandidate(nil), cs...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].ID < out[j-1].ID; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
