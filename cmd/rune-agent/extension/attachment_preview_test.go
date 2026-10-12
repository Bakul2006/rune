// Copyright (C) 2017-2026 The Rune Authors
// SPDX-License-Identifier: GPL-3.0-or-later
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or (at
// your option) any later version.
//
// This program is distributed in the hope that it will be useful, but
// WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the GNU
// General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with this program. If not, see <https://www.gnu.org/licenses/>.

package extension

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguetui"
	"unstable.build/rune/internal/component/markdown"
	mdhandler "unstable.build/rune/internal/handler/markdown"
)

func TestEncodeParseAttachmentPartV1Roundtrip(t *testing.T) {
	tests := []struct {
		name string
		meta attachmentPartMeta
		body string
	}{
		{
			name: "file with body",
			meta: attachmentPartMeta{
				ID: "attachment-1", Kind: attachmentKindFile,
				Name: "main.go", Path: "pkg/main.go", Status: attachmentStatusOK,
			},
			body: "package main\n",
		},
		{
			name: "symbol with body",
			meta: attachmentPartMeta{
				ID: "attachment-2", Kind: attachmentKindSymbol,
				Name: "pkg.Symbol", Symbol: "pkg.Symbol", Status: attachmentStatusOK,
			},
			body: "## Definition\na.go:1",
		},
		{
			name: "image without body",
			meta: attachmentPartMeta{
				ID: "attachment-3", Kind: attachmentKindImage,
				Name: "shot.png", Path: "shot.png", Status: attachmentStatusOK,
			},
		},
		{
			name: "file failure keeps id and status but no image",
			meta: attachmentPartMeta{
				ID: "attachment-4", Kind: attachmentKindFile,
				Name: "gone.go", Path: "gone.go",
				Status: attachmentStatusError, Error: "no such file",
			},
			body: "could not be read: no such file",
		},
		{
			name: "unicode name and multiline body",
			meta: attachmentPartMeta{
				ID: "attachment-5", Kind: attachmentKindFile,
				Name: "résumé.pdf", Path: "docs/résumé.pdf", Status: attachmentStatusOK,
			},
			body: "line one\nline two\n日本語",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			encoded := encodeAttachmentPart(tc.meta, tc.body)
			gotMeta, gotBody, ok := parseAttachmentPartV1(encoded)
			require.True(t, ok)
			assert.Equal(t, tc.meta, gotMeta)
			assert.Equal(t, tc.body, gotBody)
		})
	}
}

func TestPreviewSpanPadsMarkdown(t *testing.T) {
	md, err := markdown.New("hello")
	require.NoError(t, err)
	span := newPreviewSpan(mdhandler.New(md))
	span.Resize(20, 4)
	w := term.NewStringWriter(20, 4)
	span.Draw(w)
	assert.Zero(t, w.Cells()[0].Ch)
	assert.NotZero(t, w.Cells()[1].Ch)
	assert.Zero(t, w.Cells()[19].Ch)
	exit, handled := span.Handle(term.Event{Type: term.EventKey, Key: term.KeyEsc})
	assert.True(t, exit)
	assert.True(t, handled)
}

func TestToolCallPreviewerOpensOneWindowPerToolCall(t *testing.T) {
	turn := dialoguetui.NewTurn(&dialoguetui.ComponentConfig{}, nil)
	turn.AddToolCall("a", "read_file", "", "")
	turn.AddToolCall("b", "write_stdin", "", "")
	turn.Resize(60, turn.Height(60))
	a, ok := turn.ToolCallAt(term.Coordinates{X: 2, Y: 0})
	require.True(t, ok)
	b, ok := turn.ToolCallAt(term.Coordinates{X: 2, Y: 1})
	require.True(t, ok)
	require.NotEqual(t, a.Key, b.Key)

	wm := &previewWindowManager{open: make(map[uint64]browserapi.Floating)}
	p := newToolCallPreviewer(&aiEditorHandler{wm: wm, n: stubNotifications{}})

	p.OpenToolCall(a)
	p.OpenToolCall(a)
	assert.Equal(t, []uint64{2}, wm.openIDs(), "clicking again replaces the preview")
	// The replaced window's Close arrives after its replacement opened.
	wm.deliverCloses(t)
	p.OpenToolCall(a)
	assert.Equal(t, []uint64{3}, wm.openIDs(), "a stale close must not orphan the preview")

	p.OpenToolCall(b)
	assert.Equal(t, []uint64{3, 4}, wm.openIDs(), "each tool call has its own preview")

	require.NoError(t, wm.CloseWindow(previewWindow(3)))
	wm.deliverCloses(t)
	p.OpenToolCall(a)
	assert.Equal(t, []uint64{4, 5}, wm.openIDs())
}

