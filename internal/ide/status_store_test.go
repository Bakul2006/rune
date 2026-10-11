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

package ide

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/handler/handlertest"
	"unstable.build/rune/internal/term/programstatus"
)

func TestStatusInboxEntries(t *testing.T) {
	parse := func(s string) workspaceapi.URI {
		uri, err := workspaceapi.ParseURI(s)
		require.NoError(t, err)
		return uri
	}
	worker := parse("rune-agent://model/worker")
	build := parse("rune-agent://model/build")
	agent := parse("rune-agent://model/agent")
	idle := parse("rune-agent://model/idle")
	pty := parse("file:///dev/ttys042")
	// Without icons the rows start with the state's name.
	in := statusInbox{cfg: browser.TabStatusConfig{
		BlockedAttr:     term.Attributes{Fg: term.ColorYellow},
		ErrorAttr:       term.Attributes{Fg: term.ColorRed},
		DoneIconAttr:    term.Attributes{Fg: term.ColorSilver},
		IdleIconAttr:    term.Attributes{Fg: term.ColorBlue},
		WorkingIconAttr: term.Attributes{Fg: term.ColorBlue},
	}}

	assert.Empty(t, in.entries(nil))

	sources := []statusItem{
		{slot: 0, wsName: "1 home", uri: worker, name: "worker", status: programstatus.Summary{
			State: programstatus.Working, Progress: 40, Title: "cargo build",
		}},
		{slot: 1, wsName: "2 proj", uri: build, name: "build", status: programstatus.Summary{
			State: programstatus.Error, Progress: -1, Msg: "exit status 2",
		}},
		{slot: 1, wsName: "2 proj", uri: agent, name: "agent", status: programstatus.Summary{
			State: programstatus.Blocked, Kind: programstatus.KindPermission, Progress: -1,
			App: "claude", Msg: "run make test?",
			Records: []programstatus.Record{
				{Report: programstatus.Report{
					State: programstatus.Working, Progress: -1, App: "claude",
				}},
				{Report: programstatus.Report{
					ID: "tool/bash", State: programstatus.Blocked,
					Kind: programstatus.KindPermission, Progress: -1,
					App: "claude", Msg: "run make test?",
				}},
				{Report: programstatus.Report{
					ID: "tool/edit", State: programstatus.Done, Progress: -1, Msg: "wrote main.go",
				}},
				{Report: programstatus.Report{
					ID: "a/b/c/d/e/f", State: programstatus.Working, Progress: 10,
				}},
			},
		}},
		{slot: 1, wsName: "2 proj", uri: idle, name: "idle", status: programstatus.Summary{Progress: -1}},
		{slot: 1, wsName: "2 proj", uri: pty, name: detachedStatusLabel, status: programstatus.Summary{
			State: programstatus.Done, Progress: -1, Msg: "ok",
		}},
	}
	entries := in.entries(sources)
	var rows []string
	for _, e := range entries {
		rows = append(rows, e.display)
	}
	assert.Equal(t, []string{
		"blocked  2 proj  agent (claude)  run make test?",
		"    blocked  bash  run make test?",
		"    done  edit  wrote main.go",
		"        working  f  10%",
		"error  2 proj  build  exit status 2",
		"done  2 proj  terminal  ok",
		"working  1 home  worker  cargo build  40%",
	}, rows)
	assert.Equal(t, agent, entries[0].uri)
	assert.Equal(t, workspaceapi.URI{}, entries[0].ws)
	assert.Equal(t, programstatus.Blocked, entries[0].state)
	assert.Equal(t, agent, entries[1].uri, "records point at their source")
	assert.Equal(t, programstatus.Done, entries[2].state)
	assert.Equal(t, term.Attributes{Fg: term.ColorYellow}, entries[0].attr)
	assert.Equal(t, term.Attributes{Fg: term.ColorSilver}, entries[2].attr, "icon attributes stand in for missing name attributes")
	assert.Equal(t, term.Attributes{Fg: term.ColorRed}, entries[4].attr)
	assert.Equal(t, pty, entries[5].uri, "a terminal that is no tab is listed")
	assert.Equal(t, worker, entries[6].uri)
	assert.Equal(t, workspaceapi.URI{}, entries[6].ws)
	assert.Equal(t, term.Attributes{Fg: term.ColorBlue}, entries[6].attr)

	t.Run("sources are left untouched", func(t *testing.T) {
		assert.Len(t, sources, 5)
		assert.Equal(t, worker, sources[0].uri)
	})

	t.Run("a single record is not repeated beneath its source", func(t *testing.T) {
		got := in.entries([]statusItem{{wsName: "1 home", uri: worker, name: "worker",
			status: programstatus.Summary{
				State: programstatus.Working, Progress: -1,
				Records: []programstatus.Record{{Report: programstatus.Report{
					ID: "only", State: programstatus.Working, Progress: -1,
				}}},
			}}})
		require.Len(t, got, 1)
		assert.Equal(t, "working  1 home  worker", got[0].display)
	})

	t.Run("ties sort by slot then name", func(t *testing.T) {
		got := in.entries([]statusItem{
			{slot: 1, wsName: "2", uri: build, name: "zeta", status: programstatus.Summary{State: programstatus.Working, Progress: -1}},
			{slot: 1, wsName: "2", uri: agent, name: "alpha", status: programstatus.Summary{State: programstatus.Working, Progress: -1}},
			{slot: 0, wsName: "1", uri: worker, name: "omega", status: programstatus.Summary{State: programstatus.Working, Progress: -1}},
		})
		require.Len(t, got, 3)
		assert.Equal(t, "working  1  omega", got[0].display)
		assert.Equal(t, "working  2  alpha", got[1].display)
		assert.Equal(t, "working  2  zeta", got[2].display)
	})
}

