package agent

import (
	"context"
	"encoding/json"
	"unicode/utf8"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

// The original inner list cursor recaptures the same single-item document on
// continuation. The existing outer signed cursor binds the query and source;
// the digest also catches projection changes that do not advance that source.
type workListProjectionCursor struct {
	Version        int    `json:"v"`
	ListCursor     string `json:"list_cursor"`
	Digest         string `json:"digest"`
	NextByteOffset int64  `json:"next_byte_offset"`
}

// workListProjectionPage renders only the captured Q3 page. Fit candidates never
// re-query, and the final fragment resumes after its complete item, not before it.
func (r runtime) workListProjectionPage(ctx context.Context, base agentcontract.Envelope, binding string, cursor workListProjectionCursor, captured store.Q3Result) (agentcontract.Envelope, error) {
	inline, err := r.q3(base, captured)
	if err != nil {
		return inline, err
	}
	doc := inline.Result
	digest := sha256Hex(doc)
	if cursor.Digest != "" && digest != cursor.Digest {
		return coreError(base, "invalid_cursor", "work-list projection changed since its first page; restart the read", "restart_query", false), nil
	}
	if cursor.NextByteOffset < 0 || cursor.NextByteOffset >= int64(len(doc)) {
		return coreError(base, "invalid_cursor", "work-list projection offset is out of range", "restart_query", false), nil
	}
	offset := int(cursor.NextByteOffset)
	if !utf8.RuneStart(doc[offset]) {
		return coreError(base, "invalid_cursor", "work-list projection offset splits a code point", "restart_query", false), nil
	}
	at := func(n int) (agentcontract.Envelope, int, error) {
		end := offset + n
		nextCursor := captured.NextCursor
		var nextKind any
		if end < len(doc) {
			nextKind = "projection"
			raw, err := json.Marshal(workListProjectionCursor{Version: 1, ListCursor: cursor.ListCursor, Digest: digest, NextByteOffset: int64(end)})
			if err != nil {
				return base, 0, err
			}
			token := string(raw)
			nextCursor = &token
		} else if nextCursor != nil {
			nextKind = "items"
		}
		meta := captured.ResultMeta
		meta.NextCursor = nextCursor
		response, err := r.resultEnvelope(base, meta, r.scope(meta), map[string]any{
			"encoding": "json-fragment", "digest": digest, "total_bytes": len(doc),
			"offset": offset, "data": string(doc[offset:end]), "fragment_bytes": n,
			"document_complete": end == len(doc), "next_kind": nextKind,
		})
		if err != nil {
			return response, 0, err
		}
		if len(response.Omissions) >= agentcontract.MaxNotices {
			return coreError(base, "limit_exceeded", "work-list projection needs a continuation notice but the omission limit is exhausted", "restart_query", false), 0, nil
		}
		response.Omissions = append(response.Omissions, agentcontract.Notice{Kind: "work_list_projection_paged", Details: map[string]any{"total_bytes": len(doc), "offset": offset, "fragment_bytes": n, "remaining_bytes": len(doc) - end, "document_complete": end == len(doc)}})
		response, err = r.wrapCursor(ctx, response, binding, "summary")
		if err != nil {
			return response, 0, err
		}
		size, err := readEnvelopeSize(response)
		return response, size, err
	}
	length, err := continuityFragmentLength(doc, offset, func(n int) (bool, error) {
		_, size, err := at(n)
		return size <= agentcontract.MaxResultEnvelopeBytes, err
	})
	if err != nil {
		return base, err
	}
	if length == 0 {
		_, first := utf8.DecodeRune(doc[offset:])
		_, size, err := at(first)
		if err != nil {
			return base, err
		}
		return readSizeRefusal(base, size), nil
	}
	response, _, err := at(length)
	return response, err
}