func TestToolCallPreviewMarkdown(t *testing.T) {
	for _, tc := range []struct {
		name    string
		details dialoguetui.ToolCallDetails
		want    []string
	}{
		{
			name:    "agent response is markdown",
			details: dialoguetui.ToolCallDetails{Name: "agent", Args: `{"description":"research"}`, Output: "# Findings\n\n**Important**", Done: true, MarkdownOutput: true},
			want:    []string{"# agent", "````json\n{\n  \"description\": \"research\"\n}\n````", "# Findings\n\n**Important**"},
		},
		{
			name:    "agent skill result is markdown",
			details: dialoguetui.ToolCallDetails{Name: "skill", Output: "# Research\n\n**Findings**", Done: true, MarkdownOutput: true},
			want:    []string{"# skill", "## Result\n\n# Research\n\n**Findings**"},
		},
		{
			name:    "other tool output is literal",
			details: dialoguetui.ToolCallDetails{Name: "read_file", Args: `{"path":"x"}`, Output: "# not a heading\n```embedded```", Done: true, IsError: true},
			want:    []string{"# read_file", "## Error", "# not a heading\n```embedded```"},
		},
		{
			name:    "JSON output gets syntax highlighting",
			details: dialoguetui.ToolCallDetails{Name: "web_fetch", Output: `{"result":true}`, Done: true},
			want:    []string{"````json\n{\"result\":true}\n````"},
		},
		{
			name:    "large integers survive formatting",
			details: dialoguetui.ToolCallDetails{Name: "agent", Args: `{"value":12345678901234567890}`},
			want:    []string{"12345678901234567890", "_Waiting for result…_"},
		},
		{
			name:    "empty completed response",
			details: dialoguetui.ToolCallDetails{Name: "write_stdin", Done: true},
			want:    []string{"## Result\n\n_(no output)_"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := toolCallPreviewMarkdown(tc.details)
			for _, part := range tc.want {
				assert.Contains(t, got, part)
			}
			if tc.details.Name == "read_file" {
				assert.Contains(t, got, "````text\n# not a heading")
			}
		})
	}
}

func TestParseAttachmentPartV1RejectsNonV1Text(t *testing.T) {
	tests := []struct {
		name string
		text string
	}{
		{"plain text", "please look at the parser"},
		{"legacy file prefix", "Attached file pkg/main.go:\npackage main"},
		{"malformed json header", "Rune attachment v1: {not json"},
		{"empty", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, ok := parseAttachmentPartV1(tc.text)
			assert.False(t, ok)
		})
	}
}