func TestStatusInboxRow(t *testing.T) {
	in := statusInbox{cfg: browser.TabStatusConfig{
		Spinner:   []rune("⠋⠙"),
		DoneIcon:  '✓',
		ErrorIcon: '✗',
		BlockedIcons: map[programstatus.Kind]rune{
			programstatus.KindNone:       '!',
			programstatus.KindPermission: '?',
		},
	}}
	for _, tc := range []struct {
		name     string
		state    programstatus.State
		kind     programstatus.Kind
		depth    int
		ws       string
		label    string
		title    string
		progress int
		msg      string
		want     string
	}{
		{name: "every field", state: programstatus.Working, ws: "1", label: "brew", title: "Brew", progress: 50, msg: "Installing", want: "⠋  1  brew  Brew  50%  Installing"},
		{name: "empty fields skipped", state: programstatus.Working, label: "brew", progress: -1, want: "⠋  brew"},
		{name: "progress only while working", state: programstatus.Done, label: "brew", progress: 50, want: "✓  brew"},
		{name: "zero progress shown", state: programstatus.Working, label: "brew", progress: 0, want: "⠋  brew  0%"},
		{name: "blocked kind icon", state: programstatus.Blocked, kind: programstatus.KindPermission, label: "agent", progress: -1, want: "?  agent"},
		{name: "blocked unknown kind falls back", state: programstatus.Blocked, kind: programstatus.KindAuth, label: "agent", progress: -1, want: "!  agent"},
		{name: "error icon", state: programstatus.Error, label: "make", progress: -1, msg: "boom", want: "✗  make  boom"},
		{name: "idle has no icon", state: programstatus.Idle, label: "sh", progress: -1, want: "idle  sh"},
		{name: "depth indents", state: programstatus.Done, depth: 2, label: "edit", progress: -1, want: "    ✓  edit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, in.row(tc.state, tc.kind, tc.depth,
				tc.ws, tc.label, tc.title, tc.progress, tc.msg))
		})
	}

	t.Run("without icons the state is named", func(t *testing.T) {
		bare := statusInbox{}
		assert.Equal(t, "working", bare.glyph(programstatus.Working, programstatus.KindNone))
		assert.Equal(t, "blocked", bare.glyph(programstatus.Blocked, programstatus.KindPermission))
		assert.Equal(t, "done", bare.glyph(programstatus.Done, programstatus.KindNone))
		assert.Equal(t, "error", bare.glyph(programstatus.Error, programstatus.KindNone))
		assert.Equal(t, "idle", bare.glyph(programstatus.Idle, programstatus.KindNone))
	})
}

