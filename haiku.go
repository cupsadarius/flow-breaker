package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
)

// ── Haiku fallback ───────────────────────────────────────────────────────────
//
// parseHaiku resolves a time expression the heuristics couldn't place, using a
// single Claude Haiku 4.5 call with strict structured output (the 5-field
// contract). It is the fallback leg of Parse's pipeline: only reached when a
// time-ish signal is present, and every failure below is caught by the caller's
// inbox floor.
//
// Cost note: Haiku 4.5 is ~$1/$5 per MTok in/out; a capture is a few hundred
// tokens each way, so ~$0.0003 per call. Cheap enough to use liberally as a
// heuristics backstop.

// haikuModel is the typed model constant for Claude Haiku 4.5.
const haikuModel = anthropic.ModelClaudeHaiku4_5_20251001

// parseResultSchema is the strict JSON schema for the 5-field contract. Note:
// structured outputs do NOT support minLength/maxLength/numeric constraints, so
// none are used; additionalProperties:false and an explicit required list are
// mandatory.
var parseResultSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"properties": map[string]any{
		"time": map[string]any{"type": "string"},
		"date": map[string]any{"type": "string"},
		"recurrence": map[string]any{
			"type": "string",
			"enum": []string{"once", "daily", "weekdays", "weekly", "monthly"},
		},
		"days": map[string]any{
			"type":  "array",
			"items": map[string]any{"type": "string"},
		},
		"desc": map[string]any{"type": "string"},
	},
	"required": []string{"time", "date", "recurrence", "days", "desc"},
}

// haikuResult mirrors the strict schema for unmarshalling. Recurrence is decoded
// as a plain string then cast onto the store's Recurrence enum.
type haikuResult struct {
	Time       string   `json:"time"`
	Date       string   `json:"date"`
	Recurrence string   `json:"recurrence"`
	Days       []string `json:"days"`
	Desc       string   `json:"desc"`
}

// parseHaiku resolves `text` into the 5-field contract via one Haiku call. It
// respects ctx (the caller passes a 3s timeout) and uses the injected `now` (not
// the wall clock) so it agrees with the heuristics on "today". If
// ANTHROPIC_API_KEY is unset it returns an error immediately without touching
// the network.
func parseHaiku(ctx context.Context, text string, now time.Time) (ParseResult, error) {
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		return ParseResult{}, fmt.Errorf("parseHaiku: ANTHROPIC_API_KEY not set")
	}

	system := fmt.Sprintf(`You convert a short task-capture note into a structured schedule.

The current local time ("now") is %s (%s).

Resolve the note's time expression into exactly these fields:
- time: "HH:MM" 24-hour, or "" if the note names no time.
- date: "YYYY-MM-DD". Use today's date if the note doesn't resolve a specific day. Roll to a later day when the expression clearly implies it (e.g. "tomorrow", or an offset crossing midnight).
- recurrence: one of once, daily, weekdays, weekly, monthly.
- days: for weekly recurrence, the weekday set as lowercase 3-letter codes (sun, mon, tue, wed, thu, fri, sat); otherwise an empty array.
- desc: the note with the time expression removed, trimmed.

Interpret bare hours by common sense for a personal reminder (e.g. "at 3" in the afternoon is 15:00). The note may be in English or Romanian.`,
		now.Format("2006-01-02 15:04"), now.Format("Monday"))

	client := anthropic.NewClient() // reads ANTHROPIC_API_KEY from env

	msg, err := client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     haikuModel,
		MaxTokens: 512,
		System:    []anthropic.TextBlockParam{{Text: system}},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(text)),
		},
		OutputConfig: anthropic.OutputConfigParam{
			Format: anthropic.JSONOutputFormatParam{Schema: parseResultSchema},
		},
	})
	if err != nil {
		return ParseResult{}, fmt.Errorf("parseHaiku: request failed: %w", err)
	}

	// Concatenate the text blocks — structured output arrives as JSON text.
	var raw string
	for _, block := range msg.Content {
		if tb, ok := block.AsAny().(anthropic.TextBlock); ok {
			raw += tb.Text
		}
	}
	if raw == "" {
		return ParseResult{}, fmt.Errorf("parseHaiku: empty response content")
	}

	var hr haikuResult
	if err := json.Unmarshal([]byte(raw), &hr); err != nil {
		return ParseResult{}, fmt.Errorf("parseHaiku: bad JSON %q: %w", raw, err)
	}

	return ParseResult{
		Time:       hr.Time,
		Date:       hr.Date,
		Recurrence: Recurrence(hr.Recurrence),
		Days:       hr.Days,
		Desc:       hr.Desc,
	}, nil
}