func TestParseAttachmentPartV1(t *testing.T) {
	tests := []struct {
		name   string
		text   string
		want   dialoguetui.Attachment
		wantOK bool
	}{
		{
			name: "workspace file",
			text: encodeAttachmentPart(attachmentPartMeta{
				ID: "attachment-1", Kind: attachmentKindFile,
				Name: "main.go", Path: "pkg/main.go", Status: attachmentStatusOK,
			}, "package main"),
			want:   dialoguetui.NewWorkspaceFileAttachment("pkg/main.go"),
			wantOK: true,
		},
		{
			name: "absolute path keeps the dragged-file icon",
			text: encodeAttachmentPart(attachmentPartMeta{
				ID: "attachment-1", Kind: attachmentKindFile,
				Name: "notes.txt", Path: "/tmp/notes.txt", Status: attachmentStatusOK,
			}, "hi"),
			want:   dialoguetui.NewAttachment("/tmp/notes.txt"),
			wantOK: true,
		},
		{
			name: "symbol",
			text: encodeAttachmentPart(attachmentPartMeta{
				ID: "attachment-1", Kind: attachmentKindSymbol,
				Name: "pkg.Symbol", Symbol: "pkg.Symbol", Status: attachmentStatusOK,
			}, "## Definition\na.go:1"),
			want: dialoguetui.NewResolvedSymbolAttachment(
				"pkg.Symbol", "## Definition\na.go:1"),
			wantOK: true,
		},
		{
			name: "image",
			text: encodeAttachmentPart(attachmentPartMeta{
				ID: "attachment-1", Kind: attachmentKindImage,
				Name: "shot.png", Path: "shot.png", Status: attachmentStatusOK,
			}, ""),
			want:   dialoguetui.NewWorkspaceFileAttachment("shot.png"),
			wantOK: true,
		},
		{
			name: "unreadable file still yields a chip",
			text: encodeAttachmentPart(attachmentPartMeta{
				ID: "attachment-1", Kind: attachmentKindFile,
				Name: "gone.go", Path: "pkg/gone.go",
				Status: attachmentStatusError, Error: "no such file",
			}, "could not be read: no such file"),
			want:   dialoguetui.NewWorkspaceFileAttachment("pkg/gone.go"),
			wantOK: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseAttachmentPart(tc.text)
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestAttachmentContentPartsV1Roundtrip(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(root, "main.go"), []byte("package main"), 0o600))

	h := &aiEditorHandler{
		fs: testLocalFS{root: root},
		n:  stubNotifications{},
	}
	sent := []dialoguetui.Attachment{
		dialoguetui.NewWorkspaceFileAttachment("main.go"),
		dialoguetui.NewResolvedSymbolAttachment(
			"value", "## Definition\n- `scope.go:3`"),
	}
	finalized := make([]finalizedAttachment, len(sent))
	for i, a := range sent {
		finalized[i] = finalizedAttachment{id: fmt.Sprintf("attachment-%d", i+1), Attachment: a}
	}

	parts := h.attachmentContentParts(context.Background(), finalized)
	require.Len(t, parts, 2)
	for _, p := range parts {
		assert.Contains(t, p.Text, attachmentPartV1Prefix)
	}

	msg := llmapi.Message{
		Role:    llmapi.RoleUser,
		Content: "look",
		MultiContent: append([]llmapi.ContentPart{{
			Type: llmapi.ContentPartTypeText, Text: "look",
		}}, parts...),
	}

	assert.Equal(t, sent, replayedAttachments(msg))
}

func TestAttachmentContentPartsV1Failures(t *testing.T) {
	root := t.TempDir()
	h := &aiEditorHandler{
		fs: testLocalFS{root: root},
		n:  stubNotifications{},
	}
	missing := dialoguetui.NewWorkspaceFileAttachment("gone.go")
	finalized := []finalizedAttachment{{id: "attachment-1", Attachment: missing}}

	parts := h.attachmentContentParts(context.Background(), finalized)
	require.Len(t, parts, 1)
	require.Equal(t, llmapi.ContentPartTypeText, parts[0].Type)

	meta, _, ok := parseAttachmentPartV1(parts[0].Text)
	require.True(t, ok)
	assert.Equal(t, "attachment-1", meta.ID)
	assert.Equal(t, attachmentKindFile, meta.Kind)
	assert.Equal(t, attachmentStatusError, meta.Status)
	assert.NotEmpty(t, meta.Error)
}

func TestParseAttachmentPart(t *testing.T) {
	tests := []struct {
		name   string
		text   string
		want   dialoguetui.Attachment
		wantOK bool
	}{{
		name:   "workspace file",
		text:   "Attached file pkg/main.go:\npackage main",
		want:   dialoguetui.NewWorkspaceFileAttachment("pkg/main.go"),
		wantOK: true,
	}, {
		name:   "absolute path keeps the dragged-file icon",
		text:   "Attached file /tmp/notes.txt:\nhi",
		want:   dialoguetui.NewAttachment("/tmp/notes.txt"),
		wantOK: true,
	}, {
		name: "symbol",
		text: "Attached symbol pkg.Symbol:\n## Definition\na.go:1",
		want: dialoguetui.NewResolvedSymbolAttachment(
			"pkg.Symbol", "## Definition\na.go:1"),
		wantOK: true,
	}, {
		name:   "image",
		text:   "Attached image shot.png:",
		want:   dialoguetui.NewWorkspaceFileAttachment("shot.png"),
		wantOK: true,
	}, {
		name:   "unreadable file still yields a chip",
		text:   "Attached file pkg/gone.go could not be read: no such file",
		want:   dialoguetui.NewWorkspaceFileAttachment("pkg/gone.go"),
		wantOK: true,
	}, {
		name: "ordinary text",
		text: "please look at the parser",
	}}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseAttachmentPart(tc.text)
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestReplayedAttachmentsIgnoresPlainMessages(t *testing.T) {
	assert.Nil(t, replayedAttachments(llmapi.Message{
		Role: llmapi.RoleUser, Content: "Attached file foo.go: is what I meant",
	}))
}

type previewWindow uint64

func (w previewWindow) WindowID() uint64 { return uint64(w) }

// previewWindowManager closes windows like the browser does over RPC:
// the window goes away at once, but its handler's Close only runs later,
// when the test calls deliverCloses.
type previewWindowManager struct {
	browserapi.WindowManager
	nextID  uint64
	open    map[uint64]browserapi.Floating
	closing []browserapi.Floating
}

func (m *previewWindowManager) Floating(
	h browserapi.Floating, _ browserapi.FloatingConfig,
) (browserapi.Window, error) {
	m.nextID++
	m.open[m.nextID] = h
	return previewWindow(m.nextID), nil
}

func (m *previewWindowManager) CloseWindow(win browserapi.Window) error {
	h, ok := m.open[win.WindowID()]
	if !ok {
		return nil
	}
	delete(m.open, win.WindowID())
	m.closing = append(m.closing, h)
	return nil
}

func (m *previewWindowManager) deliverCloses(t *testing.T) {
	for len(m.closing) > 0 {
		h := m.closing[0]
		m.closing = m.closing[1:]
		require.NoError(t, h.Close())
	}
}

func (m *previewWindowManager) openIDs() []uint64 {
	return slices.Sorted(maps.Keys(m.open))
}