func TestStatusView(t *testing.T) {
	entries := []statusEntry{
		{display: "B  1  claude  run make test?", state: programstatus.Blocked},
		{display: "E  2  build  boom", state: programstatus.Error},
		{display: "W  1  worker  40%", state: programstatus.Working},
	}
	host, store, ws := newStatusStoreTest(t)
	for i := range entries {
		entries[i].ws = ws
		entries[i].uri = statusStoreTestURI(t, "file:///"+strings.Repeat("a", i+1))
	}
	p := store.view()
	t.Cleanup(func() { require.NoError(t, p.Close()) })
	p.set(entries)
	w, h := p.Dimensions()
	assert.Equal(t, statusViewMinWidth, w)
	assert.Equal(t, 3, h)

	handlertest.RunHandlerSequence(t, p, 30, 3, []handlertest.SequenceTestCase{
		{InputSequence: "", Expected: `
B  1  claude  run make test?  
E  2  build  boom             
W  1  worker  40%             `[1:]},
		{InputSequence: "j", Expected: `
B  1  claude  run make test?  
E  2  build  boom             
W  1  worker  40%             `[1:]},
	})
	sel, ok := p.Selection()
	require.True(t, ok)
	assert.Equal(t, "E  2  build  boom", sel)

	exit, handled := p.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowDown})
	assert.False(t, exit)
	assert.True(t, handled)
	exit, handled = p.Handle(term.Event{Type: term.EventKey, Ch: 'k'})
	assert.False(t, exit)
	assert.True(t, handled)
	exit, handled = p.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	assert.True(t, exit)
	assert.True(t, handled)
	assert.Empty(t, host.shown)
	host.drain()
	assert.Equal(t, []statusKey{{ws: ws.String(), uri: entries[1].uri.String()}}, host.shown)

	exit, handled = p.Handle(term.Event{Type: term.EventKey, Key: term.KeyEsc})
	assert.True(t, exit)
	assert.True(t, handled)
	host.drain()
	assert.Len(t, host.shown, 1, "escape selects nothing")
}

func TestStatusViewEmpty(t *testing.T) {
	host, store, _ := newStatusStoreTest(t)
	p := store.view()
	t.Cleanup(func() { require.NoError(t, p.Close()) })
	w, h := p.Dimensions()
	assert.Equal(t, statusViewMinWidth, w)
	assert.Equal(t, 1, h)
	handlertest.RunHandlerSequence(t, p, 30, 1, []handlertest.SequenceTestCase{
		{InputSequence: "", Expected: statusViewEmpty + "  "},
	})
	exit, handled := p.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	assert.True(t, exit)
	assert.True(t, handled)
	_, ok := p.Selection()
	assert.False(t, ok)
	host.drain()
	assert.Empty(t, host.shown)
}

func detachedTestReport(state programstatus.State, msg string) programstatus.Report {
	return programstatus.Report{State: state, Progress: -1, Msg: msg}
}

type statusStoreTestHost struct {
	workspaces map[string]string
	names      map[statusKey]string
	changed    []workspaceapi.URI
	shown      []statusKey
	queued     []func()
}

func (h *statusStoreTestHost) statusWorkspace(ws workspaceapi.URI) (int, string, bool) {
	name, ok := h.workspaces[ws.String()]
	return 0, name, ok
}
func (h *statusStoreTestHost) statusTabName(ws, uri workspaceapi.URI) (string, bool) {
	name, ok := h.names[statusKey{ws.String(), uri.String()}]
	return name, ok
}
func (h *statusStoreTestHost) statusInSight(workspaceapi.URI, workspaceapi.URI) bool               { return false }
func (h *statusStoreTestHost) notifyStatus(workspaceapi.URI, browserapi.NotificationLevel, string) {}
func (h *statusStoreTestHost) statusChanged(ws workspaceapi.URI)                                   { h.changed = append(h.changed, ws) }
func (h *statusStoreTestHost) showStatus(ws, uri workspaceapi.URI) {
	h.shown = append(h.shown, statusKey{ws.String(), uri.String()})
}
func (h *statusStoreTestHost) schedule(fn func()) bool { h.queued = append(h.queued, fn); return true }
func (h *statusStoreTestHost) drain() {
	queued := h.queued
	h.queued = nil
	for _, fn := range queued {
		fn()
	}
}

func statusStoreTestURI(t *testing.T, raw string) workspaceapi.URI {
	t.Helper()
	uri, err := workspaceapi.ParseURI(raw)
	require.NoError(t, err)
	return uri
}

