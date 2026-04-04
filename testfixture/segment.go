package testfixture

// SegmentEvent represents a single event in the Segment Batch API payload.
type SegmentEvent struct {
	Type        string                 `json:"type"`
	AnonymousID string                 `json:"anonymousId"`
	Event       string                 `json:"event"`
	MessageID   string                 `json:"messageId"`
	Timestamp   string                 `json:"timestamp"`
	Context     map[string]interface{} `json:"context"`
	Properties  map[string]interface{} `json:"properties"`
}

// SegmentBatch represents the top-level payload sent to the Segment Batch API.
type SegmentBatch struct {
	Batch []SegmentEvent `json:"batch"`
}