func newStatusStoreTest(t *testing.T) (*statusStoreTestHost, *statusStore, workspaceapi.URI) {
	t.Helper()
	ws := statusStoreTestURI(t, "file:///workspace")
	host := &statusStoreTestHost{workspaces: map[string]string{ws.String(): "1 home"}, names: make(map[statusKey]string)}
	return host, newStatusStore(host, browser.TabStatusConfig{}, tabStatusNotifyConfig{}, host.schedule), ws
}

type statusStoreDetachedTest struct {
	store *statusStore
	ws    workspaceapi.URI
}

func newStatusStoreDetachedTest(t *testing.T) statusStoreDetachedTest {
	_, store, ws := newStatusStoreTest(t)
	return statusStoreDetachedTest{store, ws}
}
func (d *statusStoreDetachedTest) get(uri workspaceapi.URI) (programstatus.Summary, bool) {
	if d.store == nil {
		return programstatus.Summary{}, false
	}
	return d.store.status(d.ws, uri)
}
func (d *statusStoreDetachedTest) list() []statusItem {
	if d.store == nil {
		return nil
	}
	return d.store.items()
}
func (d *statusStoreDetachedTest) rollup() (programstatus.Summary, workspaceapi.URI) {
	if d.store == nil {
		return programstatus.Summary{Progress: -1}, workspaceapi.URI{}
	}
	return d.store.rollup(d.ws)
}
func (d *statusStoreDetachedTest) update(uri workspaceapi.URI, fn func(*programstatus.Store)) (programstatus.Summary, programstatus.Summary, bool) {
	prev, ok := d.get(uri)
	if !ok {
		prev = programstatus.Summary{Progress: -1}
	}
	host := d.store.host.(*statusStoreTestHost)
	n := len(host.changed)
	d.store.updateDetached(d.ws, uri, fn)
	next, ok := d.get(uri)
	if !ok {
		next = programstatus.Summary{Progress: -1}
	}
	return prev, next, len(host.changed) != n
}

func TestStatusStoreLiveViews(t *testing.T) {
	host, store, ws := newStatusStoreTest(t)
	a, b := statusStoreTestURI(t, "file:///a"), statusStoreTestURI(t, "file:///b")
	v, other := store.view(), store.view()
	t.Cleanup(func() { require.NoError(t, v.Close()); require.NoError(t, other.Close()) })
	v.Resize(60, 8)
	apply := func(uri workspaceapi.URI, id string, state programstatus.State) {
		store.updateDetached(ws, uri, func(r *programstatus.Store) { r.Apply(programstatus.Report{ID: id, State: state, Progress: -1}) })
	}
	apply(a, "", programstatus.Working)
	apply(b, "", programstatus.Done)
	require.Len(t, v.entries, 2)
	other.Handle(term.Event{Type: term.EventKey, Ch: 'k'})
	assert.Equal(t, a, v.entries[v.list.FocusOffset()].uri)
	assert.Equal(t, b, other.entries[other.list.FocusOffset()].uri)
	apply(a, "", programstatus.Blocked)
	assert.Equal(t, 0, v.list.FocusOffset(), "focus follows a reordered source")
	apply(a, "tool/edit", programstatus.Working)
	v.Handle(term.Event{Type: term.EventKey, Ch: 'j'})
	require.Equal(t, "tool/edit", v.entries[v.list.FocusOffset()].record)
	apply(b, "", programstatus.Blocked)
	apply(a, "", programstatus.Working)
	assert.Equal(t, a, v.entries[v.list.FocusOffset()].uri)
	assert.Equal(t, "tool/edit", v.entries[v.list.FocusOffset()].record)
	w := term.NewStringWriter(60, 8)
	v.Draw(w)
	require.NoError(t, w.Flush())
	assert.Contains(t, w.String(), "edit", "rebuilt rows retain the open view's size")
	require.NoError(t, other.Close())
	frozen := append([]statusEntry(nil), other.entries...)
	apply(a, "tool/edit", programstatus.Done)
	assert.Equal(t, frozen, other.entries)
	assert.Len(t, store.views, 1)
	exit, handled := v.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	assert.True(t, exit)
	assert.True(t, handled)
	assert.Empty(t, host.shown)
	host.drain()
	assert.Equal(t, []statusKey{{ws.String(), a.String()}}, host.shown)
	apply(a, "", programstatus.Clear)
	require.Len(t, v.entries, 1)
	assert.Equal(t, b, v.entries[v.list.FocusOffset()].uri)
	apply(b, "", programstatus.Clear)
	assert.Empty(t, v.entries)
	_, ok := v.Selection()
	assert.False(t, ok)
	w = term.NewStringWriter(60, 8)
	v.Draw(w)
	require.NoError(t, w.Flush())
	assert.Contains(t, w.String(), statusViewEmpty)
}

func TestStatusStoreWorkspaceCleanup(t *testing.T) {
	host, store, ws := newStatusStoreTest(t)
	other := statusStoreTestURI(t, "file:///other")
	uri := statusStoreTestURI(t, "file:///terminal")
	host.workspaces[other.String()] = "2 other"
	for _, workspace := range []workspaceapi.URI{ws, other} {
		store.updateDetached(workspace, uri, func(r *programstatus.Store) { r.Apply(detachedTestReport(programstatus.Error, "boom")) })
		key := statusKey{workspace.String(), uri.String()}
		store.lastNotified[key] = time.Now()
		store.pendingNotify[key] = 1
	}
	v := store.view()
	defer v.Close()
	require.Len(t, v.entries, 2)
	store.dropWorkspace(ws)
	require.Len(t, v.entries, 1)
	assert.Equal(t, other, v.entries[0].ws)
	_, ok := store.status(ws, uri)
	assert.False(t, ok)
	assert.Len(t, store.lastNotified, 1)
	assert.Len(t, store.pendingNotify, 1)
	n := len(host.changed)
	store.dropWorkspace(ws)
	assert.Len(t, host.changed, n)
	delete(host.workspaces, other.String())
	assert.Empty(t, store.items(), "workspaces without a slot are hidden")
	store.dropWorkspace(other)
	assert.Empty(t, store.sources)
	assert.Empty(t, store.lastNotified)
	assert.Empty(t, store.pendingNotify)
	assert.Empty(t, v.entries)
}

func TestStatusStoreSourceBecomesTab(t *testing.T) {
	host, store, ws := newStatusStoreTest(t)
	uri := statusStoreTestURI(t, "file:///terminal")
	store.updateDetached(ws, uri, func(r *programstatus.Store) {
		r.Apply(detachedTestReport(programstatus.Working, ""))
	})
	v := store.view()
	defer v.Close()
	host.names[statusKey{ws.String(), uri.String()}] = "build"
	records := programstatus.New()
	records.Apply(detachedTestReport(programstatus.Working, ""))
	store.tabStatusChanged(ws, uri, records.Summary())
	require.Nil(t, store.sources[statusKey{ws.String(), uri.String()}].records)
	require.Len(t, v.entries, 1)
	assert.Contains(t, v.entries[0].display, "build")
	assert.NotContains(t, v.entries[0].display, detachedStatusLabel)
}

func TestStatusViewCellWidth(t *testing.T) {
	_, store, _ := newStatusStoreTest(t)
	v := store.view()
	defer v.Close()
	v.set([]statusEntry{{display: strings.Repeat("界", 30)}})
	width, height := v.Dimensions()
	assert.Equal(t, 60, width)
	assert.Equal(t, 1, height)
}

type statusStoreTestTabs struct {
	browser.TabManager
	component *browser.Component
}

type statusStoreTestHandler struct{ handler.TestHandler }

func (*statusStoreTestHandler) Close() error { return nil }
func (b statusStoreTestTabs) SetTabStatus(uri workspaceapi.URI, r programstatus.Report) error {
	if !b.component.SetTabStatus(uri, r) {
		return errors.New("missing tab")
	}
	return nil
}
func (b statusStoreTestTabs) EndTabStatus(uri workspaceapi.URI) error {
	if !b.component.EndTabStatus(uri) {
		return errors.New("missing tab")
	}
	return nil
}

func TestStatusStoreTabLifecycle(t *testing.T) {
	host, store, ws := newStatusStoreTest(t)
	cfg := browser.DefaultConfig()
	cfg.OnTabStatus = func(uri workspaceapi.URI, _, next programstatus.Summary) { store.tabStatusChanged(ws, uri, next) }
	b := browser.NewComponent(cfg)
	t.Cleanup(func() { require.NoError(t, b.Close()) })
	uri := statusStoreTestURI(t, "file:///tab")
	host.names[statusKey{ws.String(), uri.String()}] = "build"
	h := &statusStoreTestHandler{}
	tab := b.NewTab(uri, 'b', "build", h, h)
	tabs := statusStoreTestTabs{component: b}
	v := store.view()
	defer v.Close()
	for _, state := range []programstatus.State{programstatus.Done, programstatus.Error} {
		store.setTabStatus(ws, tabs, uri, detachedTestReport(state, "result"))
		require.Len(t, v.entries, 1)
		assert.Contains(t, v.entries[0].display, "build")
		require.Nil(t, store.sources[statusKey{ws.String(), uri.String()}].records)
		tab.AckStatus()
		assert.Empty(t, v.entries)
		_, ok := store.status(ws, uri)
		assert.False(t, ok)
	}
	store.setTabStatus(ws, tabs, uri, programstatus.Report{ID: "working", State: programstatus.Working, Progress: -1})
	store.setTabStatus(ws, tabs, uri, programstatus.Report{ID: "done", State: programstatus.Done, Progress: -1})
	store.endTabStatus(ws, tabs, uri)
	sum, ok := store.status(ws, uri)
	require.True(t, ok)
	require.Len(t, sum.Records, 1)
	assert.Equal(t, programstatus.Done, sum.State)
	require.True(t, b.RemoveTab(tab))
	assert.Empty(t, v.entries)
	_, ok = store.status(ws, uri)
	assert.False(t, ok)
	require.Error(t, store.setTabStatus(ws, tabs, uri, detachedTestReport(programstatus.Working, "")))
	require.Error(t, store.endTabStatus(ws, tabs, uri))
	assert.Empty(t, store.sources, "a tab error must not create a detached source")
	delete(host.names, statusKey{ws.String(), uri.String()})
	store.setTabStatus(ws, tabs, uri, detachedTestReport(programstatus.Working, ""))
	require.Len(t, v.entries, 1)
	assert.Contains(t, v.entries[0].display, detachedStatusLabel)
	store.endTabStatus(ws, tabs, uri)
	assert.Empty(t, v.entries)
}

func TestStatusSourceLabel(t *testing.T) {
	for _, tc := range []struct {
		name, app, want string
	}{
		{"rolling-fox", "", "rolling-fox"},
		{"rolling-fox", "rune-agent", "rolling-fox (rune-agent)"},
		{"cargo", "cargo", "cargo"},
		{detachedStatusLabel, "", detachedStatusLabel},
		{detachedStatusLabel, "make", "make"},
		{"", "make", "make"},
		{"", "", ""},
	} {
		assert.Equal(t, tc.want, statusSourceLabel(tc.name, tc.app), "%q, %q", tc.name, tc.app)
	}
}

func TestStatusStoreDetached(t *testing.T) {
	idle := programstatus.Summary{Progress: -1}
	parse := func(s string) workspaceapi.URI {
		uri, err := workspaceapi.ParseURI(s)
		require.NoError(t, err)
		return uri
	}
	ttyA := parse("file:///dev/ttys001")
	ttyB := parse("file:///dev/ttys002")
	clearAll := programstatus.Report{State: programstatus.Clear}
	apply := func(d *statusStoreDetachedTest, uri workspaceapi.URI, r programstatus.Report) (
		prev, status programstatus.Summary, changed bool,
	) {
		return d.update(uri, func(s *programstatus.Store) { s.Apply(r) })
	}

	t.Run("zero value reads as empty", func(t *testing.T) {
		var d statusStoreDetachedTest
		_, ok := d.get(ttyA)
		assert.False(t, ok)
		assert.Empty(t, d.list())
		sum, uri := d.rollup()
		assert.Equal(t, idle, sum)
		assert.Equal(t, workspaceapi.URI{}, uri)
	})

	t.Run("update reports both statuses and whether they differ", func(t *testing.T) {
		d := newStatusStoreDetachedTest(t)
		prev, status, changed := apply(&d, ttyA, detachedTestReport(programstatus.Working, ""))
		assert.True(t, changed)
		assert.Equal(t, idle, prev, "an unknown uri was idle")
		assert.Equal(t, programstatus.Working, status.State)

		working := status
		prev, status, changed = apply(&d, ttyA, detachedTestReport(programstatus.Working, ""))
		assert.False(t, changed, "the same report again is not a change")
		assert.True(t, working.Equal(prev))
		assert.True(t, working.Equal(status))

		prev, status, changed = apply(&d, ttyA, detachedTestReport(programstatus.Done, "ok"))
		assert.True(t, changed)
		assert.True(t, working.Equal(prev))
		got, ok := d.get(ttyA)
		require.True(t, ok)
		assert.True(t, status.Equal(got))
		assert.Equal(t, "ok", got.Msg)
	})

	t.Run("ending keeps done and error", func(t *testing.T) {
		d := newStatusStoreDetachedTest(t)
		_, _, _ = apply(&d, ttyA, programstatus.Report{ID: "w", State: programstatus.Working, Progress: -1})
		_, _, _ = apply(&d, ttyA, programstatus.Report{ID: "d", State: programstatus.Done, Progress: -1})
		_, status, changed := d.update(ttyA, func(s *programstatus.Store) { s.DropTransient() })
		assert.True(t, changed)
		assert.Equal(t, programstatus.Done, status.State)
		require.Len(t, status.Records, 1)
	})

	t.Run("no record left forgets the uri", func(t *testing.T) {
		d := newStatusStoreDetachedTest(t)
		_, done, _ := apply(&d, ttyA, detachedTestReport(programstatus.Done, "ok"))

		prev, status, changed := apply(&d, ttyA, clearAll)
		assert.True(t, changed)
		assert.True(t, done.Equal(prev))
		assert.Equal(t, idle, status)
		_, ok := d.get(ttyA)
		assert.False(t, ok)
		assert.Empty(t, d.list())

		_, _, changed = apply(&d, ttyA, clearAll)
		assert.False(t, changed, "clearing an unknown uri is not a change")
		assert.Empty(t, d.list())
	})

	t.Run("a reported idle is kept apart from silence", func(t *testing.T) {
		d := newStatusStoreDetachedTest(t)
		_, reportedIdle, changed := apply(&d, ttyA, detachedTestReport(programstatus.Idle, ""))
		assert.True(t, changed)
		require.Len(t, reportedIdle.Records, 1)
		got, ok := d.get(ttyA)
		require.True(t, ok)
		assert.True(t, reportedIdle.Equal(got))
		sum, uri := d.rollup()
		assert.True(t, reportedIdle.Equal(sum))
		assert.Equal(t, ttyA, uri)
	})

	t.Run("list is sorted by uri", func(t *testing.T) {
		d := newStatusStoreDetachedTest(t)
		_, _, _ = apply(&d, ttyB, detachedTestReport(programstatus.Working, ""))
		_, _, _ = apply(&d, ttyA, detachedTestReport(programstatus.Done, ""))
		got := d.list()
		require.Len(t, got, 2)
		assert.Equal(t, ttyA, got[0].uri)
		assert.Equal(t, programstatus.Done, got[0].status.State)
		assert.Equal(t, ttyB, got[1].uri)
		assert.Equal(t, programstatus.Working, got[1].status.State)
	})

	t.Run("rollup picks the most pressing status", func(t *testing.T) {
		d := newStatusStoreDetachedTest(t)
		_, _, _ = apply(&d, ttyA, detachedTestReport(programstatus.Working, ""))
		sum, uri := d.rollup()
		assert.Equal(t, programstatus.Working, sum.State)
		assert.Equal(t, ttyA, uri)

		_, _, _ = apply(&d, ttyB, detachedTestReport(programstatus.Error, "boom"))
		sum, uri = d.rollup()
		assert.Equal(t, programstatus.Error, sum.State)
		assert.Equal(t, "boom", sum.Msg)
		assert.Equal(t, ttyB, uri)

		_, _, _ = apply(&d, ttyA, detachedTestReport(programstatus.Blocked, "approve?"))
		sum, uri = d.rollup()
		assert.Equal(t, programstatus.Blocked, sum.State)
		assert.Equal(t, ttyA, uri)

		_, _, _ = apply(&d, ttyA, clearAll)
		_, _, _ = apply(&d, ttyB, clearAll)
		sum, uri = d.rollup()
		assert.Equal(t, idle, sum)
		assert.Equal(t, workspaceapi.URI{}, uri)
	})

	t.Run("rollup breaks ties by uri order", func(t *testing.T) {
		d := newStatusStoreDetachedTest(t)
		_, _, _ = apply(&d, ttyB, detachedTestReport(programstatus.Error, "b"))
		_, _, _ = apply(&d, ttyA, detachedTestReport(programstatus.Error, "a"))
		sum, uri := d.rollup()
		assert.Equal(t, "a", sum.Msg)
		assert.Equal(t, ttyA, uri)
	})
}
